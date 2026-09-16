#!/usr/bin/env python3
"""Protocol smoke test; requires the pinned protobuf runtime from requirements-dev."""

from pathlib import Path
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "protocols" / "gen" / "python"))

from robot_agent_platform.v1 import auth, control_pb2, envelope_pb2, telemetry_pb2  # noqa: E402


def main() -> None:
    now = time.time_ns()
    command = control_pb2.ControlCommand(
        control_session_id="session-1",
        lease_id="lease-1",
        fencing_token=4,
        command_sequence=5,
        issued_monotonic_ns=6,
        ttl_ms=300,
        mode=control_pb2.CONTROL_MODE_TARGET_MOTION,
    )
    command.motion.target_speed_mps = 1.0
    env = envelope_pb2.Envelope(
        schema_major=1,
        schema_minor=0,
        message_type="platform.v1.ControlCommand",
        vehicle_id="vehicle-1",
        gateway_id="gateway-1",
        session_id="session-1",
        sequence=5,
        utc_time_ns=now,
        monotonic_time_ns=6,
        ttl_ms=300,
        trace_id="trace-1",
        payload=command.SerializeToString(deterministic=True),
    )
    auth.validate_envelope(env, env.message_type, "vehicle-1", "gateway-1", now_ns=now)
    key = b"0123456789abcdef0123456789abcdef"
    auth.sign_envelope(env, key)
    assert auth.verify_envelope_auth(env, key)
    parsed = envelope_pb2.Envelope.FromString(env.SerializeToString(deterministic=True))
    assert auth.verify_envelope_auth(parsed, key)
    parsed.payload = b"tampered"
    assert not auth.verify_envelope_auth(parsed, key)

    telemetry = telemetry_pb2.SignalUpdate()
    telemetry.signals.add(
        path="Vehicle.Speed",
        value=telemetry_pb2.Value(number=1.0),
        sample_monotonic_ns=6,
        sample_utc_ns=now,
        quality=telemetry_pb2.SIGNAL_QUALITY_GOOD,
    )
    auth.validate_signal_update(telemetry, now_ns=now)
    invalid = telemetry_pb2.SignalUpdate()
    invalid.signals.add(path="Vehicle.Speed", quality=telemetry_pb2.SIGNAL_QUALITY_GOOD)
    try:
        auth.validate_signal_update(invalid, now_ns=now)
    except ValueError:
        pass
    else:
        raise AssertionError("invalid telemetry without value/timestamp was accepted")

    golden = envelope_pb2.Envelope(
        schema_major=1,
        schema_minor=0,
        message_type="platform.v1.ControlCommand",
        vehicle_id="vehicle-1",
        gateway_id="gateway-1",
        session_id="session-1",
        sequence=7,
        utc_time_ns=1_700_000_000_000_000_000,
        monotonic_time_ns=99,
        ttl_ms=300,
        trace_id="trace-1",
        payload=bytes([8, 1]),
    )
    auth.sign_envelope(golden, key)
    assert golden.auth_tag.hex() == (
        "94f7b4c22afaac815f0a6cfe3cb04521cdecb618f1698ce8d0f3e4fbefde0957"
    )
    print("python protocol round-trip/auth smoke test passed")


if __name__ == "__main__":
    main()
