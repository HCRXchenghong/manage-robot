#!/usr/bin/env python3
"""Security boundary tests for the QUIC relay; no vehicle data source."""

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "protocols" / "gen" / "python"))
sys.path.insert(0, str(Path(__file__).resolve().parent))

from robot_agent_platform.v1 import auth, control_pb2, envelope_pb2  # noqa: E402
from control_relay import RelayServer  # noqa: E402


class RelaySecurityTest(unittest.TestCase):
    def test_relay_rejects_envelope_signed_by_wrong_key(self):
        good_key = b"0123456789abcdef0123456789abcdef"
        wrong_key = b"abcdef0123456789abcdef0123456789"
        command = control_pb2.ControlCommand(
            control_session_id="session-1",
            lease_id="lease-1",
            fencing_token=1,
            command_sequence=1,
            issued_monotonic_ns=1,
            ttl_ms=300,
            mode=control_pb2.CONTROL_MODE_TARGET_MOTION,
            end_to_end_mac=b"x" * 32,
        )
        command.motion.target_speed_mps = 0.5
        env = envelope_pb2.Envelope(
            schema_major=1,
            schema_minor=0,
            message_type="platform.v1.ControlCommand",
            vehicle_id="vehicle-1",
            gateway_id="gateway-1",
            session_id="session-1",
            sequence=1,
            utc_time_ns=__import__("time").time_ns(),
            monotonic_time_ns=1,
            ttl_ms=300,
            trace_id="trace-1",
            payload=command.SerializeToString(deterministic=True),
        )
        auth.sign_envelope(env, good_key)
        relay = RelayServer("edge-A", 9443, ("127.0.0.1", 9100),
                            "vehicle-1", "gateway-1", wrong_key)
        self.assertIsNone(relay._valid_control(env))

        trusted = RelayServer("edge-A", 9443, ("127.0.0.1", 9100),
                              "vehicle-1", "gateway-1", good_key)
        self.assertIsNotNone(trusted._valid_control(env))


if __name__ == "__main__":
    unittest.main()
