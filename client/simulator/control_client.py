#!/usr/bin/env python3
# 远程驾驶控制客户端（模拟器）
#
# 向假车发送 ControlCommand，打印仲裁器裁决结果。
# 内置若干“故障场景”，用于验证车端安全关卡是否真的拦住非法命令。
#
# 用法：
#   python3 control_client.py --scenario normal      # 正常目标速度命令
#   python3 control_client.py --scenario stale       # 过期命令（ttl=0）
#   python3 control_client.py --scenario duplicate   # 重复序号命令
#   python3 control_client.py --scenario lease       # 无效租约
#   python3 control_client.py --scenario fencing     # fencing token 倒退
#   python3 control_client.py --scenario over_limit  # 目标速度超出 5 m/s
#   python3 control_client.py --scenario all         # 依次跑完全部场景（演示）

import argparse
import socket

import common as C

RESULT_CN = {
    "CONTROL_RESULT_ACCEPTED": "✓ 已接受并执行",
    "CONTROL_RESULT_REJECTED_STALE": "✗ 拒绝：过期/序号重复",
    "CONTROL_RESULT_REJECTED_FENCING": "✗ 拒绝：fencing token 无效",
    "CONTROL_RESULT_REJECTED_LEASE": "✗ 拒绝：租约无效",
    "CONTROL_RESULT_REJECTED_LIMIT": "✗ 拒绝：超出安全限幅",
    "CONTROL_RESULT_FAILED": "✗ 执行失败",
}


def build_command(seq, fencing, lease="valid-lease", ttl_ms=1000, speed=2.0):
    """构造一条目标运动控制命令（对应 control.proto）。"""
    payload = {
        "control_session_id": "sim-session",
        "lease_id": lease,
        "fencing_token": fencing,
        "command_sequence": seq,
        "issued_monotonic_ns": C.mono_ns(),
        "ttl_ms": ttl_ms,
        "mode": "CONTROL_MODE_TARGET_MOTION",
        "command": {"motion": {"target_speed_mps": speed}},
    }
    return C.make_envelope("platform.v1.ControlCommand", payload,
                           "sim-session", sequence=seq, ttl_ms=ttl_ms)


def send_and_wait(sock, env, addr, timeout=1.0):
    C.send_json(sock, addr, env)
    resp, _ = C.recv_json(sock, timeout)
    if resp is None:
        print("  （超时，未收到回执）")
        return
    p = resp["payload"]
    print(f"  seq={p['command_sequence']} -> "
          f"{RESULT_CN.get(p['result'], p['result'])}（{p['detail']}）")


# 演示序列：(说明, 命令构造参数)。顺序刻意安排成能触发每种拒绝。
DEMO = [
    ("1 正常接管：目标速度 2.0 m/s",        dict(seq=1, fencing=100)),
    ("2 重复序号：再发一次 seq=1",          dict(seq=1, fencing=100)),
    ("3 过期命令：ttl=0",                  dict(seq=2, fencing=101, ttl_ms=0)),
    ("4 无效租约",                          dict(seq=3, fencing=102, lease="bad-lease")),
    ("5 fencing 倒退（50 < 已见 101）",      dict(seq=4, fencing=50)),
    ("6 超限：目标速度 20 m/s（>5）",        dict(seq=5, fencing=103, speed=20.0)),
    ("7 恢复正常：目标速度 3.0 m/s",         dict(seq=6, fencing=104, speed=3.0)),
]


def main():
    ap = argparse.ArgumentParser(description="远驾控制客户端（模拟器）")
    ap.add_argument("--target", default="127.0.0.1:9100", help="假车或 netem 地址")
    ap.add_argument("--scenario", default="all",
                    choices=["normal", "stale", "duplicate", "lease",
                             "fencing", "over_limit", "all"])
    args = ap.parse_args()

    host, port = args.target.split(":")
    addr = (host, int(port))
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)

    if args.scenario == "all":
        print(f"=== 演示：对 {args.target} 依次发送 7 条命令 ===")
        for desc, kw in DEMO:
            print(desc)
            send_and_wait(sock, build_command(**kw), addr)
        return

    single = {
        "normal": dict(seq=1, fencing=100),
        "stale": dict(seq=2, fencing=101, ttl_ms=0),
        "duplicate": dict(seq=1, fencing=100),
        "lease": dict(seq=3, fencing=102, lease="bad-lease"),
        "fencing": dict(seq=4, fencing=50),
        "over_limit": dict(seq=5, fencing=103, speed=20.0),
    }[args.scenario]
    send_and_wait(sock, build_command(**single), addr)


if __name__ == "__main__":
    main()
