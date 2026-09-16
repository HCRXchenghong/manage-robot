#!/usr/bin/env python3
# QUIC 控制中继（边缘接入点）
#
# 架构文档 §7.5：远程驾驶控制走两条独立 QUIC 连接，高频控制走
# QUIC DATAGRAM（RFC 9221，不可靠、低延迟），租约/会话走可靠流。
# edge-A 与 edge-B 必须位于不同区域/运营商故障域；每个实例只绑定
# 一个已登记车辆的 Gateway 控制入口。
#
# 职责：
#   1. 与受信控制代理建立双向 mTLS QUIC 连接
#   2. 收到控制 DATAGRAM -> 转发到车端 Gateway 控制入口（UDP）
#   3. Gateway 回执 -> 原路 DATAGRAM 送回客户端
#
# 用法：
#   .venv/bin/python server/control-relay/control_relay.py --name edge-A --listen 9443

import argparse
import asyncio
import base64
import functools
import os
import sys
import ssl
import time
from pathlib import Path

from aioquic.asyncio import QuicConnectionProtocol, serve
from google.protobuf.message import DecodeError
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.events import DatagramFrameReceived

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "protocols" / "gen" / "python"))
from robot_agent_platform.v1 import auth, control_pb2, envelope_pb2  # noqa: E402

ALPN = "ra-control"


class RelayServer:
    """一个边缘接入点：QUIC 监听 + 转发到车端 Gateway。"""

    def __init__(self, name, listen_port, gateway_addr, vehicle_id, gateway_id, envelope_auth_key):
        self.name = name
        self.listen_port = listen_port
        self.gateway_addr = gateway_addr
        self.pending = {}  # (session, seq) -> (QUIC 连接协议对象, deadline)
        self.transport = None  # 转发与收回执共用同一个 UDP 端口：
        # Gateway 按"来包源地址"回执，必须和转发出口是同一个端口，
        # 否则回执会飞进一个没人读的端口（这是本步骤踩过的坑）

        self.vehicle_id = vehicle_id
        self.gateway_id = gateway_id
        self.envelope_auth_key = envelope_auth_key

    def _valid_control(self, env):
        try:
            auth.validate_envelope(
                env,
                "platform.v1.ControlCommand",
                self.vehicle_id,
                self.gateway_id,
            )
            if not auth.verify_envelope_auth(env, self.envelope_auth_key):
                return None
            command = control_pb2.ControlCommand.FromString(env.payload)
        except (TypeError, ValueError, DecodeError):
            return None
        if len(env.auth_tag) != 32:
            return None
        if (
            not command.control_session_id
            or command.control_session_id != env.session_id
            or command.command_sequence <= 0
            or command.command_sequence != env.sequence
            or not command.lease_id
            or command.fencing_token <= 0
            or command.ttl_ms != env.ttl_ms
            or env.ttl_ms > 1000
            or len(command.end_to_end_mac) != 32
            or command.mode == control_pb2.CONTROL_MODE_UNSPECIFIED
        ):
            return None
        return command

    def forward_command(self, data, proto):
        now = time.monotonic()
        self.pending = {key: route for key, route in self.pending.items() if route[1] > now}
        try:
            env = envelope_pb2.Envelope.FromString(data)
        except DecodeError:
            return
        command = self._valid_control(env)
        if command is None:
            print(f"[{self.name}] 拒绝不符合控制信封的 QUIC DATAGRAM")
            return
        key = (command.control_session_id, command.command_sequence)
        if len(self.pending) >= 4096 and key not in self.pending:
            print(f"[{self.name}] 拒绝控制命令：回执路由容量已满")
            return
        self.pending[key] = (proto, time.monotonic() + env.ttl_ms / 1000 + 1.0)
        self.transport.sendto(data, self.gateway_addr)
        print(f"[{self.name}] 收到控制命令 seq={command.command_sequence} -> 转发车端")

    async def route_ack(self, data):
        try:
            env = envelope_pb2.Envelope.FromString(data)
            ack = control_pb2.ControlAck.FromString(env.payload)
        except DecodeError:
            return
        if (
            env.message_type != "platform.v1.ControlAck"
            or env.vehicle_id != self.vehicle_id
            or env.gateway_id != self.gateway_id
            or len(env.auth_tag) != 32
            or not ack.control_session_id
            or ack.command_sequence <= 0
        ):
            return
        try:
            auth.validate_envelope(env, "platform.v1.ControlAck", self.vehicle_id, self.gateway_id)
            if not auth.verify_envelope_auth(env, self.envelope_auth_key):
                return
        except (TypeError, ValueError):
            return
        key = (ack.control_session_id, ack.command_sequence)
        route = self.pending.pop(key, None)
        if route is not None:
            proto, deadline = route
            if deadline <= time.monotonic():
                return
            # 直接操作底层 _quic 入队回执后，必须手动 transmit() 才会发出
            proto._quic.send_datagram_frame(data)
            proto.transmit()
            try:
                result_name = control_pb2.ControlResult.Name(ack.result)
            except ValueError:
                result_name = f"UNKNOWN({ack.result})"
            print(f"[{self.name}] 回执送回客户端 seq={ack.command_sequence} "
                 f"result={result_name}")


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
    relay = RelayServer(args.name, args.listen, (host, int(port)), args.vehicle_id,
                        args.gateway_id, args.envelope_auth_key)

    loop = asyncio.get_running_loop()
    transport, _ = await loop.create_datagram_endpoint(
        lambda: AckReceiver(relay), local_addr=("127.0.0.1", 0))
    relay.transport = transport

    configuration = QuicConfiguration(is_client=False, alpn_protocols=[ALPN],
                                      max_datagram_frame_size=65535,
                                      cafile=args.client_ca,
                                      verify_mode=ssl.CERT_REQUIRED)
    configuration.load_cert_chain(args.cert, args.key)

    protocol_factory = functools.partial(RelayProtocol, relay=relay)
    await serve(args.bind, args.listen, configuration=configuration,
                create_protocol=protocol_factory)
    print(f"[{args.name}] QUIC 就绪 {args.bind}:{args.listen}（双向 mTLS，ALPN={ALPN}），"
          f"vehicle={args.vehicle_id} gateway={args.gateway_id} 转发 -> {args.gateway}")
    await asyncio.Event().wait()


def main():
    ap = argparse.ArgumentParser(description="QUIC 控制中继（边缘接入点）")
    ap.add_argument("--name", required=True)
    ap.add_argument("--bind", required=True, help="明确的 QUIC 监听地址")
    ap.add_argument("--listen", type=int, required=True)
    ap.add_argument("--gateway", required=True,
                    help="车端 Gateway 控制入口")
    ap.add_argument("--vehicle-id", required=True)
    ap.add_argument("--gateway-id", required=True)
    ap.add_argument("--client-ca", required=True, help="控制代理客户端证书 CA")
    ap.add_argument("--cert", required=True, help="中继服务端证书")
    ap.add_argument("--key", required=True, help="中继服务端私钥")
    ap.add_argument("--envelope-auth-key", required=True,
                    help="Envelope HMAC-SHA256 密钥文件（0600、base64）")
    args = ap.parse_args()
    try:
        info = os.stat(args.envelope_auth_key)
        if info.st_mode & 0o077:
            raise ValueError("Envelope HMAC 密钥文件权限必须为 0600")
        with open(args.envelope_auth_key, "rb") as f:
            args.envelope_auth_key = base64.b64decode(f.read().strip(), validate=True)
        if len(args.envelope_auth_key) < 32:
            raise ValueError("Envelope HMAC 密钥至少需要 256 位")
    except (OSError, ValueError) as exc:
        raise SystemExit(f"Envelope HMAC 密钥不可用：{exc}")
    asyncio.run(main_async(args))


if __name__ == "__main__":
    main()
