# 平台协议模拟器公共库
#
# 注意：模拟器先用 JSON「镜像」Protobuf 的 Envelope 结构
# （字段与 protocols/protobuf/platform/v1/*.proto 一一对应）。
# 等 Go/protoc 工具链就位后切换为真正的 Protobuf 二进制序列化，消息结构不变。

import json
import time
import uuid

SCHEMA_MAJOR = 1
SCHEMA_MINOR = 0
VEHICLE_ID = "sim-veh-001"
GATEWAY_ID = "sim-gw-001"


def now_ns():
    """UTC 时间（纳秒）。只用于审计，不用于控制排序。"""
    return time.time_ns()


def mono_ns():
    """单调时钟（纳秒）。控制命令排序/过期判断以它为准。"""
    return time.monotonic_ns()


def make_envelope(message_type, payload, session_id, sequence, ttl_ms=1000,
                  vehicle_id=VEHICLE_ID, gateway_id=GATEWAY_ID):
    """构造统一消息信封（对应 envelope.proto）。"""
    return {
        "schema_major": SCHEMA_MAJOR,
        "schema_minor": SCHEMA_MINOR,
        "message_type": message_type,
        "vehicle_id": vehicle_id,
        "gateway_id": gateway_id,
        "session_id": session_id,
        "sequence": sequence,
        "utc_time_ns": now_ns(),
        "monotonic_time_ns": mono_ns(),
        "ttl_ms": ttl_ms,
        "trace_id": uuid.uuid4().hex[:16],
        # 真实 Protobuf 里这里是序列化后的 bytes；JSON 镜像直接放对象。
        "payload": payload,
    }


def send_json(sock, addr, envelope):
    sock.sendto(json.dumps(envelope).encode("utf-8"), addr)


def recv_json(sock, timeout_s=1.0):
    """收一条消息；超时返回 (None, None)。"""
    sock.settimeout(timeout_s)
    try:
        data, addr = sock.recvfrom(65535)
        return json.loads(data.decode("utf-8")), addr
    except (TimeoutError, OSError):
        return None, None
