#!/usr/bin/env python3
# 断链最小风险演示客户端（第 6 步）——车云链路的"死人开关"
#
# 三个阶段：
#   A. 正常遥控：每 300ms 发一条运动命令（目标 2 m/s），约 3 秒
#   B. 命令流中断：停发 4 秒（模拟两条链路全部断掉）
#      -> 车端看门狗约 800ms 判定断链 -> 自主减速 -> 安全停稳
#   C. 重新接管：驾驶员"重连"，发序号更大的新命令 -> 校验通过，恢复行驶
#
# 用法：先起 Gateway 和车端组件，再运行本脚本：
#   python3 vehicle/gateway/gateway.py
#   python3 client/simulator/vehicle_side.py
#   python3 client/simulator/minimum_risk_demo.py [--target 127.0.0.1:9100]

import argparse
import json
import socket
import time

import common as C

RESULT_CN = {
    "CONTROL_RESULT_ACCEPTED": "✓",
    "CONTROL_RESULT_REJECTED_STALE": "✗重复/过期",
    "CONTROL_RESULT_REJECTED_FENCING": "✗fencing",
    "CONTROL_RESULT_REJECTED_LEASE": "✗租约",
    "CONTROL_RESULT_REJECTED_LIMIT": "✗限幅",
}


def make_cmd(seq, fencing, speed, ttl_ms=1500):
    payload = {
        "control_session_id": "minrisk-session",
        "lease_id": "valid-lease",
        "fencing_token": fencing,
        "command_sequence": seq,
        "issued_monotonic_ns": C.mono_ns(),
        "ttl_ms": ttl_ms,
        "mode": "CONTROL_MODE_TARGET_MOTION",
        "command": {"motion": {"target_speed_mps": speed}},
    }
    return C.make_envelope("platform.v1.ControlCommand", payload,
                           "minrisk-session", sequence=seq, ttl_ms=ttl_ms)


def send_one(sock, addr, seq, fencing, speed):
    env = make_cmd(seq, fencing, speed)
    C.send_json(sock, addr, env)
    try:
        data, _ = sock.recvfrom(65535)
        ack = json.loads(data.decode("utf-8")).get("payload", {})
        result = ack.get("result", "?")
        print(f"  发 seq={seq} 目标 {speed} m/s -> {RESULT_CN.get(result, result)}"
              f"（{ack.get('detail')}）")
        return result
    except (TimeoutError, socket.timeout):
        print(f"  发 seq={seq} -> 无回执（超时）")
        return None


def main():
    ap = argparse.ArgumentParser(description="断链最小风险演示")
    ap.add_argument("--target", default="127.0.0.1:9100")
    args = ap.parse_args()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(1.0)
    host, port = args.target.split(":")
    target = (host, int(port))

    seq, fencing = 100, 500
    print("=== 阶段 A：正常遥控（每 300ms 一条命令，目标 2 m/s）===")
    t_end = time.monotonic() + 3.0
    while time.monotonic() < t_end:
        send_one(sock, target, seq, fencing, 2.0)
        seq += 1
        fencing += 1
        time.sleep(0.3)

    print("=== 阶段 B：命令流中断 4 秒（模拟两条链路全断）===")
    print("  （接下来不再发任何命令，看车端会不会一直跑下去）")
    time.sleep(4.0)

    print("=== 阶段 C：驾驶员重连，重新接管（目标 1.5 m/s）===")
    t_end = time.monotonic() + 2.5
    while time.monotonic() < t_end:
        send_one(sock, target, seq, fencing, 1.5)
        seq += 1
        fencing += 1
        time.sleep(0.3)

    print("演示结束。请对照车端日志，应依次出现：")
    print("  1) 阶段 B 约 0.8s 后：⚠ 判定链路中断 -> 最小风险：自主减速停车")
    print("  2) 约 2.5s 后：✓ 车辆已安全停稳")
    print("  3) 阶段 C 首条命令：✓ 执行（ok，链路恢复，重新接管）")


if __name__ == "__main__":
    main()
