#!/usr/bin/env python3
"""Shared platform.v1 protobuf helpers for trusted vehicle-side clients.

Only the generated bindings under ``protocols/gen/python`` define the wire
format. This facade adds repository-local import resolution and the common
Envelope/authentication rules; it does not contain vehicle defaults or
fabricated runtime data.
"""

from __future__ import annotations

import sys
import time
import uuid
from pathlib import Path

_ROOT = Path(__file__).resolve().parents[2]
_GEN = _ROOT / "protocols" / "gen" / "python"
if str(_GEN) not in sys.path:
    sys.path.insert(0, str(_GEN))

from robot_agent_platform.v1 import auth  # noqa: E402
from robot_agent_platform.v1 import control_pb2  # noqa: E402
from robot_agent_platform.v1 import envelope_pb2  # noqa: E402

SCHEMA_MAJOR = auth.SUPPORTED_SCHEMA_MAJOR
SCHEMA_MINOR = auth.SUPPORTED_SCHEMA_MINOR


def now_ns() -> int:
    """UTC time in nanoseconds, used for cross-host audit only."""
    return time.time_ns()


def mono_ns() -> int:
    """Local monotonic time in nanoseconds, used for command freshness."""
    return time.monotonic_ns()


def make_envelope(
    message_type: str,
    payload: bytes,
    session_id: str,
    sequence: int,
    *,
    vehicle_id: str,
    gateway_id: str,
    ttl_ms: int = 1000,
    auth_key: bytes | None = None,
) -> envelope_pb2.Envelope:
    if not vehicle_id or not gateway_id:
        raise ValueError("vehicle_id and gateway_id are required")
    if not message_type.startswith("platform.v1."):
        raise ValueError("message_type must use platform.v1")
    if not isinstance(payload, bytes) or not payload:
        raise ValueError("protobuf payload must be non-empty bytes")
    env = envelope_pb2.Envelope(
        schema_major=SCHEMA_MAJOR,
        schema_minor=SCHEMA_MINOR,
        message_type=message_type,
        vehicle_id=vehicle_id,
        gateway_id=gateway_id,
        session_id=session_id,
        sequence=sequence,
        utc_time_ns=now_ns(),
        monotonic_time_ns=mono_ns(),
        ttl_ms=ttl_ms,
        trace_id=uuid.uuid4().hex[:16],
        payload=payload,
    )
    if auth_key is not None:
        auth.sign_envelope(env, auth_key)
    return env


def parse_envelope(data: bytes) -> envelope_pb2.Envelope:
    if not isinstance(data, bytes) or not 0 < len(data) <= auth.MAX_ENVELOPE_PAYLOAD + 4096:
        raise ValueError("invalid protobuf envelope size")
    env = envelope_pb2.Envelope.FromString(data)
    if not env.IsInitialized():
        raise ValueError("protobuf envelope is not initialized")
    return env


def validate_envelope(env: envelope_pb2.Envelope, *, vehicle_id: str, gateway_id: str,
                      expected_type: str | None = None, now_ns_value: int | None = None) -> None:
    auth.validate_envelope(
        env,
        expected_type or "",
        vehicle_id,
        gateway_id,
        now_ns=now_ns_value,
    )


def sign_envelope(env: envelope_pb2.Envelope, key: bytes) -> None:
    auth.sign_envelope(env, key)


def verify_envelope_auth(env: envelope_pb2.Envelope, key: bytes) -> bool:
    return auth.verify_envelope_auth(env, key)


def sign_control_command_mac(env: envelope_pb2.Envelope, command: control_pb2.ControlCommand, key: bytes) -> None:
    auth.sign_control_command_mac(env, command, key)


def verify_control_command_mac(env: envelope_pb2.Envelope, command: control_pb2.ControlCommand, key: bytes) -> bool:
    return auth.verify_control_command_mac(env, command, key)


def build_target_motion_command(
    *,
    control_session_id: str,
    lease_id: str,
    fencing_token: int,
    command_sequence: int,
    issued_monotonic_ns: int,
    ttl_ms: int,
    target_speed_mps: float,
    end_to_end_mac: bytes,
) -> control_pb2.ControlCommand:
    if len(end_to_end_mac) != 32:
        raise ValueError("ControlCommand requires a 256-bit end-to-end MAC")
    command = control_pb2.ControlCommand(
        control_session_id=control_session_id,
        lease_id=lease_id,
        fencing_token=fencing_token,
        command_sequence=command_sequence,
        issued_monotonic_ns=issued_monotonic_ns,
        ttl_ms=ttl_ms,
        mode=control_pb2.CONTROL_MODE_TARGET_MOTION,
        end_to_end_mac=end_to_end_mac,
    )
    command.motion.target_speed_mps = target_speed_mps
    return command
