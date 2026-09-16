#!/usr/bin/env python3
# 双链路远驾控制客户端
#
# 架构文档 §7.5：接管期间控制命令经两条独立 QUIC 链路双发，
# 车端按租约、fencing token、序号和 TTL 校验并去重。
# 本客户端把同一条 ControlCommand 同时发给 edge-A / edge-B，
# 并打印每条链路的回执——预期一条 ACCEPTED、另一条被判“序号重复”，
# 证明：命令到达两次，但只执行一次。
#
# 用法（链路、身份、租约均必须来自受控接入配置）：
#   .venv/bin/python client/core/dual_link_control.py \
#     --link primary=control-a.example:9443 --link backup=control-b.example:9443 \
#     --ca /etc/robot-agent/ca.crt --cert /etc/robot-agent/client.crt \
#     --key /etc/robot-agent/client.key --vehicle-id VEHICLE_ID \
#     --gateway-id GATEWAY_ID --lease-id LEASE_ID --seq 20 --fencing 300

import argparse
import asyncio
import base64
import os
import sys
import time
import uuid

from aioquic.asyncio import QuicConnectionProtocol, connect
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.events import DatagramFrameReceived

ALPN = "ra-control"
CORE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, CORE)
import protocol as C  # noqa: E402

RESULT_CN = {
    "CONTROL_RESULT_ACCEPTED": "✓ 已执行",
    "CONTROL_RESULT_REJECTED_STALE": "✗ 重复/过期（已去重）",
    "CONTROL_RESULT_REJECTED_FENCING": "✗ fencing 无效",
    "CONTROL_RESULT_REJECTED_LEASE": "✗ 租约无效",
    "CONTROL_RESULT_REJECTED_LIMIT": "✗ 超出限幅",
    "CONTROL_RESULT_REJECTED_AUTH": "✗ 身份/MAC 校验失败",
    "CONTROL_RESULT_FAILED": "✗ 车端执行失败",
}


class ClientProto(QuicConnectionProtocol):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.acks = asyncio.Queue()

    def quic_event_received(self, event):
        if isinstance(event, DatagramFrameReceived):
            self.acks.put_nowait(event.data)


async def run_link(name, addr, payload_bytes, ca, cert, key, vehicle_id, gateway_id, auth_key, timeout):
    """经一条 QUIC 链路发送命令并等回执。"""
    async def _attempt():
        cfg = QuicConfiguration(is_client=True, alpn_protocols=[ALPN],
                                max_datagram_frame_size=65535)
        cfg.load_verify_locations(ca)
        cfg.load_cert_chain(cert, key)
        async with connect(addr[0], addr[1], configuration=cfg,
                           create_protocol=ClientProto) as client:
            # 注意：send_datagram_frame 在底层 QuicConnection（_quic）上；
            # 直接操作 _quic 入队后，必须手动 transmit() 才会真正发到网络
            client._quic.send_datagram_frame(payload_bytes)
            client.transmit()
            data = await client.acks.get()
            return C.parse_envelope(data)

    label = f"edge-{name} {addr[0]}:{addr[1]}"
    try:
        ack = await asyncio.wait_for(_attempt(), timeout=timeout)
        C.validate_envelope(
            ack,
            vehicle_id=vehicle_id,
            gateway_id=gateway_id,
            expected_type="platform.v1.ControlAck",
        )
        if not C.verify_envelope_auth(ack, auth_key):
            raise ValueError("ControlAck auth_tag 校验失败")
        ack_payload = C.control_pb2.ControlAck.FromString(ack.payload)
        if not ack_payload.control_session_id or ack_payload.command_sequence <= 0:
            raise ValueError("ControlAck 缺少安全字段")
        try:
            result = C.control_pb2.ControlResult.Name(ack_payload.result)
        except ValueError:
            result = f"UNKNOWN({ack_payload.result})"
        print(f"  [{label}] {RESULT_CN.get(result, result)}（{ack_payload.detail}）")
        return result
    except asyncio.TimeoutError:
        print(f"  [{label}] 超时（链路故障或未响应）")
        return None
    except Exception as e:
        print(f"  [{label}] 链路不可用：{type(e).__name__}")
        return None


def _load_mac_key(path):
    """读取受信控制代理下发的会话 MAC 密钥。

    此入口用于 HIL/封闭场地集成；浏览器不得持有该文件，生产中由设备
    证明后的原生控制代理经受控会话材料通道获取短期密钥。
    """
    if not path:
        raise ValueError("必须提供 --mac-key；Safety Arbiter 不接受无端到端 MAC 的命令")
    st = os.stat(path)
    if st.st_mode & 0o077:
        raise ValueError("命令 MAC 密钥文件权限必须为 0600")
    raw = base64.b64decode(open(path, "rb").read().strip(), validate=True)
    if len(raw) < 32:
        raise ValueError("命令 MAC 密钥至少需要 256 位")
    return raw


def build_command(args):
    key = _load_mac_key(args.mac_key)
    issued_monotonic_ns = C.mono_ns()
    command = C.build_target_motion_command(
        control_session_id=args.session_id,
        lease_id=args.lease_id,
        fencing_token=args.fencing,
        command_sequence=args.seq,
        issued_monotonic_ns=issued_monotonic_ns,
        ttl_ms=args.ttl_ms,
        target_speed_mps=args.speed,
        # The placeholder is replaced before the envelope is signed.
        end_to_end_mac=b"\x00" * 32,
    )
    env = C.make_envelope(
        "platform.v1.ControlCommand",
        command.SerializeToString(deterministic=True),
        args.session_id,
        sequence=args.seq,
        vehicle_id=args.vehicle_id,
        gateway_id=args.gateway_id,
        ttl_ms=args.ttl_ms,
    )
    C.sign_control_command_mac(env, command, key)
    env.payload = command.SerializeToString(deterministic=True)
    C.sign_envelope(env, key)
    return env


async def send_dual(args, links):
    env = build_command(args)
    payload_bytes = env.SerializeToString(deterministic=True)
    print(f"命令 seq={args.seq} fencing={args.fencing} 目标 {args.speed} m/s —— 同时走 "
          f"{len(links)} 条链路双发")
    tasks = [run_link(name, addr, payload_bytes, args.ca, args.cert, args.key,
                       args.vehicle_id, args.gateway_id, _load_mac_key(args.mac_key),
                       timeout=args.timeout)
             for name, addr in links]
    return await asyncio.gather(*tasks)


def parse_link(value):
    if "=" not in value:
        raise argparse.ArgumentTypeError("链路格式应为 名称=HOST:PORT")
    name, endpoint = value.split("=", 1)
    if not name or ":" not in endpoint:
        raise argparse.ArgumentTypeError("链路格式应为 名称=HOST:PORT")
    host, port = endpoint.rsplit(":", 1)
    try:
        port_n = int(port)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("链路端口必须为整数") from exc
    if not host or port_n < 1 or port_n > 65535:
        raise argparse.ArgumentTypeError("链路地址无效")
    return name, (host, port_n)


def main():
    ap = argparse.ArgumentParser(description="双链路远驾控制客户端")
    ap.add_argument("--link", action="append", required=True, type=parse_link,
                    help="重复提供：名称=HOST:PORT")
    ap.add_argument("--ca", required=True, help="签发中继端证书的 CA 文件")
    ap.add_argument("--cert", required=True, help="控制代理客户端证书")
    ap.add_argument("--key", required=True, help="控制代理客户端私钥")
    ap.add_argument("--vehicle-id", required=True)
    ap.add_argument("--gateway-id", required=True)
    ap.add_argument("--lease-id", required=True)
    ap.add_argument("--session-id", required=True)
    ap.add_argument("--seq", type=int, required=True)
    ap.add_argument("--fencing", type=int, required=True)
    ap.add_argument("--speed", type=float, required=True)
    ap.add_argument("--ttl-ms", type=int, default=300)
    ap.add_argument("--mac-key", required=True, help="受信控制代理下发的 base64 会话 MAC 密钥文件（0600）")
    ap.add_argument("--timeout", type=float, default=3.0)
    args = ap.parse_args()
    if len(args.link) < 2:
        ap.error("至少提供两条独立 --link")
    if args.seq < 0 or args.fencing < 0 or args.speed < 0 or args.ttl_ms <= 0:
        ap.error("seq、fencing、speed 必须非负，ttl-ms 必须为正")
    asyncio.run(send_dual(args, args.link))


if __name__ == "__main__":
    main()
