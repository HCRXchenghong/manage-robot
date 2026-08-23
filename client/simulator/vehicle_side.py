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
                if rec.get("kind") != "control":
                    continue
                self.handle_control(rec.get("env", {}))

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

    # ---------- 上行：假总线 -> 翻译 -> 遥测 ----------

    def telemetry_loop(self):
        interval = 1.0 / max(self.hz, 0.01)
        while True:
            time.sleep(interval)
            ts = C.mono_ns()
            # 假 can 帧：真实字段结构来自车端 can_msgs
            vs = {"cur_speed": self.arb.speed / adapter.KMH_TO_MPS,
                  "cur_steer": 0.0, "shift_level": 1, "is_autodrive": True}
            bat = {"capacity": 85.0, "voltage": 52.3}
            signals = adapter.translate_vehicle_status(vs, ts) + \
                adapter.translate_battery(bat, ts)
            self.seq += 1
            env = C.make_envelope("platform.v1.SignalUpdate",
                                  {"signals": signals}, "sim-session",
                                  sequence=self.seq, ttl_ms=5000)
            self.send_env(env)


def main():
    ap = argparse.ArgumentParser(description="链上车端组件")
    ap.add_argument("--uds", default="/tmp/ra-gw.sock")
    ap.add_argument("--hz", type=float, default=2.0)
    args = ap.parse_args()

    vs = VehicleSide(args.uds, args.hz)
    threading.Thread(target=vs.reader_loop, daemon=True).start()
    print(f"[车端] 已接入 Gateway：{args.uds}，遥测 {args.hz}Hz")
    vs.telemetry_loop()


if __name__ == "__main__":
    main()
