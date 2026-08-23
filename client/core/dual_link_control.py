#!/usr/bin/env python3
# 双链路远驾控制客户端（第 5c 步）
#
# 架构文档 §7.5：接管期间控制命令经两条独立 QUIC 链路双发，
# 车端按租约、fencing token、序号和 TTL 校验并去重。
# 本客户端把同一条 ControlCommand 同时发给 edge-A / edge-B，
# 并打印每条链路的回执——预期一条 ACCEPTED、另一条被判“序号重复”，
# 证明：命令到达两次，但只执行一次。
#
# 用法：
#   .venv/bin/python client/core/dual_link_control.py --demo
#   .venv/bin/python client/core/dual_link_control.py --links a --seq 20 --fencing 300

import argparse
import asyncio
import json
import os
import sys
import time
import uuid

from aioquic.asyncio import QuicConnectionProtocol, connect
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.events import DatagramFrameReceived

ALPN = "ra-control"
REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
sys.path.insert(0, os.path.join(REPO, "client", "simulator"))
import common as C  # noqa: E402

EDGES = {
    # 用 127.0.0.1 而非 localhost：服务器只绑 IPv4，避免解析到 ::1 导致连不上
    "a": ("127.0.0.1", 9443),
    "b": ("127.0.0.1", 9444),
}

RESULT_CN = {
    "CONTROL_RESULT_ACCEPTED": "✓ 已执行",
    "CONTROL_RESULT_REJECTED_STALE": "✗ 重复/过期（已去重）",
    "CONTROL_RESULT_REJECTED_FENCING": "✗ fencing 无效",
    "CONTROL_RESULT_REJECTED_LEASE": "✗ 租约无效",
    "CONTROL_RESULT_REJECTED_LIMIT": "✗ 超出限幅",
}


class ClientProto(QuicConnectionProtocol):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.acks = asyncio.Queue()

    def quic_event_received(self, event):
        if isinstance(event, DatagramFrameReceived):
            self.acks.put_nowait(event.data)


async def run_link(name, addr, payload_bytes, ca, timeout):
    """经一条 QUIC 链路发送命令并等回执。"""
    async def _attempt():
        cfg = QuicConfiguration(is_client=True, alpn_protocols=[ALPN],
                                max_datagram_frame_size=65535)
        cfg.load_verify_locations(ca)
        async with connect(addr[0], addr[1], configuration=cfg,
                           create_protocol=ClientProto) as client:
            # 注意：send_datagram_frame 在底层 QuicConnection（_quic）上；
            # 直接操作 _quic 入队后，必须手动 transmit() 才会真正发到网络
            client._quic.send_datagram_frame(payload_bytes)
            client.transmit()
            data = await client.acks.get()
            return json.loads(data.decode("utf-8"))

    label = f"edge-{name} {addr[0]}:{addr[1]}"
    try:
        ack = await asyncio.wait_for(_attempt(), timeout=timeout)
        p = ack.get("payload", {})
        result = p.get("result", "?")
        print(f"  [{label}] {RESULT_CN.get(result, result)}（{p.get('detail')}）")
        return result
    except asyncio.TimeoutError:
        print(f"  [{label}] 超时（链路故障或未响应）")
        return None
    except Exception as e:
        print(f"  [{label}] 链路不可用：{type(e).__name__}")
        return None


def build_command(seq, fencing, speed=2.0, ttl_ms=1500):
    payload = {
        "control_session_id": "dual-session",
        "lease_id": "valid-lease",
        "fencing_token": fencing,
        "command_sequence": seq,
        "issued_monotonic_ns": C.mono_ns(),
        "ttl_ms": ttl_ms,
        "mode": "CONTROL_MODE_TARGET_MOTION",
        "command": {"motion": {"target_speed_mps": speed}},
    }
    return C.make_envelope("platform.v1.ControlCommand", payload,
                           "dual-session", sequence=seq, ttl_ms=ttl_ms)


async def send_dual(seq, fencing, links, ca, speed=2.0):
    env = build_command(seq, fencing, speed)
    payload_bytes = json.dumps(env).encode("utf-8")
    print(f"命令 seq={seq} fencing={fencing} 目标 {speed} m/s —— 同时走 "
          f"{len(links)} 条链路双发")
    tasks = [run_link(n, EDGES[n], payload_bytes, ca, timeout=3.0)
             for n in links]
    return await asyncio.gather(*tasks)


async def demo(args):
    ca = os.path.join(REPO, "deploy", "pki", "dev", "ca.crt")
    print("=== 场景 1：双链路双发（同一命令发两次，只应执行一次）===")
    await send_dual(seq=10, fencing=200, links=["a", "b"], ca=ca)
    print("=== 场景 2：edge-B 宕机（只连 edge-A，命令仍应送达执行）===")
    await send_dual(seq=11, fencing=201, links=["a", "b"], ca=ca)
    print("=== 场景 3：恢复后再次双发 ===")
    await send_dual(seq=12, fencing=202, links=["a", "b"], ca=ca)


def main():
    ap = argparse.ArgumentParser(description="双链路远驾控制客户端")
    ap.add_argument("--demo", action="store_true", help="跑三个标准场景")
    ap.add_argument("--kill-b", action="store_true",
                    help="场景 2 前请手动停掉 edge-B（或让它没启动）")
    ap.add_argument("--links", default="a,b")
    ap.add_argument("--seq", type=int, default=10)
    ap.add_argument("--fencing", type=int, default=200)
    ap.add_argument("--speed", type=float, default=2.0)
    args = ap.parse_args()

    ca = os.path.join(REPO, "deploy", "pki", "dev", "ca.crt")
    if args.demo:
        asyncio.run(demo(args))
    else:
        links = [x.strip() for x in args.links.split(",") if x.strip()]
        asyncio.run(send_dual(args.seq, args.fencing, links, ca, args.speed))


if __name__ == "__main__":
    main()
