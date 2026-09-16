#!/usr/bin/env python3
# Vehicle Gateway
#
# 定位（架构文档 §5.1）：车云通信的唯一入口、无图形化常驻后台。
# 通道：
#   - 车端组件（adapter/仲裁器）经长度前缀 Protobuf Unix Domain Socket 接入
#   - 只从本机受信控制中继接收 Protobuf 二进制控制命令（UDP），做第一道过期预筛，再路由给车端组件
#   - 遥测/心跳经 mTLS + MQTT 上行；无可信 broker 时拒绝启动
#
# 防御纵深：网关只做“明显过期”预筛；租约/fencing/限幅等完整裁决
# 仍在车端安全仲裁器完成，云端不可绕过。
#
# 用法：
#   python3 gateway.py --vehicle-id VEHICLE_ID --gateway-id GATEWAY_ID

import argparse
import base64
import ctypes
import os
import socket
import struct
import sys
import threading
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "protocols" / "gen" / "python"))
from google.protobuf.message import DecodeError  # noqa: E402
from robot_agent_platform.v1 import capabilities_pb2, compatibility, control_pb2, envelope_pb2, local_pb2, map_pb2, navigation_pb2, telemetry_pb2, workspace_pb2  # noqa: E402
from robot_agent_platform.v1 import auth as proto_auth  # noqa: E402

SCHEMA_MAJOR = 1
SCHEMA_MINOR = 0
MAX_MSG_BYTES = 1024 * 1024  # 每类消息最大长度：解析前检查，防内存攻击
MAX_PENDING_RETURNS = 4096  # 控制回执路由上限，防止异常链路耗尽内存
LOCAL_PROTOCOL = "platform.v1.local"
LOCAL_FRAME_HEADER = 4


def peer_credentials(conn):
    """Return (uid, gid) from the Unix socket peer or None if unavailable."""
    getpeereid = getattr(conn, "getpeereid", None)
    if getpeereid is not None:
        try:
            return tuple(int(v) for v in getpeereid())
        except OSError:
            pass
    # CPython on macOS does not expose libc's getpeereid(3) on socket
    # objects, while LOCAL_PEERCRED does not expose the peer's primary GID.
    # Use the OS API directly so configured UID/GID bindings remain enforced
    # on both Linux and macOS.
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        getpeereid_fn = getattr(libc, "getpeereid")
        getpeereid_fn.argtypes = [ctypes.c_int,
                                  ctypes.POINTER(ctypes.c_uint),
                                  ctypes.POINTER(ctypes.c_uint)]
        getpeereid_fn.restype = ctypes.c_int
        uid = ctypes.c_uint()
        gid = ctypes.c_uint()
        if getpeereid_fn(conn.fileno(), ctypes.byref(uid), ctypes.byref(gid)) == 0:
            return int(uid.value), int(gid.value)
    except (AttributeError, OSError, TypeError, ValueError):
        pass
    option = getattr(socket, "SO_PEERCRED", 17)  # Linux
    try:
        raw = conn.getsockopt(socket.SOL_SOCKET, option, struct.calcsize("3i"))
        pid, uid, gid = struct.unpack("3i", raw)
        _ = pid
        return uid, gid
    except (OSError, struct.error):
        return None


def mono_ns():
    return time.monotonic_ns()


def make_envelope(vehicle_id, gateway_id, message_type, payload, session_id,
                  sequence, ttl_ms=5000, auth_key=None):
    """Build the only Gateway cloud envelope representation: protobuf."""
    if not isinstance(payload, bytes) or not payload:
        raise ValueError("MQTT Envelope payload must be non-empty protobuf bytes")
    env = envelope_pb2.Envelope(
        schema_major=SCHEMA_MAJOR, schema_minor=SCHEMA_MINOR,
        message_type=message_type, vehicle_id=vehicle_id, gateway_id=gateway_id,
        session_id=session_id, sequence=sequence, utc_time_ns=time.time_ns(),
        monotonic_time_ns=mono_ns(), ttl_ms=ttl_ms,
        trace_id=uuid.uuid4().hex[:16], payload=payload,
    )
    if auth_key is not None:
        proto_auth.sign_envelope(env, auth_key)
    return env


def parse_binary_control(data, vehicle_id, gateway_id, auth_key=None):
    """Decode and pre-screen the cross-host protobuf control datagram.

    The Gateway cannot verify the command MAC without the short-lived secret;
    it does verify the transport envelope shape, identity, auth-tag presence,
    and command/envelope consistency before forwarding to the Arbiter.
    """
    try:
        env = envelope_pb2.Envelope.FromString(data)
        proto_auth.validate_envelope(
            env,
            "platform.v1.ControlCommand",
            vehicle_id,
            gateway_id,
        )
        command = control_pb2.ControlCommand.FromString(env.payload)
    except (DecodeError, TypeError, ValueError):
        return None, None
    if len(env.auth_tag) != 32:
        return None, None
    if auth_key is not None and not proto_auth.verify_envelope_auth(env, auth_key):
        return None, None
    if (
        not command.control_session_id
        or command.control_session_id != env.session_id
        or command.command_sequence <= 0
        or command.command_sequence != env.sequence
        or not command.lease_id
        or command.fencing_token <= 0
        or command.ttl_ms != env.ttl_ms
        or env.ttl_ms > 1000
        or len(command.end_to_end_mac) != 32
        or command.mode == control_pb2.CONTROL_MODE_UNSPECIFIED
    ):
        return None, None
    return env, command


def binary_ack(vehicle_id, gateway_id, control_env, command, result, detail, auth_key=None):
    """Build the binary ACK returned to the control client.

    A local Arbiter ACK is converted into this representation at the Gateway
    boundary. The ACK authentication key is deliberately not held by Gateway;
    the vehicle-side Arbiter remains the authority for command execution.
    """
    ack = control_pb2.ControlAck(
        control_session_id=command.control_session_id,
        command_sequence=command.command_sequence,
        result=result,
        detail=detail,
    )
    env = envelope_pb2.Envelope(
        schema_major=1,
        schema_minor=0,
        message_type="platform.v1.ControlAck",
        vehicle_id=vehicle_id,
        gateway_id=gateway_id,
        session_id=control_env.session_id,
        sequence=command.command_sequence,
        utc_time_ns=time.time_ns(),
        monotonic_time_ns=mono_ns(),
        ttl_ms=1000,
        trace_id=control_env.trace_id,
        payload=ack.SerializeToString(deterministic=True),
    )
    if auth_key is not None:
        proto_auth.sign_envelope(env, auth_key)
    return env.SerializeToString(deterministic=True)


def control_proto_to_uds(env, command):
    """Wrap the original signed protobuf envelope for the local Arbiter.

    The local boundary deliberately carries the exact bytes received from the
    control relay. No JSON conversion is allowed because changing protobuf
    fields at this boundary would invalidate the end-to-end auth_tag.
    """
    if command.mode == control_pb2.CONTROL_MODE_TARGET_MOTION and not command.HasField("motion"):
        return None
    if command.mode == control_pb2.CONTROL_MODE_MINIMAL_RISK and not command.HasField("minimal_risk"):
        return None
    if command.mode not in (
        control_pb2.CONTROL_MODE_TARGET_MOTION,
        control_pb2.CONTROL_MODE_MINIMAL_RISK,
    ):
        return None
    return local_pb2.LocalFrame(
        kind=local_pb2.LocalFrame.KIND_ENVELOPE,
        component="gateway",
        protocol=LOCAL_PROTOCOL,
        envelope=env,
    )


def uds_ack_to_binary(env, vehicle_id, gateway_id, auth_key=None):
    """Return an Arbiter's protected local protobuf ACK as wire bytes."""
    if not isinstance(env, envelope_pb2.Envelope):
        return None
    try:
        if len(env.auth_tag) != 32:
            return None
        ack = control_pb2.ControlAck.FromString(env.payload)
        if not ack.control_session_id or ack.command_sequence <= 0:
            return None
        proto_auth.validate_envelope(env, "platform.v1.ControlAck", vehicle_id, gateway_id)
        if auth_key is not None and not proto_auth.verify_envelope_auth(env, auth_key):
            return None
        return env.SerializeToString(deterministic=True)
    except (TypeError, ValueError, DecodeError):
        return None


def encode_local_frame(frame):
    """Encode one local protobuf frame using a bounded big-endian length prefix."""
    if not isinstance(frame, local_pb2.LocalFrame):
        raise TypeError("local frame must be LocalFrame")
    raw = frame.SerializeToString(deterministic=True)
    if not 0 < len(raw) <= MAX_MSG_BYTES:
        raise ValueError("本机 Protobuf 帧超过大小上限")
    return struct.pack(">I", len(raw)) + raw


def hello_frame(component):
    return local_pb2.LocalFrame(
        kind=local_pb2.LocalFrame.KIND_HELLO,
        component=component,
        protocol=LOCAL_PROTOCOL,
    )


def envelope_frame(env, component="gateway"):
    return local_pb2.LocalFrame(
        kind=local_pb2.LocalFrame.KIND_ENVELOPE,
        component=component,
        protocol=LOCAL_PROTOCOL,
        envelope=env,
    )


class Gateway:
    def __init__(self, args):
        self.uds_path = args.uds
        self.heartbeat_hz = args.heartbeat_hz
        self.vehicle_id = args.vehicle_id
        self.gateway_id = args.gateway_id
        self.session_id = "gateway-" + uuid.uuid4().hex
        self.envelope_auth_key = args.envelope_auth_key
        self.component_identities = dict(args.component_identities)
        required_components = {"adapter", "arbiter", "workspace"}
        if not required_components.issubset(self.component_identities) or \
                not set(self.component_identities).issubset(required_components | {"map"}):
            raise ValueError("必须为 adapter/arbiter/workspace 分别配置本机 UID/GID；map 可选")

        self.udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        # 控制 UDP 只允许车端本机 relay 接入。公网/蜂窝链路不能直达 Gateway；
        # 真实部署使用 mTLS QUIC relay，再由本机 UDS/loopback 转入 Arbiter。
        self.udp.bind((args.control_bind, args.control))
        self.udp.settimeout(0.05)
        self.control_port = args.control

        self.components = {}       # UDS 连接 -> 已声明角色；未握手者不收控制
        self.component_peers = {}  # UDS 连接 -> kernel-reported (uid, gid)
        self.returns = {}          # (session, seq) -> [控制端地址...]（双链路：每条来路都要回执）
        self.return_expiry = {}    # (session, seq) -> monotonic deadline
        self.lock = threading.Lock()
        self.stats = {"uplink": 0, "down": 0, "ack": 0, "prefilter": 0}
        self.uplink = MQTTUplink(args.mqtt_host, args.mqtt_port,
                                 args.ca, args.cert, args.key, self.vehicle_id,
                                 self.gateway_id, self._on_lease_grant,
                                 self._on_terminal_grant,
                                 self._on_navigation_command,
                                 self._on_map_publication_command,
                                 self.envelope_auth_key)

    # ---------- UDS：车端组件接入 ----------

    def start_uds(self):
        if os.path.exists(self.uds_path):
            os.unlink(self.uds_path)
        srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        srv.bind(self.uds_path)
        os.chmod(self.uds_path, 0o660)
        srv.listen(8)
        threading.Thread(target=self._accept_loop, args=(srv,), daemon=True).start()
        print(f"[gateway] UDS 就绪：{self.uds_path}")

    def _accept_loop(self, srv):
        while True:
            conn, _ = srv.accept()
            peer = peer_credentials(conn)
            with self.lock:
                self.components[conn] = ""
                self.component_peers[conn] = peer
                count = len(self.components)
            print(f"[gateway] 车端组件接入（当前 {count} 个，等待角色握手）")
            threading.Thread(target=self._component_reader,
                             args=(conn,), daemon=True).start()

    def _send_uds(self, conn, frame):
        try:
            conn.sendall(encode_local_frame(frame))
            return True
        except (OSError, TypeError, ValueError):
            return False

    def _component_reader(self, conn):
        buf = bytearray()
        while True:
            try:
                data = conn.recv(65535)
            except OSError:
                break
            if not data:
                break
            buf.extend(data)
            while len(buf) >= LOCAL_FRAME_HEADER:
                frame_len = struct.unpack(">I", buf[:LOCAL_FRAME_HEADER])[0]
                if frame_len == 0 or frame_len > MAX_MSG_BYTES:
                    print("[gateway] 拒绝超限或空的本机 Protobuf 帧")
                    return
                if len(buf) < LOCAL_FRAME_HEADER + frame_len:
                    break
                raw = bytes(buf[LOCAL_FRAME_HEADER:LOCAL_FRAME_HEADER + frame_len])
                del buf[:LOCAL_FRAME_HEADER + frame_len]
                try:
                    frame = local_pb2.LocalFrame.FromString(raw)
                except DecodeError:
                    print("[gateway] 拒绝无法解析的本机 Protobuf 帧")
                    continue
                if frame.protocol != LOCAL_PROTOCOL:
                    print("[gateway] 拒绝未知本机协议版本")
                    return
                if frame.kind == local_pb2.LocalFrame.KIND_HELLO:
                    role = frame.component
                    if role not in ("adapter", "arbiter", "workspace", "map"):
                        print("[gateway] 拒绝未知车端组件角色")
                        return
                    with self.lock:
                        peer = self.component_peers.get(conn)
                    expected = self.component_identities.get(role)
                    if (peer is None or expected is None or peer[0] != expected[0] or
                            expected[1] is not None and peer[1] != expected[1]):
                        print(f"[gateway] 拒绝组件 {role}：Unix peer UID/GID 不匹配")
                        return
                    with self.lock:
                        if conn in self.components:
                            self.components[conn] = role
                    print(f"[gateway] 组件角色已确认：{role}")
                    continue
                if frame.kind != local_pb2.LocalFrame.KIND_ENVELOPE or not frame.HasField("envelope"):
                    print("[gateway] 拒绝非 Envelope 本机业务帧")
                    continue
                env = frame.envelope
                try:
                    proto_auth.validate_envelope(env, "", self.vehicle_id, self.gateway_id)
                except (TypeError, ValueError):
                    print("[gateway] 拒绝无效的车端 Protobuf 信封")
                    continue
                mtype = env.message_type
                with self.lock:
                    role = self.components.get(conn, "")
                if mtype.endswith("ControlAck"):
                    if role != "arbiter":
                        print("[gateway] 拒绝非 Arbiter 组件提交 ControlAck")
                        continue
                    try:
                        ack = control_pb2.ControlAck.FromString(env.payload)
                    except DecodeError:
                        continue
                    key = (ack.control_session_id, ack.command_sequence)
                    with self.lock:
                        ret = self.returns.pop(key, None)
                        self.return_expiry.pop(key, None)
                    rets = ret if isinstance(ret, list) else ([ret] if ret else [])
                    ack_bytes = uds_ack_to_binary(env, self.vehicle_id, self.gateway_id,
                                                  self.envelope_auth_key)
                    if ack_bytes is None:
                        print("[gateway] 丢弃未认证的 Arbiter ControlAck")
                        continue
                    # ACK 同时上行到 Fleet 形成持久化执行证据；UDP/QUIC
                    # 回传只是操作端低延迟显示，不能作为唯一审计来源。
                    self._uplink_env(env)
                    for ret in rets:
                        self.udp.sendto(ack_bytes, ret)
                    self.stats["ack"] += 1
                elif mtype.endswith("NavigationAck"):
                    if role != "adapter":
                        print("[gateway] 拒绝非 Adapter 组件提交 NavigationAck")
                        continue
                    try:
                        ack = navigation_pb2.NavigationAck.FromString(env.payload)
                        if (ack.version != 1 or ack.vehicle_id != self.vehicle_id or
                                not ack.route_id or
                                ack.action == navigation_pb2.NAVIGATION_ACTION_UNSPECIFIED or
                                ack.result == navigation_pb2.NAVIGATION_RESULT_UNSPECIFIED):
                            raise ValueError("NavigationAck 字段无效")
                    except (DecodeError, ValueError):
                        continue
                    if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                        print("[gateway] 丢弃未认证的 Adapter NavigationAck")
                        continue
                    # ACK 是 Adapter 对真实导航栈结果的证明；Gateway 不自行
                    # 补回执，只把已认证的原始 Envelope 上送 Fleet。
                    self._uplink_env(env)
                elif mtype.endswith("MapPublicationAck"):
                    if role != "map":
                        print("[gateway] 拒绝非 Map Agent 组件提交 MapPublicationAck")
                        continue
                    try:
                        ack = map_pb2.MapPublicationAck.FromString(env.payload)
                        if (ack.vehicle_id != self.vehicle_id or not ack.publication_id or
                                not ack.map_id or ack.map_version <= 0):
                            raise ValueError("MapPublicationAck 字段无效")
                    except (DecodeError, ValueError):
                        continue
                    if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                        print("[gateway] 丢弃未认证的 MapPublicationAck")
                        continue
                    # ACK 先进入 Fleet 的可审计 MQTT 流；没有 Map Agent 就没有
                    # ACK，不能由 Gateway 自己补“成功”。
                    self._uplink_env(env)
                else:
                    if role != "adapter" or not mtype.endswith("SignalUpdate"):
                        print(f"[gateway] 拒绝组件 {role or '未握手'} 上行消息 {mtype}")
                        continue
                    if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                        print("[gateway] 拒绝 auth_tag 无效的车端遥测")
                        continue
                    try:
                        update = telemetry_pb2.SignalUpdate.FromString(env.payload)
                        proto_auth.validate_signal_update(update)
                    except (DecodeError, TypeError, ValueError) as exc:
                        print(f"[gateway] 拒绝无效车端遥测：{exc}")
                        continue
                    self._uplink_env(env)
                    self.stats["uplink"] += 1
        with self.lock:
            self.components.pop(conn, None)
            self.component_peers.pop(conn, None)
        try:
            conn.close()
        except OSError:
            pass

    def _uplink_env(self, env):
        """经已建立的 mTLS MQTT 会话上行，绝不回退到未认证 UDP。"""
        try:
            self.uplink.send(env)
        except Exception as exc:
            print(f"[gateway] MQTT 上行失败：{exc}")

    def _on_lease_grant(self, env):
        """LeaseGrant 只接受 mTLS MQTT 下行，拒绝裸 UDP 注入。

        Gateway 不负责判定签名、租约或 fencing；这些必须由独立的
        Safety Arbiter 验证。这里仅校验消息被送往正确的车/Gateway。
        """
        if not isinstance(env, envelope_pb2.Envelope):
            print("[gateway] 拒绝非 Protobuf LeaseGrant")
            return
        if env.vehicle_id != self.vehicle_id or env.gateway_id != self.gateway_id:
            print("[gateway] 拒绝身份不匹配的 LeaseGrant")
            return
        try:
            proto_auth.validate_envelope(env, "platform.v1.LeaseGrant",
                                         self.vehicle_id, self.gateway_id)
        except (TypeError, ValueError):
            print("[gateway] 拒绝无效的 LeaseGrant 信封")
            return
        n = self._forward_to_components(envelope_frame(env), role="arbiter")
        try:
            grant = control_pb2.LeaseGrant.FromString(env.payload)
            detail = f"lease={grant.lease_id} fencing={grant.fencing_token}"
        except DecodeError:
            detail = "payload=invalid"
        print(f"[gateway] mTLS LeaseGrant {detail} -> {n} 个组件")

    def _on_terminal_grant(self, env):
        """Workspace 授权只经 MQTT mTLS 到 Gateway，再转发本机 Protobuf。

        Gateway 不解析或代替终端授权；车端 Agent 会再次验证 Envelope HMAC
        和 Authority Ed25519 签名，确保任一中间层都不能伪造维护会话。
        """
        if not isinstance(env, envelope_pb2.Envelope):
            print("[gateway] 拒绝非 Protobuf TerminalGrant")
            return
        try:
            proto_auth.validate_envelope(env, "platform.v1.TerminalGrant",
                                         self.vehicle_id, self.gateway_id)
            if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                raise ValueError("Envelope.auth_tag 无效")
            grant = workspace_pb2.TerminalGrant.FromString(env.payload)
            if (grant.version != 1 or grant.vehicle_id != self.vehicle_id or
                    grant.gateway_id != self.gateway_id or not grant.session_id):
                raise ValueError("TerminalGrant 字段无效")
        except (TypeError, ValueError):
            print("[gateway] 拒绝无效的 TerminalGrant")
            return
        n = self._forward_to_components(envelope_frame(env), role="workspace")
        print(f"[gateway] mTLS TerminalGrant -> {n} 个 workspace 组件")

    def _on_navigation_command(self, env):
        """把已认证的导航命令送入 Adapter，绝不以 JSON 兼容层下发。"""
        try:
            proto_auth.validate_envelope(env, "platform.v1.NavigationCommand",
                                         self.vehicle_id, self.gateway_id)
            if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                raise ValueError("Envelope.auth_tag 无效")
            command = navigation_pb2.NavigationCommand.FromString(env.payload)
            if (command.version != 1 or command.vehicle_id != self.vehicle_id or
                    not command.route_id or command.action == navigation_pb2.NAVIGATION_ACTION_UNSPECIFIED):
                raise ValueError("NavigationCommand 字段无效")
        except (TypeError, ValueError, DecodeError) as exc:
            print(f"[gateway] 拒绝无效的 NavigationCommand：{exc}")
            return
        n = self._forward_to_components(envelope_frame(env), role="adapter")
        print(f"[gateway] NavigationCommand {command.route_id} -> {n} 个 adapter 组件")

    def _on_map_publication_command(self, env):
        """将地图发布命令转给真实 Map Agent；没有 Map Agent 时保持未确认。"""
        try:
            proto_auth.validate_envelope(env, "platform.v1.MapPublicationCommand",
                                         self.vehicle_id, self.gateway_id)
            if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                raise ValueError("Envelope.auth_tag 无效")
            command = map_pb2.MapPublicationCommand.FromString(env.payload)
            if (command.version != 1 or command.vehicle_id != self.vehicle_id or
                    not command.publication_id or not command.map_id or
                    command.map_version <= 0 or
                    command.action == map_pb2.MAP_PUBLICATION_ACTION_UNSPECIFIED):
                raise ValueError("MapPublicationCommand 字段无效")
        except (TypeError, ValueError, DecodeError) as exc:
            print(f"[gateway] 拒绝无效的 MapPublicationCommand：{exc}")
            return
        n = self._forward_to_components(envelope_frame(env), role="map")
        if n == 0:
            print(f"[gateway] 地图发布 {command.publication_id} 未转发：Map Agent 未连接（不会伪造 ACK）")
        else:
            print(f"[gateway] MapPublicationCommand {command.publication_id} -> {n} 个 map 组件")

    # ---------- UDP：控制命令下行 ----------

    def control_loop(self):
        while True:
            self._expire_return_routes()
            try:
                data, addr = self.udp.recvfrom(65535)
            except (socket.timeout, TimeoutError, OSError):
                continue
            if len(data) > MAX_MSG_BYTES:
                continue
            env, command = parse_binary_control(data, self.vehicle_id, self.gateway_id,
                                                self.envelope_auth_key)
            if env is None or command is None:
                self.stats["prefilter"] += 1
                continue
            mtype = env.message_type
            if mtype == "platform.v1.LeaseGrant":
                # LeaseGrant 不能从可被局域网注入的 UDP 口进入。
                self.stats["prefilter"] += 1
                print("[gateway] 拒绝 UDP LeaseGrant：租约仅允许经 mTLS MQTT 下行")
                continue
            if mtype != "platform.v1.ControlCommand":
                continue

            # 第一道预筛：跨机器不能比较 monotonic clock，故只以 UTC 做明显
            # 过期拦截；最终的 TTL、时钟偏差和 MAC 判断仍由 Arbiter 完成。
            age_ms = (time.time_ns() - env.utc_time_ns) / 1e6
            ttl_ms = env.ttl_ms
            if age_ms > ttl_ms:
                self.stats["prefilter"] += 1
                ack = binary_ack(self.vehicle_id, self.gateway_id, env, command,
                                 control_pb2.CONTROL_RESULT_REJECTED_STALE,
                                 f"网关预筛：过期 age={age_ms:.0f}ms > ttl={ttl_ms}ms",
                                 self.envelope_auth_key)
                self.udp.sendto(ack, addr)
                print(f"[gateway] 预筛拒绝过期命令 seq={command.command_sequence}")
                continue

            key = (command.control_session_id, command.command_sequence)
            with self.lock:
                if len(self.returns) >= MAX_PENDING_RETURNS and key not in self.returns:
                    self.stats["prefilter"] += 1
                    reject = binary_ack(self.vehicle_id, self.gateway_id, env, command,
                                        control_pb2.CONTROL_RESULT_FAILED,
                                        "网关回执路由容量已满", self.envelope_auth_key)
                    self.udp.sendto(reject, addr)
                    continue
                self.returns.setdefault(key, []).append(addr)
                self.return_expiry[key] = time.monotonic() + max(command.ttl_ms, 1000) / 1000
            local_frame = control_proto_to_uds(env, command)
            if local_frame is None:
                with self.lock:
                    self.returns.pop(key, None)
                    self.return_expiry.pop(key, None)
                reject = binary_ack(self.vehicle_id, self.gateway_id, env, command,
                                    control_pb2.CONTROL_RESULT_REJECTED_LIMIT,
                                    "Gateway 仅允许已注册的 TargetMotion 控制", self.envelope_auth_key)
                self.udp.sendto(reject, addr)
                continue
            n = self._forward_to_components(local_frame, role="arbiter")
            if n == 0:
                with self.lock:
                    self.returns.pop(key, None)
                    self.return_expiry.pop(key, None)
                reject = binary_ack(self.vehicle_id, self.gateway_id, env, command,
                                    control_pb2.CONTROL_RESULT_FAILED,
                                    "车端 Safety Arbiter 未连接", self.envelope_auth_key)
                self.udp.sendto(reject, addr)
                continue
            self.stats["down"] += 1
            print(f"[gateway] 控制命令下发 seq={command.command_sequence} -> {n} 个组件")

    def _expire_return_routes(self):
        now = time.monotonic()
        with self.lock:
            expired = [key for key, deadline in self.return_expiry.items() if deadline <= now]
            for key in expired:
                self.return_expiry.pop(key, None)
                self.returns.pop(key, None)

    def _forward_to_components(self, frame, role=None):
        with self.lock:
            comps = [c for c, component_role in self.components.items()
                     if role is None or component_role == role]
        ok = 0
        for c in comps:
            if self._send_uds(c, frame):
                ok += 1
        return ok

    # ---------- 心跳 ----------

    def heartbeat_loop(self):
        seq = 0
        interval = 1.0 / max(self.heartbeat_hz, 0.01)
        while True:
            time.sleep(interval)
            seq += 1
            status = telemetry_pb2.GatewayStatus(uptime_s=int(time.monotonic()))
            env = make_envelope(self.vehicle_id, self.gateway_id, "platform.v1.GatewayStatus",
                                status.SerializeToString(deterministic=True), self.session_id,
                                sequence=seq, ttl_ms=10000, auth_key=self.envelope_auth_key)
            self._uplink_env(env)
            if seq % 10 == 1:
                s = self.stats
                print(f"[gateway] 心跳 #{seq} | 上行 {s['uplink']} 下行 {s['down']} "
                      f"回执 {s['ack']} 预筛 {s['prefilter']}")


class MQTTUplink:
    """经 MQTT 5 / mTLS 上行（第 5b 步）。话题规划：
        gateway/{gateway}/vehicle/{vehicle}/register    启动时的注册与能力声明
        gateway/{gateway}/vehicle/{vehicle}/telemetry   遥测 SignalUpdate
        gateway/{gateway}/vehicle/{vehicle}/status      心跳 / Gateway 状态
    身份来自客户端证书（Broker 侧 use_identity_as_username + Gateway ACL）。
    """

    def __init__(self, host, port, ca, cert, key, vehicle_id, gateway_id, lease_handler,
                 terminal_handler, navigation_handler, map_handler, envelope_auth_key):
        import paho.mqtt.client as mqtt
        self.vehicle_id = vehicle_id
        self.gateway_id = gateway_id
        self.lease_handler = lease_handler
        self.terminal_handler = terminal_handler
        self.navigation_handler = navigation_handler
        self.map_handler = map_handler
        self.envelope_auth_key = envelope_auth_key
        self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                                  client_id=f"gw-{vehicle_id}",
                                  protocol=mqtt.MQTTv5)
        self.client.tls_set(ca_certs=ca, certfile=cert, keyfile=key)
        self.client.on_message = self._on_message
        self.client.on_connect = self._on_connect
        self.client.connect(host, port, keepalive=15)
        self.client.loop_start()

    def _on_connect(self, client, _userdata, _flags, _reason_code, _properties):
        client.subscribe(f"gateway/{self.gateway_id}/lease", qos=1)
        client.subscribe(f"gateway/{self.gateway_id}/workspace", qos=1)
        client.subscribe(f"gateway/{self.gateway_id}/navigation", qos=1)
        client.subscribe(f"gateway/{self.gateway_id}/map", qos=1)

    def _on_message(self, _client, _userdata, msg):
        if msg.topic not in (f"gateway/{self.gateway_id}/lease",
                             f"gateway/{self.gateway_id}/workspace",
                             f"gateway/{self.gateway_id}/navigation",
                             f"gateway/{self.gateway_id}/map"):
            return
        try:
            env = envelope_pb2.Envelope.FromString(msg.payload)
        except DecodeError:
            print("[gateway] 拒绝无法解析的 Protobuf LeaseGrant")
            return
        expected = ("platform.v1.LeaseGrant" if msg.topic.endswith("/lease") else
                    "platform.v1.TerminalGrant" if msg.topic.endswith("/workspace") else
                    "platform.v1.NavigationCommand" if msg.topic.endswith("/navigation") else
                    "platform.v1.MapPublicationCommand")
        if env.message_type != expected:
            print("[gateway] 拒绝非法的 MQTT 下行消息类型")
            return
        try:
            proto_auth.validate_envelope(env, expected,
                                         self.vehicle_id, self.gateway_id)
            if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                raise ValueError("LeaseGrant auth_tag 校验失败")
        except (TypeError, ValueError):
            print("[gateway] 拒绝无效的 LeaseGrant 信封")
            return
        if expected == "platform.v1.LeaseGrant":
            self.lease_handler(env)
        elif expected == "platform.v1.TerminalGrant":
            self.terminal_handler(env)
        elif expected == "platform.v1.NavigationCommand":
            self.navigation_handler(env)
        else:
            self.map_handler(env)

    def send(self, env):
        if not isinstance(env, envelope_pb2.Envelope):
            raise TypeError("MQTT 上行只能发送 Protobuf Envelope")
        if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
            raise ValueError("MQTT 上行 Envelope.auth_tag 无效")
        mtype = env.message_type
        if mtype.endswith("SignalUpdate"):
            kind = "telemetry"
        elif mtype.endswith("ControlAck"):
            kind = "ack"
        elif mtype.endswith("NavigationAck"):
            kind = "nav_ack"
        elif mtype.endswith("MapPublicationAck"):
            kind = "map_ack"
        elif mtype.endswith("Heartbeat") or mtype.endswith("GatewayStatus"):
            kind = "status"
        else:
            kind = "misc"
        topic = f"gateway/{self.gateway_id}/vehicle/{self.vehicle_id}/{kind}"
        self.client.publish(topic, env.SerializeToString(deterministic=True), qos=1)

    def register(self, capabilities_env):
        if not proto_auth.verify_envelope_auth(capabilities_env, self.envelope_auth_key):
            raise ValueError("GatewayCapabilities Envelope.auth_tag 无效")
        topic = f"gateway/{self.gateway_id}/vehicle/{self.vehicle_id}/register"
        # retained=True：注册/能力是车辆“当前状态”，晚订阅的云端服务也能拿到
        self.client.publish(topic, capabilities_env.SerializeToString(deterministic=True), qos=1, retain=True)


def load_envelope_auth_key(path):
    if not path:
        raise ValueError("必须配置 Envelope HMAC 密钥")
    st = os.stat(path)
    if st.st_mode & 0o077:
        raise ValueError("Envelope HMAC 密钥文件权限必须为 0600")
    with open(path, "rb") as f:
        key = base64.b64decode(f.read().strip(), validate=True)
    if len(key) < 32:
        raise ValueError("Envelope HMAC 密钥至少需要 256 位")
    return key


def parse_component_peer(value):
    if "=" not in value:
        raise argparse.ArgumentTypeError("组件身份格式应为 role=uid[:gid]")
    role, spec = value.split("=", 1)
    if role not in ("adapter", "arbiter", "workspace", "map"):
        raise argparse.ArgumentTypeError("组件角色必须是 adapter、arbiter、workspace 或 map")
    parts = spec.split(":", 1)
    try:
        uid = int(parts[0])
        gid = int(parts[1]) if len(parts) == 2 else None
    except ValueError as exc:
        raise argparse.ArgumentTypeError("组件 UID/GID 必须为整数") from exc
    if uid < 0 or gid is not None and gid < 0:
        raise argparse.ArgumentTypeError("组件 UID/GID 不能为负数")
    return role, (uid, gid)


def main():
    ap = argparse.ArgumentParser(description="Vehicle Gateway（第 5b 步：支持 MQTT 上行）")
    ap.add_argument("--uds", default="/tmp/ra-gw.sock")
    ap.add_argument("--control", type=int, default=9100)
    ap.add_argument("--control-bind", default="127.0.0.1",
                    help="仅车端本机受信 relay 可访问；禁止暴露为公网入口")
    ap.add_argument("--heartbeat-hz", type=float, default=0.5)
    ap.add_argument("--group", default=os.environ.get("RA_GROUP", ""),
                    help="本网关所辖车辆的分组（分组 = 独立项目平台）")
    ap.add_argument("--mqtt-host", default="localhost")
    ap.add_argument("--mqtt-port", type=int, default=8883)
    ap.add_argument("--ca", required=True, help="受控安装流程下发的 Broker CA")
    ap.add_argument("--cert", required=True, help="本 Gateway mTLS 客户端证书")
    ap.add_argument("--key", required=True, help="本 Gateway mTLS 私钥")
    ap.add_argument("--envelope-auth-key", required=True,
                    help="Envelope HMAC-SHA256 密钥文件（0600、base64；必须与 Fleet/Adapter 一致）")
    ap.add_argument("--vehicle-id", required=True)
    ap.add_argument("--gateway-id", required=True)
    ap.add_argument("--component-peer", action="append", required=True,
                    type=parse_component_peer,
                    help="必须提供 adapter/arbiter/workspace=UID[:GID]；真实 Map Agent 可额外提供 map=UID[:GID]")
    ap.add_argument("--chassis", default="ackermann",
                    choices=["ackermann", "4w4s", "diff_agv"],
                    help="底盘类型：阿克曼 / 四轮四转 / 差速AGV")
    args = ap.parse_args()

    try:
        args.envelope_auth_key = load_envelope_auth_key(args.envelope_auth_key)
    except (OSError, ValueError) as exc:
        raise SystemExit(f"Envelope HMAC 密钥不可用：{exc}")

    try:
        args.component_identities = dict(args.component_peer)
        if len(args.component_identities) != len(args.component_peer):
            raise ValueError("组件角色不能重复")
    except (TypeError, ValueError) as exc:
        raise SystemExit(f"组件 peer 身份配置重复或无效：{exc}")
    gw = Gateway(args)
    caps_payload = capabilities_pb2.GatewayCapabilities(
        vehicle_id=args.vehicle_id, gateway_version="0.1.0",
        stack=capabilities_pb2.AUTONOMY_STACK_ROS1,
        stack_version="ROS 1 Noetic",
        supported_control_modes=[control_pb2.CONTROL_MODE_TARGET_MOTION],
        topic_mapping_version="ros1-map-v0.1", certificate_installed=True,
        adapter_version="0.1.0", safety_arbiter_version="0.1.0",
        chassis_type=args.chassis, group_id=args.group,
    )
    try:
        capability_decision = compatibility.assess_gateway_capabilities(caps_payload)
    except ValueError as exc:
        raise SystemExit(f"Gateway 能力声明不符合兼容矩阵：{exc}") from exc
    if not capability_decision.monitoring_allowed:
        raise SystemExit(f"Gateway 能力不兼容，拒绝启动：{capability_decision.control_reason}")
    if not capability_decision.control_allowed:
        print(f"[gateway] 兼容矩阵准入：监控可用，控制禁止（{capability_decision.control_reason}）")
    caps = make_envelope(args.vehicle_id, args.gateway_id, "platform.v1.GatewayCapabilities",
                         caps_payload.SerializeToString(deterministic=True),
                         gw.session_id, sequence=1, ttl_ms=30000,
                         auth_key=args.envelope_auth_key)
    gw.uplink.register(caps)
    print(f"[gateway] mTLS 注册已发送 -> mqtts://{args.mqtt_host}:{args.mqtt_port}")
    gw.start_uds()
    threading.Thread(target=gw.heartbeat_loop, daemon=True).start()
    print(f"[gateway] 控制入口 {args.control_bind}:{args.control}，上行通道=mqtt")
    gw.control_loop()


if __name__ == "__main__":
    main()
