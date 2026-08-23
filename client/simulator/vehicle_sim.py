#!/usr/bin/env python3
# 假车模拟器（阶段 0）
#
# 角色：
#   1) 周期性向“云端”发送遥测 SignalUpdate（默认 127.0.0.1:9200）；
#   2) 接收 ControlCommand，按车端“安全仲裁器”逻辑校验，返回 ControlAck。
#
# 仲裁规则（对应 control.proto / 架构文档 §9）：
#   - 租约无效          -> REJECTED_LEASE
#   - fencing token 倒退 -> REJECTED_FENCING
#   - TTL 过期          -> REJECTED_STALE（宁可丢弃，不执行旧命令）
#   - 序号重复/倒退      -> REJECTED_STALE
#   - 超出安全限幅       -> REJECTED_LIMIT（低速 ODD：5 m/s）
#   - 全部通过          -> ACCEPTED 并执行
#
# 用法：python3 vehicle_sim.py [--listen 9100] [--cloud 127.0.0.1:9200] [--hz 2]

import argparse
import json
import math
import socket
import time

import common as C


class Arbiter:
    """车端安全仲裁器：控制命令是否执行，它说了算，云端不可绕过。"""

    MAX_SPEED_MPS = 5.0  # 第一阶段低速 ODD 演示限速

    def __init__(self):
        self.last_seq = {}      # 会话 -> 已接受的最大序号
        self.last_fencing = 0   # 见过的最大 fencing token
        self.valid_lease = "valid-lease"
        self.mode = "autonomous"
        self.speed = 1.6        # 模拟车速（m/s）

    def check(self, cmd):
        """返回 (ControlResult, 详情)。顺序：租约 → fencing → TTL → 序号 → 限幅。"""
        if cmd.get("lease_id") != self.valid_lease:
            return "CONTROL_RESULT_REJECTED_LEASE", "租约无效"

        tok = int(cmd.get("fencing_token", 0))
        if tok < self.last_fencing:
            return "CONTROL_RESULT_REJECTED_FENCING", f"fencing {tok} < 已见 {self.last_fencing}"

        issued = int(cmd.get("issued_monotonic_ns", 0))
        ttl_ms = int(cmd.get("ttl_ms", 0))
        age_ms = (C.mono_ns() - issued) / 1e6
        if age_ms > ttl_ms:
            return "CONTROL_RESULT_REJECTED_STALE", f"过期：age={age_ms:.0f}ms > ttl={ttl_ms}ms"

        sid = cmd.get("control_session_id", "")
        seq = int(cmd.get("command_sequence", 0))
        if seq <= self.last_seq.get(sid, -1):
            return "CONTROL_RESULT_REJECTED_STALE", f"序号重复/倒退：{seq}"

        which = cmd.get("command", {})
        if "direct" in which:
            d = which["direct"]
            if not (0.0 <= d.get("throttle_fraction", 0) <= 1.0) or \
               not (0.0 <= d.get("brake_fraction", 0) <= 1.0):
                return "CONTROL_RESULT_REJECTED_LIMIT", "油门/制动超出 0..1"
        if "motion" in which:
            m = which["motion"]
            if abs(m.get("target_speed_mps", 0)) > self.MAX_SPEED_MPS:
                return "CONTROL_RESULT_REJECTED_LIMIT", f"目标速度超出 {self.MAX_SPEED_MPS} m/s"

        self.last_fencing = max(self.last_fencing, tok)
        self.last_seq[sid] = seq
        return "CONTROL_RESULT_ACCEPTED", "ok"


def telemetry_payload(arb):
    """生成一批假遥测（SignalUpdate，对应 telemetry.proto）。"""
    ts = C.mono_ns()
    return {"signals": [
        {"path": "Vehicle.Speed",
         "value": {"number": round(arb.speed + 0.05 * math.sin(ts / 1e9), 3)},
         "sample_monotonic_ns": ts, "quality": "SIGNAL_QUALITY_GOOD"},
        {"path": "Platform.Autonomy.OperationMode",
         "value": {"text": arb.mode},
         "sample_monotonic_ns": ts, "quality": "SIGNAL_QUALITY_GOOD"},
        {"path": "Platform.Network.LinkA.RttP95",
         "value": {"number": 38},
         "sample_monotonic_ns": ts, "quality": "SIGNAL_QUALITY_GOOD"},
    ]}


def main():
    ap = argparse.ArgumentParser(description="假车模拟器")
    ap.add_argument("--listen", type=int, default=9100, help="监听 UDP 端口")
    ap.add_argument("--cloud", default="127.0.0.1:9200", help="遥测发送目标")
    ap.add_argument("--hz", type=float, default=2.0, help="遥测频率")
    args = ap.parse_args()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", args.listen))
    sock.settimeout(0.05)

    chost, cport = args.cloud.split(":")
    cloud_addr = (chost, int(cport))

    arb = Arbiter()
    seq = 0
    interval = 1.0 / args.hz
    last_send = 0.0

    print(f"[假车] 监听 :{args.listen}，遥测 -> {args.cloud} @ {args.hz}Hz")
    while True:
        # 1) 接收并裁决控制命令
        try:
            data, addr = sock.recvfrom(65535)
            env = json.loads(data.decode("utf-8"))
            if env.get("message_type") == "platform.v1.ControlCommand":
                cmd = env["payload"]
                result, detail = arb.check(cmd)
                accepted = result == "CONTROL_RESULT_ACCEPTED"
                ack_payload = {
                    "control_session_id": cmd.get("control_session_id", ""),
                    "command_sequence": cmd.get("command_sequence", 0),
                    "result": result,
                    "detail": detail,
                    "applied_monotonic_ns": C.mono_ns() if accepted else 0,
                }
                ack = C.make_envelope("platform.v1.ControlAck", ack_payload,
                                      env.get("session_id", ""), sequence=0)
                C.send_json(sock, addr, ack)
                tag = "✓ 执行" if accepted else "✗ 拒绝"
                print(f"[仲裁器] {tag} seq={cmd.get('command_sequence')} "
                      f"-> {result}（{detail}）")
                if accepted and "motion" in cmd.get("command", {}):
                    arb.speed = cmd["command"]["motion"].get("target_speed_mps", arb.speed)
        except (TimeoutError, socket.timeout):
            pass
        except OSError:
            pass
        except Exception as e:  # 坏消息不能打挂假车
            print(f"[假车] 消息解析失败：{e}")

        # 2) 周期上报遥测
        now = time.monotonic()
        if now - last_send >= interval:
            last_send = now
            seq += 1
            env = C.make_envelope("platform.v1.SignalUpdate",
                                  telemetry_payload(arb), "sim-session",
                                  sequence=seq, ttl_ms=5000)
            try:
                C.send_json(sock, cloud_addr, env)
            except OSError:
                pass


if __name__ == "__main__":
    main()
