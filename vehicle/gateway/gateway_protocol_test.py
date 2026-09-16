#!/usr/bin/env python3
"""Gateway binary control boundary tests; never a production data source."""

import os
import socket
import unittest
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "protocols" / "gen" / "python"))
from robot_agent_platform.v1 import capabilities_pb2, compatibility, control_pb2, envelope_pb2
from robot_agent_platform.v1 import auth as proto_auth

from gateway import (
    control_proto_to_uds,
    encode_local_frame,
    parse_binary_control,
    parse_component_peer,
    peer_credentials,
    uds_ack_to_binary,
)
from robot_agent_platform.v1 import local_pb2


class GatewayProtocolTest(unittest.TestCase):
    def test_component_peer_parser_requires_explicit_uid_and_optional_gid(self):
        self.assertEqual(parse_component_peer("adapter=1000:1001"),
                         ("adapter", (1000, 1001)))
        self.assertEqual(parse_component_peer("arbiter=1002"),
                         ("arbiter", (1002, None)))
        with self.assertRaises(Exception):
            parse_component_peer("unknown=1000")
        with self.assertRaises(Exception):
            parse_component_peer("workspace=-1")

    def test_unix_peer_credentials_are_available_for_local_socket(self):
        left, right = socket.socketpair()
        try:
            creds = peer_credentials(left)
            if creds is None:
                self.skipTest("当前平台未提供 Unix peer credentials API")
            self.assertEqual(creds[0], os.geteuid())
            self.assertEqual(creds[1], os.getegid())
        finally:
            left.close()
            right.close()

    def test_signed_control_is_decoded(self):
        now = __import__("time").time_ns()
        key = b"0123456789abcdef0123456789abcdef"
        cmd = control_pb2.ControlCommand(
            control_session_id="session-1",
            lease_id="lease-1",
            fencing_token=1,
            command_sequence=2,
            issued_monotonic_ns=3,
            ttl_ms=300,
            mode=control_pb2.CONTROL_MODE_TARGET_MOTION,
            end_to_end_mac=b"x" * 32,
        )
        cmd.motion.target_speed_mps = 0.5
        env = envelope_pb2.Envelope(
            schema_major=1,
            schema_minor=0,
            message_type="platform.v1.ControlCommand",
            vehicle_id="vehicle-1",
            gateway_id="gateway-1",
            session_id="session-1",
            sequence=2,
            utc_time_ns=now,
            monotonic_time_ns=4,
            ttl_ms=300,
            trace_id="trace-1",
            payload=cmd.SerializeToString(deterministic=True),
        )
        proto_auth.sign_envelope(env, key)
        decoded_env, decoded_cmd = parse_binary_control(
            env.SerializeToString(deterministic=True), "vehicle-1", "gateway-1"
        )
        self.assertIsNotNone(decoded_env)
        self.assertEqual(decoded_cmd.command_sequence, 2)

    def test_missing_auth_tag_is_rejected(self):
        env = envelope_pb2.Envelope(
            schema_major=1,
            schema_minor=0,
            message_type="platform.v1.ControlCommand",
            vehicle_id="vehicle-1",
            gateway_id="gateway-1",
            session_id="session-1",
            sequence=2,
            utc_time_ns=__import__("time").time_ns(),
            monotonic_time_ns=4,
            ttl_ms=300,
            trace_id="trace-1",
            payload=b"invalid",
        )
        self.assertEqual(
            parse_binary_control(env.SerializeToString(), "vehicle-1", "gateway-1"),
            (None, None),
        )

    def test_signed_arbiter_ack_survives_local_compatibility_boundary(self):
        now = __import__("time").time_ns()
        key = b"0123456789abcdef0123456789abcdef"
        ack = control_pb2.ControlAck(
            control_session_id="session-1",
            command_sequence=2,
            result=control_pb2.CONTROL_RESULT_ACCEPTED,
            detail="accepted",
            applied_monotonic_ns=8,
        )
        raw = ack.SerializeToString(deterministic=True)
        env = envelope_pb2.Envelope(
            schema_major=1,
            schema_minor=0,
            message_type="platform.v1.ControlAck",
            vehicle_id="vehicle-1",
            gateway_id="gateway-1",
            session_id="session-1",
            sequence=2,
            utc_time_ns=now,
            monotonic_time_ns=7,
            ttl_ms=1000,
            trace_id="trace-1",
            payload=raw,
        )
        proto_auth.sign_envelope(env, key)
        forwarded = uds_ack_to_binary(env, "vehicle-1", "gateway-1")
        self.assertIsNotNone(forwarded)
        parsed = envelope_pb2.Envelope.FromString(forwarded)
        self.assertTrue(proto_auth.verify_envelope_auth(parsed, key))

    def test_local_control_forward_is_length_prefixed_protobuf(self):
        now = __import__("time").time_ns()
        cmd = control_pb2.ControlCommand(
            control_session_id="session-1", lease_id="lease-1", fencing_token=1,
            command_sequence=2, issued_monotonic_ns=3, ttl_ms=300,
            mode=control_pb2.CONTROL_MODE_TARGET_MOTION, end_to_end_mac=b"x" * 32,
        )
        cmd.motion.target_speed_mps = 0.5
        env = envelope_pb2.Envelope(
            schema_major=1, schema_minor=0, message_type="platform.v1.ControlCommand",
            vehicle_id="vehicle-1", gateway_id="gateway-1", session_id="session-1",
            sequence=2, utc_time_ns=now, monotonic_time_ns=4, ttl_ms=300,
            trace_id="trace-1", payload=cmd.SerializeToString(deterministic=True),
        )
        frame = control_proto_to_uds(env, cmd)
        self.assertIsInstance(frame, local_pb2.LocalFrame)
        encoded = encode_local_frame(frame)
        length = int.from_bytes(encoded[:4], "big")
        self.assertEqual(length, len(encoded) - 4)
        decoded = local_pb2.LocalFrame.FromString(encoded[4:])
        self.assertEqual(decoded.envelope.payload, env.payload)

    def test_capability_matrix_is_fail_closed_and_monitoring_is_separate(self):
        caps = capabilities_pb2.GatewayCapabilities(
            vehicle_id="vehicle-1", gateway_version="0.1.0",
            stack=capabilities_pb2.AUTONOMY_STACK_ROS1,
            stack_version="ROS 1 Noetic",
            supported_control_modes=[control_pb2.CONTROL_MODE_TARGET_MOTION],
            topic_mapping_version="ros1-map-v0.1", adapter_version="0.1.0",
            safety_arbiter_version="0.1.0", certificate_installed=True,
        )
        decision = compatibility.assess_gateway_capabilities(caps)
        self.assertTrue(decision.monitoring_allowed)
        self.assertFalse(decision.control_allowed)
        caps.gateway_version = "9.9.9"
        decision = compatibility.assess_gateway_capabilities(caps)
        self.assertFalse(decision.monitoring_allowed)
        self.assertFalse(decision.control_allowed)


if __name__ == "__main__":
    unittest.main()
