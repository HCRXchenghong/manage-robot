#!/usr/bin/env python3
# 接管生命周期演示（第 8 步）：控制权服务 + 租约 + fencing 全流程
#
# 语义约定（重要）：
#   - fencing token 由权限服务按"接管纪元"发放：一次接管一个值，客户端
#     发命令时原样携带，不自己改。新接管的 fencing 一定更大，旧客户端
#     重放立刻被"fencing 太小"挡下。
#   - command_sequence 才是每条命令的序号（去重/排序用），逐条递增。
#
# 四个阶段（两个"驾驶员"抢同一台车的控制权）：
#   1. 驾驶员 A 申请接管 -> 拿到租约 + fencing，发命令全部 ACCEPTED
#   2. 驾驶员 B 申请接管 -> fencing 更大，【顶替】A；车端租约换成 B 的
#   3. A 再用旧租约发命令 -> 全部被拒（租约不对 / fencing 太小）
#   4. B 行驶撑住链路 -> 按权限服务给的到期时间等租约过期 -> B 也被拒
#      -> 没有新有效命令 -> 车端看门狗 -> 自主减速 -> 安全停稳
#
# 用法：先起 gateway / vehicle_side / authority_service，再跑本脚本：
#   python3 client/simulator/takeover_demo.py \
#       [--authority 127.0.0.1:9300] [--target 127.0.0.1:9100]

import argparse
import json
import socket
import time

import common as C

RESULT_CN = {
    "CONTROL_RESULT_ACCEPTED": "✓",
    "CONTROL_RESULT_REJECTED_STALE": "✗重复/过期",
    "CONTROL_RESULT_REJECTED_FENCING": "✗fencing太小",
    "CONTROL_RESULT_REJECTED_LEASE": "✗租约问题",
    "CONTROL_RESULT_REJECTED_LIMIT": "✗限幅",
}


def authority_call(sock, addr, op, driver):
    sock.sendto(json.dumps({"op": op, "driver": driver}).encode(), addr)
    try:
        data, _ = sock.recvfrom(65535)
        return json.loads(data.decode())
    except (TimeoutError, socket.timeout):
        return {"ok": False, "error": "authority 超时"}


def make_cmd(seq, fencing, lease, speed, ttl_ms=1500):
    payload = {
        "control_session_id": f"takeover-{lease}",
        "lease_id": lease,
        "fencing_token": fencing,
        "command_sequence": seq,
        "issued_monotonic_ns": C.mono_ns(),
        "ttl_ms": ttl_ms,
        "mode": "CONTROL_MODE_TARGET_MOTION",
        "command": {"motion": {"target_speed_mps": speed}},
    }
    return C.make_envelope("platform.v1.ControlCommand", payload,
                           "takeover", sequence=seq, ttl_ms=ttl_ms)


def send_cmd(sock, addr, seq, fencing, lease, speed):
    C.send_json(sock, addr, make_cmd(seq, fencing, lease, speed))
    try:
        data, _ = sock.recvfrom(65535)
        result = json.loads(data.decode()).get("payload", {}).get("result", "?")
        print(f"    seq={seq} fencing={fencing} -> {RESULT_CN.get(result, result)}")
        return result
    except (TimeoutError, socket.timeout):
        print(f"    seq={seq} -> 无回执")
        return None


def stream(sock, addr, grant, seq, duration, speed=2.0, step=0.3):
    """用租约里权限服务发的同一个 fencing，连续发命令（只增序号）。"""
    t_end = time.monotonic() + duration
    while time.monotonic() < t_end:
        send_cmd(sock, addr, seq, grant["fencing_token"], grant["lease_id"], speed)
        seq += 1
        time.sleep(step)
    return seq


def seconds_until(grant, offset=0.0):
    return (grant["valid_until_unix_ns"] - time.time_ns()) / 1e9 + offset


def main():
    ap = argparse.ArgumentParser(description="接管生命周期演示")
    ap.add_argument("--authority", default="127.0.0.1:9300")
    ap.add_argument("--target", default="127.0.0.1:9100")
    args = ap.parse_args()

    def addr(s):
        h, p = s.split(":")
        return (h, int(p))

    auth_addr, gw_addr = addr(args.authority), addr(args.target)
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(1.5)

    print("=== 阶段 1：驾驶员 A 申请接管并行驶 ===")
    gA = authority_call(sock, auth_addr, "request", "driver-A")
    print(f"  A 拿到租约 {gA.get('lease_id')} fencing={gA.get('fencing_token')}")
    seq = stream(sock, gw_addr, gA, seq=1, duration=1.0)

    print("=== 阶段 2：驾驶员 B 申请接管（顶替 A，fencing 更大）===")
    gB = authority_call(sock, auth_addr, "request", "driver-B")
    print(f"  B 拿到租约 {gB.get('lease_id')} fencing={gB.get('fencing_token')}")

    print("=== 阶段 3：A 用旧租约继续发 -> 应全部被拒 ===")
    for _ in range(2):
        send_cmd(sock, gw_addr, seq, gA["fencing_token"], gA["lease_id"], 2.0)
        seq += 1
        time.sleep(0.15)

    print("=== 阶段 4：B 行驶撑住链路，等租约到期后也应被拒 ===")
    drive_for = max(0.3, seconds_until(gB) - 1.2)   # 到期前 1.2s 停止 accepted 发送
    seq = stream(sock, gw_addr, gB, seq=1, duration=drive_for, speed=1.5)
    wait = seconds_until(gB, offset=0.3)            # 等到期 + 0.3s
    if wait > 0:
        print(f"  （等租约到期 {wait:.1f}s…）")
        time.sleep(wait)
    print("  B 租约已过期，继续发 -> 应被拒（租约过期）")
    for _ in range(3):
        send_cmd(sock, gw_addr, seq, gB["fencing_token"], gB["lease_id"], 1.5)
        seq += 1
        time.sleep(0.3)

    print("演示结束。请对照车端日志，应依次出现：")
    print("  1) 阶段1 A 的命令 ✓ 执行 + 收到租约")
    print("  2) 阶段2 收到 B 的新租约（顶替）")
    print("  3) 阶段3 A 的命令 ✗ 拒绝（租约无效 / fencing 太小）")
    print("  4) 阶段4 B 先 ✓ 执行，到期后 ✗ 拒绝，最后 ⚠ 断链 -> ✓ 安全停稳")


if __name__ == "__main__":
    main()

