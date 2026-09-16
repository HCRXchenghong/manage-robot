"""Shared Envelope authentication and validation for Python services.

The tag is HMAC-SHA256 over deterministic protobuf bytes of the Envelope with
``auth_tag`` cleared. The descriptor package remains ``platform.v1`` while the
Python import package avoids shadowing the stdlib ``platform`` module.
"""

from __future__ import annotations

import hashlib
import hmac
import math
import time

from . import control_pb2, envelope_pb2, telemetry_pb2

SUPPORTED_SCHEMA_MAJOR = 1
SUPPORTED_SCHEMA_MINOR = 0
MAX_ENVELOPE_PAYLOAD = 1024 * 1024
MAX_ENVELOPE_TTL_MS = 60_000


def validate_envelope(
    env: envelope_pb2.Envelope,
    expected_type: str,
    vehicle_id: str,
    gateway_id: str,
    *,
    now_ns: int | None = None,
) -> None:
    if not isinstance(env, envelope_pb2.Envelope):
        raise ValueError("envelope is not a platform.v1.Envelope")
    if (env.schema_major, env.schema_minor) != (
        SUPPORTED_SCHEMA_MAJOR,
        SUPPORTED_SCHEMA_MINOR,
    ):
        raise ValueError("unsupported schema version")
    if expected_type and env.message_type != expected_type:
        raise ValueError("unexpected message type")
    if not vehicle_id or env.vehicle_id != vehicle_id or not gateway_id or env.gateway_id != gateway_id:
        raise ValueError("vehicle/gateway identity mismatch")
    if not env.session_id or env.sequence <= 0 or env.utc_time_ns <= 0 or env.monotonic_time_ns <= 0:
        raise ValueError("missing envelope freshness fields")
    if not 0 < env.ttl_ms <= MAX_ENVELOPE_TTL_MS or not env.trace_id:
        raise ValueError("invalid envelope ttl or trace")
    if not 0 < len(env.payload) <= MAX_ENVELOPE_PAYLOAD:
        raise ValueError("invalid envelope payload size")
    current_ns = time.time_ns() if now_ns is None else now_ns
    age_ns = current_ns - env.utc_time_ns
    if age_ns < -2_000_000_000 or age_ns > env.ttl_ms * 1_000_000 + 2_000_000_000:
        raise ValueError("envelope expired or clock skew is excessive")


def _unsigned_bytes(env: envelope_pb2.Envelope) -> bytes:
    unsigned = envelope_pb2.Envelope()
    unsigned.CopyFrom(env)
    unsigned.ClearField("auth_tag")
    return unsigned.SerializeToString(deterministic=True)


def sign_envelope(env: envelope_pb2.Envelope, key: bytes) -> None:
    if len(key) < 32:
        raise ValueError("a 256-bit key is required")
    env.auth_tag = hmac.new(key, _unsigned_bytes(env), hashlib.sha256).digest()


def verify_envelope_auth(env: envelope_pb2.Envelope, key: bytes) -> bool:
    if len(key) < 32 or len(env.auth_tag) != hashlib.sha256().digest_size:
        return False
    expected = hmac.new(key, _unsigned_bytes(env), hashlib.sha256).digest()
    return hmac.compare_digest(env.auth_tag, expected)


def validate_signal_update(
    update: telemetry_pb2.SignalUpdate, *, now_ns: int | None = None
) -> None:
    """Validate semantic telemetry fields shared by Gateway and Fleet."""
    if not isinstance(update, telemetry_pb2.SignalUpdate) or not 0 < len(update.signals) <= 512:
        raise ValueError("invalid SignalUpdate signal count")
    current_ns = time.time_ns() if now_ns is None else now_ns
    seen: set[str] = set()
    valid_quality = {
        telemetry_pb2.SIGNAL_QUALITY_GOOD,
        telemetry_pb2.SIGNAL_QUALITY_STALE,
        telemetry_pb2.SIGNAL_QUALITY_MISSING,
        telemetry_pb2.SIGNAL_QUALITY_INVALID,
    }
    for signal in update.signals:
        path = signal.path.strip()
        if not path or len(path) > 256 or not path.isprintable() or path in seen:
            raise ValueError("invalid or duplicate Signal path")
        seen.add(path)
        if signal.quality not in valid_quality:
            raise ValueError("invalid Signal quality")
        if signal.sample_monotonic_ns <= 0 or signal.sample_utc_ns <= 0:
            raise ValueError("Signal sample timestamp is required")
        if signal.sample_utc_ns > current_ns + 2_000_000_000 or signal.sample_utc_ns < current_ns - 86_400_000_000_000:
            raise ValueError("Signal sample timestamp is outside the allowed window")
        if not signal.HasField("value"):
            if signal.quality in (telemetry_pb2.SIGNAL_QUALITY_GOOD, telemetry_pb2.SIGNAL_QUALITY_STALE):
                raise ValueError("valid Signal requires value")
            continue
        kind = signal.value.WhichOneof("kind")
        if kind == "number":
            if not math.isfinite(signal.value.number):
                raise ValueError("Signal number must be finite")
        elif kind == "number_list":
            if len(signal.value.number_list.values) > 256 or any(
                not math.isfinite(value) for value in signal.value.number_list.values
            ):
                raise ValueError("Signal number list is invalid")
        elif kind in ("boolean", "text"):
            pass
        elif kind == "raw":
            if len(signal.value.raw) > 64 * 1024:
                raise ValueError("Signal raw value exceeds limit")
        else:
            raise ValueError("Signal value kind is required")


def control_command_mac_input(
    env: envelope_pb2.Envelope, command: control_pb2.ControlCommand
) -> bytes:
    """Return deterministic bytes for the end-to-end ControlCommand MAC."""
    if not isinstance(env, envelope_pb2.Envelope) or not isinstance(command, control_pb2.ControlCommand):
        raise ValueError("Envelope 和 ControlCommand 类型无效")
    unsigned_command = control_pb2.ControlCommand()
    unsigned_command.CopyFrom(command)
    unsigned_command.ClearField("end_to_end_mac")
    unsigned_envelope = envelope_pb2.Envelope()
    unsigned_envelope.CopyFrom(env)
    unsigned_envelope.payload = unsigned_command.SerializeToString(deterministic=True)
    unsigned_envelope.ClearField("auth_tag")
    return unsigned_envelope.SerializeToString(deterministic=True)


def sign_control_command_mac(
    env: envelope_pb2.Envelope, command: control_pb2.ControlCommand, key: bytes
) -> None:
    if len(key) < 32:
        raise ValueError("ControlCommand MAC 需要至少 256 位密钥")
    command.end_to_end_mac = hmac.new(
        key, control_command_mac_input(env, command), hashlib.sha256
    ).digest()


def verify_control_command_mac(
    env: envelope_pb2.Envelope, command: control_pb2.ControlCommand, key: bytes
) -> bool:
    if len(key) < 32 or len(command.end_to_end_mac) != hashlib.sha256().digest_size:
        return False
    expected = hmac.new(key, control_command_mac_input(env, command), hashlib.sha256).digest()
    return hmac.compare_digest(command.end_to_end_mac, expected)
