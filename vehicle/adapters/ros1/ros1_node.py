#!/usr/bin/env python3
"""ROS 1 vehicle adapter: real ROS topics <-> Gateway and Arbiter.

The adapter has two deliberately separate local channels:

* Gateway UDS (role ``adapter``): status/battery telemetry only.
* Arbiter UDS (role ``adapter-actuator``): already-authorized ECU output.

The adapter never accepts a platform ControlCommand from Gateway and never
decides whether a command is safe. Safety Arbiter is the only caller allowed
to request an ECU action. Both socket paths and the calibration package are
required at startup; there is no default vehicle path.
"""

import argparse
import base64
import json
import math
import os
import socket
import struct
import sys
import threading
import time
import uuid

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "../../.."))
sys.path.insert(0, os.path.join(ROOT, "protocols", "gen", "python"))
from robot_agent_platform.v1 import auth as proto_auth  # noqa: E402
from robot_agent_platform.v1 import envelope_pb2, local_pb2, navigation_pb2, telemetry_pb2  # noqa: E402

try:
    import actionlib
    import rospy
    from can_msgs.msg import Ecu
    from can_msgs.msg import VehicleStatus, Battery
    from move_base_msgs.msg import MoveBaseAction, MoveBaseGoal
    from actionlib_msgs.msg import GoalStatus
except ImportError as e:
    raise SystemExit("缺少 ROS 1 环境、can_msgs 或 move_base_msgs：请先 source ROS 1 与小车工作空间。 " + str(e))

import adapter


MAX_FRAME_BYTES = 1024 * 1024
LOCAL_PROTOCOL = "platform.v1.local"


def send_frame(conn, frame):
    raw = frame.SerializeToString(deterministic=True)
    if not 0 < len(raw) <= MAX_FRAME_BYTES:
        raise ValueError("本机 Protobuf 帧超过大小上限")
    conn.sendall(struct.pack(">I", len(raw)) + raw)


def recv_frame(conn):
    header = _recv_exact(conn, 4)
    length = struct.unpack(">I", header)[0]
    if length == 0 or length > MAX_FRAME_BYTES:
        raise ValueError("本机 Protobuf 帧长度无效")
    return local_pb2.LocalFrame.FromString(_recv_exact(conn, length))


def _recv_exact(conn, size):
    buf = bytearray()
    while len(buf) < size:
        part = conn.recv(size - len(buf))
        if not part:
            raise ConnectionError("本机 UDS 在完整帧前关闭")
        buf.extend(part)
    return bytes(buf)


def make_envelope(vehicle_id, gateway_id, message_type, payload, sequence, auth_key,
                  session_id):
    env = envelope_pb2.Envelope(
        schema_major=1, schema_minor=0, message_type=message_type,
        vehicle_id=vehicle_id, gateway_id=gateway_id,
        session_id=session_id, sequence=sequence,
        utc_time_ns=time.time_ns(), monotonic_time_ns=time.monotonic_ns(),
        ttl_ms=5000, trace_id=uuid.uuid4().hex, payload=payload,
    )
    proto_auth.sign_envelope(env, auth_key)
    return env


class GatewayTelemetry:
    def __init__(self, path, vehicle_id, gateway_id, envelope_auth_key,
                 on_navigation=None):
        self.path = path
        self.vehicle_id = vehicle_id
        self.gateway_id = gateway_id
        self.envelope_auth_key = envelope_auth_key
        self.session_id = "adapter-" + uuid.uuid4().hex
        self.conn = None
        self.lock = threading.Lock()
        self.sequence = 0
        self.on_navigation = on_navigation
        self.reader_thread = None

    def connect(self):
        conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        conn.settimeout(5)
        conn.connect(self.path)
        send_frame(conn, local_pb2.LocalFrame(
            kind=local_pb2.LocalFrame.KIND_HELLO,
            component="adapter", protocol=LOCAL_PROTOCOL))
        conn.settimeout(None)
        with self.lock:
            self.conn = conn
        self.reader_thread = threading.Thread(target=self._read_loop, args=(conn,), daemon=True)
        self.reader_thread.start()

    def _read_loop(self, conn):
        """Receive Gateway-delivered NavigationCommand on the full-duplex UDS."""
        try:
            while not rospy.is_shutdown():
                frame = recv_frame(conn)
                if (frame.protocol != LOCAL_PROTOCOL or
                        frame.kind != local_pb2.LocalFrame.KIND_ENVELOPE or
                        not frame.HasField("envelope")):
                    raise ValueError("Gateway UDS 下发的帧类型无效")
                env = frame.envelope
                proto_auth.validate_envelope(env, "platform.v1.NavigationCommand",
                                             self.vehicle_id, self.gateway_id)
                if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                    raise ValueError("NavigationCommand Envelope.auth_tag 无效")
                command = navigation_pb2.NavigationCommand.FromString(env.payload)
                if (command.version != 1 or command.vehicle_id != self.vehicle_id or
                        not command.route_id or
                        command.action == navigation_pb2.NAVIGATION_ACTION_UNSPECIFIED):
                    raise ValueError("NavigationCommand 字段无效")
                if self.on_navigation is not None:
                    self.on_navigation(env, command)
        except (ConnectionError, OSError, ValueError, DecodeError) as exc:
            if not rospy.is_shutdown():
                rospy.logerr("Gateway 导航下行连接已关闭：%s", exc)
        finally:
            with self.lock:
                if self.conn is conn:
                    self.conn = None

    def _send(self, env):
        with self.lock:
            conn = self.conn
            if conn is None:
                raise ConnectionError("Gateway UDS 未连接")
            send_frame(conn, local_pb2.LocalFrame(
                kind=local_pb2.LocalFrame.KIND_ENVELOPE,
                component="adapter", protocol=LOCAL_PROTOCOL, envelope=env))

    def emit(self, signals):
        with self.lock:
            conn = self.conn
            self.sequence += 1
            seq = self.sequence
            if conn is None:
                raise ConnectionError("Gateway UDS 未连接")
            update = telemetry_pb2.SignalUpdate()
            for raw in signals:
                value = raw.get("value", {})
                sample_monotonic_ns = int(raw.get("sample_monotonic_ns", 0)) or time.monotonic_ns()
                sample_utc_ns = int(raw.get("sample_utc_ns", 0)) or time.time_ns()
                signal = update.signals.add(
                    path=raw.get("path", ""),
                    sample_monotonic_ns=sample_monotonic_ns,
                    sample_utc_ns=sample_utc_ns,
                )
                quality = raw.get("quality", "SIGNAL_QUALITY_GOOD")
                signal.quality = getattr(telemetry_pb2, quality,
                                         telemetry_pb2.SIGNAL_QUALITY_UNSPECIFIED)
                if "number" in value:
                    signal.value.number = float(value["number"])
                elif "boolean" in value:
                    signal.value.boolean = bool(value["boolean"])
                elif "text" in value:
                    signal.value.text = str(value["text"])
                else:
                    raise ValueError("Signal 缺少受支持的 Protobuf 值类型")
            env = make_envelope(self.vehicle_id, self.gateway_id,
                                "platform.v1.SignalUpdate",
                                update.SerializeToString(deterministic=True), seq,
                                self.envelope_auth_key, self.session_id)
            send_frame(conn, local_pb2.LocalFrame(
                kind=local_pb2.LocalFrame.KIND_ENVELOPE,
                component="adapter", protocol=LOCAL_PROTOCOL, envelope=env))

    def send_navigation_ack(self, command_env, command, result, detail="", current_point=0):
        with self.lock:
            self.sequence += 1
            seq = self.sequence
            ack = navigation_pb2.NavigationAck(
                version=1, action=command.action, route_id=command.route_id,
                vehicle_id=self.vehicle_id, result=result, detail=detail,
                current_point=current_point, applied_at_unix_ns=time.time_ns())
            env = make_envelope(self.vehicle_id, self.gateway_id,
                                "platform.v1.NavigationAck",
                                ack.SerializeToString(deterministic=True), seq,
                                self.envelope_auth_key, self.session_id)
            env.trace_id = command_env.trace_id
            proto_auth.sign_envelope(env, self.envelope_auth_key)
            conn = self.conn
            if conn is None:
                raise ConnectionError("Gateway UDS 未连接")
            send_frame(conn, local_pb2.LocalFrame(
                kind=local_pb2.LocalFrame.KIND_ENVELOPE,
                component="adapter", protocol=LOCAL_PROTOCOL, envelope=env))


class ActuatorServer:
    """仅接收 Safety Arbiter 已裁决的 ECU 动作。"""

    def __init__(self, path, calibration, publish_ecu):
        self.path = path
        self.calibration = calibration
        self.publish_ecu = publish_ecu
        self.server = None

    def start(self):
        parent = os.path.dirname(self.path)
        if parent:
            os.makedirs(parent, mode=0o750, exist_ok=True)
        if os.path.exists(self.path):
            os.unlink(self.path)
        srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        srv.bind(self.path)
        os.chmod(self.path, 0o660)
        srv.listen(4)
        self.server = srv
        threading.Thread(target=self._accept_loop, daemon=True).start()

    def _accept_loop(self):
        while not rospy.is_shutdown():
            try:
                conn, _ = self.server.accept()
            except OSError:
                return
            threading.Thread(target=self._handle, args=(conn,), daemon=True).start()

    def _handle(self, conn):
        conn.settimeout(2)
        buf = b""
        try:
            frame = recv_frame(conn)
            if frame.protocol != LOCAL_PROTOCOL or frame.kind != local_pb2.LocalFrame.KIND_ACTUATOR_REQUEST:
                raise ValueError("执行器只接受 platform.v1 LocalFrame")
            req = frame.actuator_request
            if req.HasField("minimal_risk_reason"):
                ecu = adapter.minimal_risk_ecu(self.calibration)
            elif req.HasField("target_motion"):
                motion = req.target_motion
                ecu = adapter.control_to_ecu(
                    {"command": {"motion": {
                        "target_speed_mps": motion.target_speed_mps,
                        "target_acceleration_mps2": motion.target_acceleration_mps2,
                        "target_curvature_inv_m": motion.target_curvature_inv_m,
                        "target_yaw_rate_radps": motion.target_yaw_rate_radps,
                    }}}, self.calibration)
            else:
                raise ValueError("执行器只接受 target_motion 或 minimal_risk")
            self.publish_ecu(ecu)
            send_frame(conn, local_pb2.LocalFrame(
                kind=local_pb2.LocalFrame.KIND_ACTUATOR_REPLY,
                component="adapter", protocol=LOCAL_PROTOCOL,
                actuator_reply=local_pb2.ActuatorReply(
                    ok=True, applied_monotonic_ns=time.monotonic_ns())))
        except Exception as exc:
            try:
                send_frame(conn, local_pb2.LocalFrame(
                    kind=local_pb2.LocalFrame.KIND_ACTUATOR_REPLY,
                    component="adapter", protocol=LOCAL_PROTOCOL,
                    actuator_reply=local_pb2.ActuatorReply(ok=False, error=str(exc))))
            except (OSError, ValueError):
                pass
        finally:
            conn.close()


class NavigationBridge:
    """Translate platform routes to a real ROS 1 move_base action server."""

    def __init__(self, action_name, frame_id, send_ack):
        if not action_name or not frame_id:
            raise ValueError("导航 action 名称和坐标系不能为空")
        self.client = actionlib.SimpleActionClient(action_name, MoveBaseAction)
        self.frame_id = frame_id
        self.send_ack = send_ack
        self.lock = threading.Lock()
        self.active_route_id = ""
        self.cancel_event = None
        self.recent_results = {}

    def _ack(self, env, command, result, detail="", current_point=0):
        try:
            self.send_ack(env, command, result, detail, current_point)
        except (ConnectionError, OSError, ValueError) as exc:
            rospy.logerr("NavigationAck 上送失败 route=%s：%s", command.route_id, exc)

    def on_command(self, env, command):
        if command.action == navigation_pb2.NAVIGATION_ACTION_CANCEL:
            self._cancel(env, command)
            return
        if command.action != navigation_pb2.NAVIGATION_ACTION_DISPATCH:
            self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_REJECTED,
                      "不支持的导航动作")
            return
        if len(command.points) < 2:
            self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_REJECTED,
                      "导航任务至少需要两个路点")
            return
        for point in command.points:
            if not all(math.isfinite(v) for v in (point.x_m, point.y_m, point.dwell_s)):
                self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_REJECTED,
                          "路点坐标或停留时间不是有限数")
                return
        with self.lock:
            if command.route_id in self.recent_results:
                result, detail, point = self.recent_results[command.route_id]
                duplicate = True
            elif self.active_route_id:
                duplicate = False
                result, detail, point = (navigation_pb2.NAVIGATION_RESULT_REJECTED,
                                         "已有另一条导航任务执行中", 0)
            else:
                self.active_route_id = command.route_id
                self.cancel_event = threading.Event()
                duplicate = False
                result = detail = point = None
        if duplicate or result is not None:
            self._ack(env, command, result, detail, point)
            return
        self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_ACCEPTED,
                  "车端导航栈已接收任务")
        threading.Thread(target=self._run, args=(env, command), daemon=True).start()

    def _cancel(self, env, command):
        with self.lock:
            active = self.active_route_id == command.route_id
            event = self.cancel_event if active else None
        if not active or event is None:
            self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_REJECTED,
                      "没有正在执行的对应导航任务")
            return
        event.set()
        self.client.cancel_all_goals()
        self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_ACCEPTED,
                  "取消请求已由车端导航栈接收")

    def _run(self, env, command):
        current_point = 0
        try:
            if not self.client.wait_for_server(rospy.Duration(3.0)):
                self._record_result(command.route_id,
                                    navigation_pb2.NAVIGATION_RESULT_FAILED,
                                    "move_base action server 不可用", current_point)
                self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_FAILED,
                          "move_base action server 不可用", current_point)
                return
            self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_STARTED,
                      "导航栈开始执行", current_point)
            for index, point in enumerate(command.points):
                with self.lock:
                    event = self.cancel_event
                if event is None or event.is_set():
                    self._record_result(command.route_id,
                                        navigation_pb2.NAVIGATION_RESULT_CANCELLED,
                                        "导航任务已取消", current_point)
                    self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_CANCELLED,
                              "导航任务已取消", current_point)
                    return
                goal = MoveBaseGoal()
                goal.target_pose.header.stamp = rospy.Time.now()
                goal.target_pose.header.frame_id = self.frame_id
                goal.target_pose.pose.position.x = point.x_m
                goal.target_pose.pose.position.y = point.y_m
                goal.target_pose.pose.orientation.w = 1.0
                self.client.send_goal(goal)
                while not self.client.wait_for_result(rospy.Duration(0.25)):
                    if event.is_set():
                        self.client.cancel_all_goals()
                if event.is_set():
                    self._record_result(command.route_id,
                                        navigation_pb2.NAVIGATION_RESULT_CANCELLED,
                                        "导航任务已取消", current_point)
                    self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_CANCELLED,
                              "导航任务已取消", current_point)
                    return
                if self.client.get_state() != GoalStatus.SUCCEEDED:
                    detail = "move_base 未成功到达路点 %d" % (index + 1)
                    self._record_result(command.route_id,
                                        navigation_pb2.NAVIGATION_RESULT_FAILED,
                                        detail, current_point)
                    self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_FAILED,
                              detail, current_point)
                    return
                current_point = index + 1
                if point.dwell_s > 0:
                    deadline = rospy.Time.now() + rospy.Duration(point.dwell_s)
                    while rospy.Time.now() < deadline:
                        if event.is_set():
                            self._record_result(command.route_id,
                                                navigation_pb2.NAVIGATION_RESULT_CANCELLED,
                                                "导航任务已取消", current_point)
                            self._ack(env, command,
                                      navigation_pb2.NAVIGATION_RESULT_CANCELLED,
                                      "导航任务已取消", current_point)
                            return
                        rospy.sleep(min(0.1, max(0.01, (deadline - rospy.Time.now()).to_sec())))
            self._record_result(command.route_id,
                                navigation_pb2.NAVIGATION_RESULT_COMPLETED,
                                "全部路点已完成", current_point)
            self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_COMPLETED,
                      "全部路点已完成", current_point)
        except Exception as exc:
            detail = "导航执行异常：%s" % exc
            self._record_result(command.route_id,
                                navigation_pb2.NAVIGATION_RESULT_FAILED,
                                detail, current_point)
            self._ack(env, command, navigation_pb2.NAVIGATION_RESULT_FAILED,
                      detail, current_point)
        finally:
            with self.lock:
                if self.active_route_id == command.route_id:
                    self.active_route_id = ""
                    self.cancel_event = None

    def _record_result(self, route_id, result, detail, current_point):
        with self.lock:
            self.recent_results[route_id] = (result, detail, current_point)
            while len(self.recent_results) > 128:
                self.recent_results.pop(next(iter(self.recent_results)))


class Ros1AdapterNode:
    def __init__(self, args):
        self.calibration = adapter.load_calibration(args.calibration)
        self.gateway = GatewayTelemetry(args.gateway_uds, args.vehicle_id, args.gateway_id,
                                        args.envelope_auth_key)
        self.navigation = NavigationBridge(args.navigation_action, args.navigation_frame,
                                            self.gateway.send_navigation_ack)
        self.gateway.on_navigation = self.navigation.on_command
        self.ecu_pub = rospy.Publisher(args.ecu_topic, Ecu, queue_size=10)
        self.actuator = ActuatorServer(args.arbiter_uds, self.calibration, self.publish_ecu)

    def publish_ecu(self, values):
        msg = Ecu()
        for field in ("motor", "steer", "brake", "shift"):
            if not hasattr(msg, field):
                raise RuntimeError("can_msgs/Ecu 缺少字段: " + field)
            setattr(msg, field, values[field])
        if hasattr(msg, "header"):
            msg.header.stamp = rospy.Time.now()
        self.ecu_pub.publish(msg)

    def start(self):
        self.gateway.connect()
        self.actuator.start()
        rospy.loginfo("ROS1 Adapter 已连接 Gateway=%s，执行器 socket=%s，标定=%s",
                      self.gateway.path, self.actuator.path, self.calibration.version)
        rospy.Subscriber("/vehicle_status", VehicleStatus, self.on_vehicle_status,
                         queue_size=10)
        rospy.Subscriber("/battery", Battery, self.on_battery, queue_size=10)

    def on_vehicle_status(self, msg):
        values = {"cur_speed": msg.cur_speed, "cur_steer": msg.cur_steer,
                  "shift_level": msg.shift_level, "is_autodrive": msg.is_autodrive}
        self.gateway.emit(adapter.translate_vehicle_status(
            values, adapter.now_ns(), self.calibration))

    def on_battery(self, msg):
        values = {"capacity": msg.capacity, "voltage": msg.voltage}
        self.gateway.emit(adapter.translate_battery(values, adapter.now_ns()))


def main():
    ap = argparse.ArgumentParser(description="ROS 1 vehicle adapter（无默认车辆）")
    ap.add_argument("--vehicle-id", required=True)
    ap.add_argument("--gateway-id", required=True)
    ap.add_argument("--gateway-uds", required=True)
    ap.add_argument("--arbiter-uds", required=True)
    ap.add_argument("--calibration", required=True,
                    help="已审核车型标定 JSON；缺少标定时拒绝启动")
    ap.add_argument("--envelope-auth-key", required=True,
                    help="Envelope HMAC-SHA256 密钥文件（0600、base64；必须与 Gateway/Fleet 一致）")
    ap.add_argument("--navigation-action", required=True,
                    help="真实 ROS1 move_base action 名称，例如 /move_base")
    ap.add_argument("--navigation-frame", required=True,
                    help="导航路点坐标系，例如经过标定的 map；禁止隐式默认坐标系")
    ap.add_argument("--ecu-topic", default="/ecu")
    args = ap.parse_args()
    try:
        st = os.stat(args.envelope_auth_key)
        if st.st_mode & 0o077:
            raise ValueError("Envelope HMAC 密钥文件权限必须为 0600")
        with open(args.envelope_auth_key, "rb") as f:
            args.envelope_auth_key = base64.b64decode(f.read().strip(), validate=True)
        if len(args.envelope_auth_key) < 32:
            raise ValueError("Envelope HMAC 密钥至少需要 256 位")
    except (OSError, ValueError) as exc:
        raise SystemExit(f"Envelope HMAC 密钥不可用：{exc}")
    rospy.init_node("platform_ros1_adapter", anonymous=False)
    node = Ros1AdapterNode(args)
    node.start()
    rospy.spin()


if __name__ == "__main__":
    main()
