#!/usr/bin/env python3
# QUIC 控制中继（第 5c 步）——模拟公网边缘接入点
#
# 架构文档 §7.5：远程驾驶控制走两条独立 QUIC 连接，高频控制走
# QUIC DATAGRAM（RFC 9221，不可靠、低延迟），租约/会话走可靠流。
# 生产部署时，edge-A 与 edge-B 位于不同区域/运营商故障域；
# 本机演示用两个端口模拟。
#
# 职责：
#   1. 与远驾客户端建立 mTLS QUIC 连接（复用开发证书）
#   2. 收到控制 DATAGRAM -> 转发到车端 Gateway 控制入口（UDP）
#   3. Gateway 回执 -> 原路 DATAGRAM 送回客户端
#
# 用法：
#   .venv/bin/python server/control-relay/control_relay.py --name edge-A --listen 9443

import argparse
import asyncio
import functools
import json

from aioquic.asyncio import QuicConnectionProtocol, serve
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.events import DatagramFrameReceived

ALPN = "ra-control"


class RelayServer:
    """一个边缘接入点：QUIC 监听 + 转发到车端 Gateway。"""

    def __init__(self, name, listen_port, gateway_addr):
        self.name = name
        self.listen_port = listen_port
        self.gateway_addr = gateway_addr
        self.pending = {}  # (session, seq) -> QUIC 连接协议对象（回执路由）
        self.transport = None  # 转发与收回执共用同一个 UDP 端口：
        # Gateway 按"来包源地址"回执，必须和转发出口是同一个端口，
        # 否则回执会飞进一个没人读的端口（这是本步骤踩过的坑）

    def forward_command(self, data, proto):
        try:
            env = json.loads(data.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            return
        payload = env.get("payload", {})
        key = (payload.get("control_session_id"),
               payload.get("command_sequence"))
        self.pending[key] = proto
        self.transport.sendto(data, self.gateway_addr)
        print(f"[{self.name}] 收到控制命令 seq={payload.get('command_sequence')} -> 转发车端")

    async def route_ack(self, data):
        try:
            env = json.loads(data.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            return
        payload = env.get("payload", {})
        key = (payload.get("control_session_id"),
               payload.get("command_sequence"))
        proto = self.pending.pop(key, None)
        if proto is not None:
            # 直接操作底层 _quic 入队回执后，必须手动 transmit() 才会发出
            proto._quic.send_datagram_frame(data)
            proto.transmit()
            print(f"[{self.name}] 回执送回客户端 seq={payload.get('command_sequence')} "
                  f"result={payload.get('result')}")


class RelayProtocol(QuicConnectionProtocol):
    def __init__(self, *args, relay=None, **kwargs):
        super().__init__(*args, **kwargs)
        self.relay = relay

    def quic_event_received(self, event):
        if isinstance(event, DatagramFrameReceived):
            self.relay.forward_command(event.data, self)


class AckReceiver(asyncio.DatagramProtocol):
    """接收车端 Gateway 的 UDP 回执。"""

    def __init__(self, relay):
        self.relay = relay

    def datagram_received(self, data, addr):
        asyncio.ensure_future(self.relay.route_ack(data))


async def main_async(args):
    host, port = args.gateway.split(":")
    relay = RelayServer(args.name, args.listen, (host, int(port)))

    loop = asyncio.get_running_loop()
    transport, _ = await loop.create_datagram_endpoint(
        lambda: AckReceiver(relay), local_addr=("127.0.0.1", 0))
    relay.transport = transport

    configuration = QuicConfiguration(is_client=False, alpn_protocols=[ALPN],
                                      max_datagram_frame_size=65535)
    configuration.load_cert_chain(args.cert, args.key)

    protocol_factory = functools.partial(RelayProtocol, relay=relay)
    await serve("0.0.0.0", args.listen, configuration=configuration,
                create_protocol=protocol_factory)
    print(f"[{args.name}] QUIC 就绪 :{args.listen}（mTLS，ALPN={ALPN}），"
          f"转发 -> {args.gateway}")
    await asyncio.Event().wait()


def main():
    ap = argparse.ArgumentParser(description="QUIC 控制中继（边缘接入点）")
    ap.add_argument("--name", default="edge-A")
    ap.add_argument("--listen", type=int, default=9443)
    ap.add_argument("--gateway", default="127.0.0.1:9100",
                    help="车端 Gateway 控制入口")
    ap.add_argument("--cert", default="deploy/pki/dev/server.crt")
    ap.add_argument("--key", default="deploy/pki/dev/server.key")
    args = ap.parse_args()
    asyncio.run(main_async(args))


if __name__ == "__main__":
    main()
