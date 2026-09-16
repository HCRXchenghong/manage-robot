package main

import (
	"crypto/ed25519"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

func signedNativeGrant(t *testing.T, priv ed25519.PrivateKey, keyID string, key []byte, now time.Time) *platformv1.Envelope {
	t.Helper()
	grant := &platformv1.LeaseGrant{
		Version: 1, Action: platformv1.LeaseAction_LEASE_ACTION_GRANT,
		VehicleId: "veh-1", GatewayId: "gw-1", LeaseId: "lease-native",
		DriverId: "driver-1", DeviceId: "device-1", FencingToken: 1,
		ValidUntilUnixNs: now.Add(time.Minute).UnixNano(), IssuedAtUnixNs: now.UnixNano(),
		AuthorityKeyId: keyID,
	}
	unsigned := proto.Clone(grant).(*platformv1.LeaseGrant)
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	grant.AuthoritySignature = ed25519.Sign(priv, canonical)
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	env := &platformv1.Envelope{
		SchemaMajor: 1, SchemaMinor: 0, MessageType: "platform.v1.LeaseGrant",
		VehicleId: "veh-1", GatewayId: "gw-1", SessionId: "authority", Sequence: 1,
		UtcTimeNs: now.UnixNano(), MonotonicTimeNs: 1, TtlMs: 30000,
		TraceId: "native-grant", Payload: payload,
	}
	if err := platformv1.SignEnvelope(env, key); err != nil {
		t.Fatal(err)
	}
	return env
}

func signedNativeCommand(t *testing.T, key []byte, leaseID string, fencing, sequence uint64, now time.Time, mode platformv1.ControlMode) *platformv1.Envelope {
	t.Helper()
	cmd := &platformv1.ControlCommand{
		ControlSessionId: "session-native", LeaseId: leaseID, FencingToken: fencing,
		CommandSequence: sequence, IssuedMonotonicNs: 2, TtlMs: 300,
		Mode: mode, EndToEndMac: make([]byte, 32),
	}
	if mode == platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION {
		cmd.Command = &platformv1.ControlCommand_Motion{Motion: &platformv1.TargetMotion{TargetSpeedMps: 1}}
	} else {
		cmd.Command = &platformv1.ControlCommand_MinimalRisk{MinimalRisk: &platformv1.MinimalRiskCommand{
			Reason: platformv1.MinimalRiskReason_MINIMAL_RISK_REASON_DRIVER_REQUEST,
		}}
	}
	env := &platformv1.Envelope{
		SchemaMajor: 1, SchemaMinor: 0, MessageType: "platform.v1.ControlCommand",
		VehicleId: "veh-1", GatewayId: "gw-1", SessionId: "session-native", Sequence: sequence,
		UtcTimeNs: now.UnixNano(), MonotonicTimeNs: 2, TtlMs: 300,
		TraceId: "native-command", Payload: []byte{1},
	}
	if err := platformv1.SignControlCommandMAC(env, cmd, key); err != nil {
		t.Fatal(err)
	}
	var err error
	env.Payload, err = (proto.MarshalOptions{Deterministic: true}).Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformv1.SignEnvelope(env, key); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestProtoKernelUsesBinaryCommandPath(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	arb, priv, macKey, keyID := testArbiter(t, &now)
	if got := arb.HandleProtoLease(signedNativeGrant(t, priv, keyID, macKey, now)); got.Result != Accepted {
		t.Fatalf("native grant=%+v", got)
	}
	command := signedNativeCommand(t, macKey, "lease-native", 1, 1, now, platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)
	if got := arb.HandleProtoCommand(command); got.Result != Accepted {
		t.Fatalf("native command=%+v", got)
	}
	if got := arb.HandleProtoCommand(command); got.Result != RejectedStale {
		t.Fatalf("native replay=%+v", got)
	}
}

func TestProtoKernelRejectsTamperAndSupportsMinimalRisk(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	arb, priv, macKey, keyID := testArbiter(t, &now)
	if got := arb.HandleProtoLease(signedNativeGrant(t, priv, keyID, macKey, now)); got.Result != Accepted {
		t.Fatalf("native grant=%+v", got)
	}
	command := signedNativeCommand(t, macKey, "lease-native", 1, 1, now, platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)
	command.Payload[0] ^= 0xff
	if got := arb.HandleProtoCommand(command); got.Result != RejectedAuth {
		t.Fatalf("tampered native command=%+v", got)
	}
	minimal := signedNativeCommand(t, macKey, "lease-native", 1, 2, now, platformv1.ControlMode_CONTROL_MODE_MINIMAL_RISK)
	if got := arb.HandleProtoCommand(minimal); got.Result != Accepted || !got.MinimalRisk {
		t.Fatalf("minimal risk command=%+v", got)
	}
}
