#!/usr/bin/env python3
# Vehicle Gateway 骨架（阶段 0 本地版）
#
# 定位（架构文档 §5.1）：车云通信的唯一入口、无图形化常驻后台。
# 本版本先实现“本地通道”：
#   - 车端组件（adapter/仲裁器）经 Unix Domain Socket 接入（JSON 镜像消息）
#   - 接收控制端/云端控制命令（UDP），做第一道过期预筛，再路由给车端组件
#   - 遥测/心跳/回执统一转发上行到云端端点（UDP）
# 第 5b 步会把上行通道换成 mTLS + MQTT / QUIC；路由结构保持不变。
#
# 防御纵深：网关只做“明显过期”预筛；租约/fencing/限幅等完整裁决
# 仍在车端安全仲裁器（vehicle_sim.Arbiter）完成，云端不可绕过。
#
# 用法：
#   python3 gateway.py [--uds /tmp/ra-gw.sock] [--control 9100]
#                      [--cloud 127.0.0.1:9200] [--heartbeat-hz 0.5]

import argparse
import json
import os
import socket
import threading
import time
import uuid

SCHEMA_MAJOR = 1
SCHEMA_MINOR = 0
MAX_MSG_BYTES = 1024 * 1024  # 每类消息最大长度：解析前检查，防内存攻击


def mono_ns():
    return time.monotonic_ns()


def make_envelope(message_type, payload, session_id, sequence, ttl_ms=5000):
    return {
        "schema_major": SCHEMA_MAJOR, "schema_minor": SCHEMA_MINOR,
        "message_type": message_type,
        "vehicle_id": "sim-veh-001", "gateway_id": "gw-local-001",
        "session_id": session_id, "sequence": sequence,
        "utc_time_ns": time.time_ns(), "monotonic_time_ns": mono_ns(),
        "ttl_ms": ttl_ms, "trace_id": uuid.uuid4().hex[:16],
        "payload": payload,
    }


class Gateway:
    def __init__(self, args):
        self.uds_path = args.uds
        chost, cport = args.cloud.split(":")
        self.cloud_addr = (chost, int(cport))
        self.heartbeat_hz = args.heartbeat_hz

        self.udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.udp.bind(("0.0.0.0", args.control))
        self.udp.settimeout(0.05)
        self.control_port = args.control

        self.components = []       # 已接入的车端组件（UDS 连接）
        self.returns = {}          # (session, seq) -> [控制端地址...]（双链路：每条来路都要回执）
        self.lock = threading.Lock()
        self.stats = {"uplink": 0, "down": 0, "ack": 0, "prefilter": 0}
        self.uplink = None  # MQTTUplink；为 None 时退回 UDP 演示通道

    # ---------- UDS：车端组件接入 ----------

    def start_uds(self):
        if os.path.exists(self.uds_path):
            os.unlink(self.uds_path)
        srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        srv.bind(self.uds_path)
        srv.listen(8)
        threading.Thread(target=self._accept_loop, args=(srv,), daemon=True).start()
        print(f"[gateway] UDS 就绪：{self.uds_path}")

    def _accept_loop(self, srv):
        while True:
            conn, _ = srv.accept()
            with self.lock:
                self.components.append(conn)
            print(f"[gateway] 车端组件接入（当前 {len(self.components)} 个）")
            threading.Thread(target=self._component_reader,
                             args=(conn,), daemon=True).start()

    def _send_uds(self, conn, rec):
        try:
            conn.sendall((json.dumps(rec) + "\n").encode("utf-8"))
            return True
        except OSError:
            return False

    def _component_reader(self, conn):
        buf = b""
        while True:
            try:
                data = conn.recv(65535)
            except OSError:
                break
            if not data:
                break
            buf += data
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                if len(line) > MAX_MSG_BYTES:
                    continue
                try:
                    rec = json.loads(line.decode("utf-8"))
                except (ValueError, UnicodeDecodeError):
                    continue
                env = rec.get("env") or {}
                mtype = env.get("message_type", "")
                if mtype.endswith("ControlAck"):
                    payload = env.get("payload", {})
                    key = (payload.get("control_session_id"),
                           payload.get("command_sequence"))
                    ret = self.returns.pop(key, None)
                    rets = ret if isinstance(ret, list) else ([ret] if ret else [])
                    for ret in rets:
                        # 转发内层信封本身，而不是 UDS 记录外壳
                        self.udp.sendto(json.dumps(env).encode("utf-8"), ret)
                    self.stats["ack"] += 1
                else:
                    # 遥测/心跳等：转发上行到云端
                    self._uplink_env(env)
                    self.stats["uplink"] += 1

    def _uplink_env(self, env):
        """上行一个信封：优先 MQTT（真实通道），否则 UDP 演示通道。"""
        if self.uplink is not None:
            try:
                self.uplink.send(env)
            except Exception:
                pass
            return
        try:
            self.udp.sendto(json.dumps(env).encode("utf-8"), self.cloud_addr)
        except OSError:
            pass

    # ---------- UDP：控制命令下行 ----------

    def control_loop(self):
        while True:
            try:
                data, addr = self.udp.recvfrom(65535)
            except (socket.timeout, TimeoutError, OSError):
                continue
            if len(data) > MAX_MSG_BYTES:
                continue
            try:
                env = json.loads(data.decode("utf-8"))
            except (ValueError, UnicodeDecodeError):
                continue
            mtype = env.get("message_type")
            if mtype == "platform.v1.LeaseGrant":
                # 控制权服务的租约下发：直接转发车端组件（不走过期预筛）
                n = self._forward_to_components({"kind": "lease", "env": env})
                p = env.get("payload", {})
                print(f"[gateway] 租约下发 lease={p.get('lease_id')} "
                      f"fencing={p.get('fencing_token')} -> {n} 个组件")
                continue
            if mtype == "platform.v1.TerminalGrant":
                # 终端令牌下发（第 9 步）：同样是授权先行
                n = self._forward_to_components({"kind": "terminal", "env": env})
                p = env.get("payload", {})
                print(f"[gateway] 终端令牌下发 driver={p.get('driver_id')} "
                      f"-> {n} 个组件")
                continue
            if mtype != "platform.v1.ControlCommand":
                continue

            # 第一道预筛：信封级过期判断（完整裁决仍在车端仲裁器）
            age_ms = (mono_ns() - int(env.get("monotonic_time_ns", 0))) / 1e6
            ttl_ms = int(env.get("ttl_ms", 0))
            if age_ms > ttl_ms:
                self.stats["prefilter"] += 1
                payload = env.get("payload", {})
                ack = make_envelope("platform.v1.ControlAck", {
                    "control_session_id": payload.get("control_session_id", ""),
                    "command_sequence": payload.get("command_sequence", 0),
                    "result": "CONTROL_RESULT_REJECTED_STALE",
                    "detail": f"网关预筛：过期 age={age_ms:.0f}ms > ttl={ttl_ms}ms",
                    "applied_monotonic_ns": 0,
                }, env.get("session_id", ""), sequence=0)
                self.udp.sendto(json.dumps(ack).encode("utf-8"), addr)
                print(f"[gateway] 预筛拒绝过期命令 seq={payload.get('command_sequence')}")
                continue

            payload = env.get("payload", {})
            key = (payload.get("control_session_id"),
                   payload.get("command_sequence"))
            self.returns.setdefault(key, []).append(addr)
            n = self._forward_to_components({"kind": "control", "env": env})
            self.stats["down"] += 1
            print(f"[gateway] 控制命令下发 seq={payload.get('command_sequence')} -> {n} 个组件")

    def _forward_to_components(self, rec):
        with self.lock:
            comps = list(self.components)
        ok = 0
        for c in comps:
            if self._send_uds(c, rec):
                ok += 1
        return ok

    # ---------- 心跳 ----------

    def heartbeat_loop(self):
        seq = 0
        interval = 1.0 / max(self.heartbeat_hz, 0.01)
        while True:
            time.sleep(interval)
            seq += 1
            env = make_envelope("platform.v1.Heartbeat", {}, "gw-session",
                                sequence=seq, ttl_ms=10000)
            self._uplink_env(env)
            if seq % 10 == 1:
                s = self.stats
                print(f"[gateway] 心跳 #{seq} | 上行 {s['uplink']} 下行 {s['down']} "
                      f"回执 {s['ack']} 预筛 {s['prefilter']}")


class MQTTUplink:
    """经 MQTT 5 / mTLS 上行（第 5b 步）。话题规划：
        vehicle/{id}/register    启动时的注册与能力声明
        vehicle/{id}/telemetry   遥测 SignalUpdate
        vehicle/{id}/status      心跳 / Gateway 状态
    身份来自客户端证书（Broker 侧 use_identity_as_username）。
    """

    def __init__(self, host, port, ca, cert, key, vehicle_id):
        import paho.mqtt.client as mqtt  # 延迟导入：UDP 模式不依赖
        self.vehicle_id = vehicle_id
        self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                                  client_id=f"gw-{vehicle_id}",
                                  protocol=mqtt.MQTTv5)
        self.client.tls_set(ca_certs=ca, certfile=cert, keyfile=key)
        self.client.connect(host, port, keepalive=15)
        self.client.loop_start()

    def send(self, env):
        mtype = env.get("message_type", "")
        if mtype.endswith("SignalUpdate"):
            kind = "telemetry"
        elif mtype.endswith("Heartbeat") or mtype.endswith("GatewayStatus"):
            kind = "status"
        else:
            kind = "misc"
        topic = f"vehicle/{self.vehicle_id}/{kind}"
        self.client.publish(topic, json.dumps(env), qos=1)

    def register(self, capabilities_env):
        topic = f"vehicle/{self.vehicle_id}/register"
        # retained=True：注册/能力是车辆“当前状态”，晚订阅的云端服务也能拿到
        self.client.publish(topic, json.dumps(capabilities_env), qos=1, retain=True)


def main():
    ap = argparse.ArgumentParser(description="Vehicle Gateway（第 5b 步：支持 MQTT 上行）")
    ap.add_argument("--uds", default="/tmp/ra-gw.sock")
    ap.add_argument("--control", type=int, default=9100)
    ap.add_argument("--cloud", default="127.0.0.1:9200")
    ap.add_argument("--heartbeat-hz", type=float, default=0.5)
    ap.add_argument("--uplink", choices=["udp", "mqtt"], default="udp",
                    help="udp=本地演示通道；mqtt=mTLS 真实通道（第 5b 步）")
    ap.add_argument("--group", default=os.environ.get("RA_GROUP", "g-2"),
                    help="本网关所辖车辆的分组（分组 = 独立项目平台）")
    ap.add_argument("--mqtt-host", default="localhost")
    ap.add_argument("--mqtt-port", type=int, default=8883)
    ap.add_argument("--ca", default="deploy/pki/dev/ca.crt")
    ap.add_argument("--cert", default="deploy/pki/dev/vehicle.crt")
    ap.add_argument("--key", default="deploy/pki/dev/vehicle.key")
    ap.add_argument("--vehicle-id", default="sim-veh-001")
    ap.add_argument("--chassis", default="ackermann",
                    choices=["ackermann", "4w4s", "diff_agv"],
                    help="底盘类型：阿克曼 / 四轮四转 / 差速AGV")
    args = ap.parse_args()

    gw = Gateway(args)
    if args.uplink == "mqtt":
        gw.uplink = MQTTUplink(args.mqtt_host, args.mqtt_port,
                               args.ca, args.cert, args.key, args.vehicle_id)
        caps = make_envelope("platform.v1.GatewayCapabilities", {
            "vehicle_id": args.vehicle_id,
            "gateway_version": "0.1.0-sim",
            "stack": "AUTONOMY_STACK_ROS1",
            "stack_version": "ROS 1 Noetic",
            "chassis_type": args.chassis,
            "group": args.group,
            "supported_control_modes": ["CONTROL_MODE_TARGET_MOTION"],
            "topic_mapping_version": "ros1-map-v0.1",
            "modems": [{"modem_id": "sim-a", "carrier": "carrier-A"},
                       {"modem_id": "sim-b", "carrier": "carrier-B"}],
            "certificate_installed": True,
        }, "gw-session", sequence=0, ttl_ms=0)
        gw.uplink.register(caps)
        print(f"[gateway] mTLS 注册已发送 -> mqtt://{args.mqtt_host}:{args.mqtt_port}")
    gw.start_uds()
    threading.Thread(target=gw.heartbeat_loop, daemon=True).start()
    print(f"[gateway] 控制入口 :{args.control}，上行通道={args.uplink}")
    gw.control_loop()


if __name__ == "__main__":
    main()
