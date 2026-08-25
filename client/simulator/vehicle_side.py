#!/usr/bin/env python3
# 链上车端组件（阶段 0）
#
# 通过 Unix Domain Socket 接入 Gateway，把“假总线 + 翻译官 + 仲裁器”串成完整车端：
#   上行：假 can 帧 -> adapter 翻译核心 -> SignalUpdate -> Gateway -> 云
#   下行：Gateway 下发 ControlCommand -> Arbiter 裁决 -> 若接受则翻译成
#         ecu 指令（打印模拟执行）-> ControlAck 回传
#
# 用法：python3 vehicle_side.py [--uds /tmp/ra-gw.sock] [--hz 2]

import argparse
import json
import math
import os
import socket
import sys
import threading
import time

# 复用两个已有模块：翻译核心 + 仲裁器
ADAPTER_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                           "..", "..", "vehicle", "adapters", "ros1")
sys.path.insert(0, ADAPTER_DIR)
import adapter  # noqa: E402
from vehicle_sim import Arbiter  # noqa: E402
import common as C  # noqa: E402


class VehicleSide:
    def __init__(self, uds_path, hz):
        self.hz = hz
        self.arb = Arbiter()
        self.seq = 0
        # GPS 模拟初值（WGS-84，北京城区一点）；总览高德底图纠偏后绘制轨迹
        self.gps_lat, self.gps_lon, self.gps_alt = 39.908, 116.397, 44.0
        self.gps_heading = 0.6
        self.prev_speed = 0.0
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        for _ in range(50):  # Gateway 可能还没起，等待重试
            try:
                self.sock.connect(uds_path)
                return
            except (FileNotFoundError, ConnectionRefusedError):
                time.sleep(0.2)
        raise SystemExit(f"无法连接 Gateway UDS：{uds_path}")

    def send_env(self, env):
        rec = {"kind": "envelope", "env": env}
        try:
            self.sock.sendall((json.dumps(rec) + "\n").encode("utf-8"))
        except OSError:
            pass

    # ---------- 下行：控制命令 ----------

    def reader_loop(self):
        buf = b""
        while True:
            try:
                data = self.sock.recv(65535)
            except OSError:
                break
            if not data:
                break
            buf += data
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                try:
                    rec = json.loads(line.decode("utf-8"))
                except (ValueError, UnicodeDecodeError):
                    continue
                kind = rec.get("kind")
                if kind == "control":
                    self.handle_control(rec.get("env", {}))
                elif kind == "lease":
                    self.handle_lease(rec.get("env", {}))

    def handle_control(self, env):
        cmd = env.get("payload", {})
        result, detail = self.arb.check(cmd)
        accepted = result == "CONTROL_RESULT_ACCEPTED"
        if accepted:
            ecu = adapter.control_to_ecu(cmd)
            if "motion" in cmd.get("command", {}):
                self.arb.speed = cmd["command"]["motion"].get(
                    "target_speed_mps", self.arb.speed)
            print(f"[车端] ✓ 执行 seq={cmd.get('command_sequence')}，"
                  f"翻译出的 ECU 指令 = {ecu}")
        else:
            print(f"[车端] ✗ 拒绝 seq={cmd.get('command_sequence')}："
                  f"{result}（{detail}）")
        ack = C.make_envelope("platform.v1.ControlAck", {
            "control_session_id": cmd.get("control_session_id", ""),
            "command_sequence": cmd.get("command_sequence", 0),
            "result": result,
            "detail": detail,
            "applied_monotonic_ns": C.mono_ns() if accepted else 0,
        }, env.get("session_id", ""), sequence=0)
        self.send_env(ack)

    def handle_lease(self, env):
        """控制权服务的租约下发/回收（第 8 步）。"""
        p = env.get("payload", {})
        self.arb.grant_lease(p.get("lease_id", ""),
                             int(p.get("fencing_token", 0)),
                             int(p.get("valid_until_unix_ns", 0)))

    # ---------- 上行：假总线 -> 翻译 -> 遥测 ----------

    def telemetry_loop(self):
        interval = 1.0 / max(self.hz, 0.01)
        while True:
            time.sleep(interval)
            self.arb.tick()  # 断链看门狗：周期性判断遥控命令是否断流
            ts = C.mono_ns()
            # 假 can 帧：真实字段结构来自车端 can_msgs
            vs = {"cur_speed": self.arb.speed / adapter.KMH_TO_MPS,
                  "cur_steer": 0.0, "shift_level": 1, "is_autodrive": True}
            bat = {"capacity": 85.0, "voltage": 52.3}
            signals = adapter.translate_vehicle_status(vs, ts) + \
                adapter.translate_battery(bat, ts)
            # 底盘轮速信号（远程接管页轮速图用）；差速/四轮四转由车型决定分布
            ws = self.arb.speed
            for w in ("FL", "FR", "RL", "RR"):
                signals.append({"path": "Vehicle.Chassis.WheelSpeeds." + w,
                                "value": {"number": round(ws, 6)},
                                "sample_monotonic_ns": ts,
                                "quality": "SIGNAL_QUALITY_GOOD"})
            # GPS 定位 + 油门/刹车（总览 GPS 轨迹与油门刹车曲线用）
            dt = interval
            if self.arb.speed > 0.05:
                # 航向缓慢摆动形成自然轨迹；按速度推进 WGS-84 经纬度
                self.gps_heading += 0.06 * math.sin(time.time() / 7.0)
                dlat = (self.arb.speed * dt * math.cos(self.gps_heading)) / 111320.0
                dlon = (self.arb.speed * dt * math.sin(self.gps_heading)) / (
                    111320.0 * math.cos(math.radians(self.gps_lat)))
                self.gps_lat += dlat
                self.gps_lon += dlon
            accel = (self.arb.speed - self.prev_speed) / dt
            self.prev_speed = self.arb.speed
            if accel > 0.05:
                thr, brk = min(100.0, 25.0 + accel * 30.0), 0.0
            elif accel < -0.05:
                thr, brk = 0.0, min(100.0, 20.0 + abs(accel) * 30.0)
            else:
                thr, brk = (12.0 if self.arb.speed > 0.1 else 0.0), 0.0
            for path, val in (("Vehicle.GPS.Fix", 1.0),
                              ("Vehicle.GPS.Latitude", round(self.gps_lat, 7)),
                              ("Vehicle.GPS.Longitude", round(self.gps_lon, 7)),
                              ("Vehicle.GPS.Altitude", round(self.gps_alt, 1)),
                              ("Vehicle.Chassis.Throttle.Pct", round(thr, 1)),
                              ("Vehicle.Chassis.Brake.Pct", round(brk, 1)),
                              ("Vehicle.Chassis.Accel.Longitudinal", round(accel, 3)),
                              ("Vehicle.Cabin.Temperature.C",
                               round(26.0 + 2.0 * math.sin(time.time() / 37.0), 1)),
                              ("Vehicle.Cabin.Humidity.Pct",
                               round(45.0 + 6.0 * math.sin(time.time() / 53.0), 1))):
                signals.append({"path": path, "value": {"number": val},
                                "sample_monotonic_ns": ts,
                                "quality": "SIGNAL_QUALITY_GOOD"})
            # 第 10 步：让大屏看到仲裁器真实状态
            # （arb.mode ∈ autonomous/remote_control/minimum_risk/stopped）
            for s in signals:
                if s["path"] == "Platform.Autonomy.OperationMode":
                    s["value"] = {"text": self.arb.mode}
            self.seq += 1
            env = C.make_envelope("platform.v1.SignalUpdate",
                                  {"signals": signals}, "sim-session",
                                  sequence=self.seq, ttl_ms=5000)
            self.send_env(env)

    # ---------- 地图上报：实时推到云端地图中心 ----------

    def map_push_loop(self):
        """车端有地图文件（.pcd/.csv/.png）时，每周期上报到云端地图中心。
        存储双份：车上原文件不动，服务器留版本化副本；
        sha256 去重：内容没变不产生新版本、不占带宽。
        """
        import base64
        import hashlib
        import urllib.request

        path = os.environ.get("RA_MAP_FILE", "/tmp/ra-ndt-map.csv")
        hub = os.environ.get("RA_HUB", "http://127.0.0.1:9800")
        vid = os.environ.get("RA_VEHICLE_ID", "sim-veh-001")
        interval = float(os.environ.get("RA_MAP_PUSH_S", "15"))
        last_sha = None
        while True:
            try:
                if os.path.exists(path):
                    with open(path, "rb") as f:
                        data = f.read()
                    sha = hashlib.sha256(data).hexdigest()
                    if sha != last_sha:
                        body = json.dumps({
                            "vehicle_id": vid,
                            "name": os.path.basename(path),
                            "source": "vehicle_push",
                            "author": vid,
                            "data_base64": base64.b64encode(data).decode(),
                        }).encode()
                        req = urllib.request.Request(
                            hub + "/api/maps/upload", data=body,
                            headers={"Content-Type": "application/json"})
                        with urllib.request.urlopen(req, timeout=15) as resp:
                            resp.read()
                        last_sha = sha
                        print(f"[车端] 地图已上报: {os.path.basename(path)} "
                              f"sha={sha[:12]} {len(data)} 字节 -> {hub}", flush=True)
            except Exception as e:  # 网络抖动不影响遥测主链路
                print(f"[车端] 地图上报失败（稍后重试）: {e}", flush=True)
            time.sleep(interval)


def main():
    ap = argparse.ArgumentParser(description="链上车端组件")
    ap.add_argument("--uds", default="/tmp/ra-gw.sock")
    ap.add_argument("--hz", type=float, default=2.0)
    args = ap.parse_args()

    vs = VehicleSide(args.uds, args.hz)
    threading.Thread(target=vs.reader_loop, daemon=True).start()
    threading.Thread(target=vs.map_push_loop, daemon=True).start()
    print(f"[车端] 已接入 Gateway：{args.uds}，遥测 {args.hz}Hz，地图实时上报已启用")
    vs.telemetry_loop()


if __name__ == "__main__":
    main()
