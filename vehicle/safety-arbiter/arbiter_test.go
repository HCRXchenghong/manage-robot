package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

type testActuator struct {
	motions int
	minimal int
}

func (a *testActuator) ApplyMotion(Motion) (ActuationResult, error) {
	a.motions++
	return ActuationResult{AppliedMonotonicNS: int64(a.motions)}, nil
}

func (a *testActuator) MinimalRisk(string) (ActuationResult, error) {
	a.minimal++
	return ActuationResult{AppliedMonotonicNS: int64(a.minimal)}, nil
}

func testArbiter(t *testing.T, now *time.Time) (*Arbiter, ed25519.PrivateKey, []byte, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "test-authority"
	macKey := []byte("0123456789abcdef0123456789abcdef")
	actuator := &testActuator{}
	arb, err := NewArbiter(Config{
		VehicleID: "veh-1", GatewayID: "gw-1",
		AuthorityKeys: map[string]ed25519.PublicKey{keyID: pub},
		CommandMACKey: macKey, RequireMAC: true,
		Watchdog: 500 * time.Millisecond, MaxClockSkew: 100 * time.Millisecond,
		Limits: Limits{MaxSpeedMPS: 2, MaxAccelerationMPS: 1,
			MaxCurvatureInvM: 0.5, MaxYawRateRadPS: 0.8},
		Actuator: actuator,
		Now:      func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if actuator.motions != 0 || actuator.minimal != 0 {
		t.Fatalf("new arbiter performed unexpected actuator call: %+v", actuator)
	}
	return arb, priv, macKey, keyID
}

func signedProtoGrant(t *testing.T, priv ed25519.PrivateKey, keyID string, envelopeKey []byte,
	now time.Time, leaseID string, fencing uint64, action platformv1.LeaseAction) *platformv1.Envelope {
	t.Helper()
	grant := &platformv1.LeaseGrant{
		Version: 1, Action: action,
		VehicleId: "veh-1", GatewayId: "gw-1", LeaseId: leaseID,
		DriverId: "driver-1", DeviceId: "device-1", FencingToken: fencing,
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
		VehicleId: "veh-1", GatewayId: "gw-1", SessionId: "authority", Sequence: fencing,
		UtcTimeNs: now.UnixNano(), MonotonicTimeNs: 1, TtlMs: 30000,
		TraceId: "lease-test", Payload: payload,
	}
	if err := platformv1.SignEnvelope(env, envelopeKey); err != nil {
		t.Fatal(err)
	}
	return env
}

func signedProtoCommand(t *testing.T, key []byte, leaseID string, fencing, sequence uint64,
	issued time.Time, mode platformv1.ControlMode) *platformv1.Envelope {
	t.Helper()
	cmd := &platformv1.ControlCommand{
		ControlSessionId: "session-test", LeaseId: leaseID, FencingToken: fencing,
		CommandSequence: sequence, IssuedMonotonicNs: 2, TtlMs: 300,
		Mode: mode, EndToEndMac: make([]byte, 32),
	}
	if mode == platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION {
		cmd.Command = &platformv1.ControlCommand_Motion{Motion: &platformv1.TargetMotion{
			TargetSpeedMps: 1, TargetAccelerationMps2: 0.2,
			TargetCurvatureInvM: 0.1, TargetYawRateRadps: 0.1,
		}}
	} else {
		cmd.Command = &platformv1.ControlCommand_MinimalRisk{MinimalRisk: &platformv1.MinimalRiskCommand{
			Reason: platformv1.MinimalRiskReason_MINIMAL_RISK_REASON_DRIVER_REQUEST,
		}}
	}
	env := &platformv1.Envelope{
		SchemaMajor: 1, SchemaMinor: 0, MessageType: "platform.v1.ControlCommand",
		VehicleId: "veh-1", GatewayId: "gw-1", SessionId: "session-test", Sequence: sequence,
		UtcTimeNs: issued.UnixNano(), MonotonicTimeNs: 2, TtlMs: 300,
		TraceId: "command-test",
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

func TestRejectsForgedLeaseGrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	arb, trusted, envelopeKey, keyID := testArbiter(t, &now)
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	forged := signedProtoGrant(t, attacker, keyID, envelopeKey, now, "lease-1", 1,
		platformv1.LeaseAction_LEASE_ACTION_GRANT)
	if got := arb.HandleProtoLease(forged); got.Result != RejectedAuth {
		t.Fatalf("forged grant=%+v, want auth rejection", got)
	}
	valid := signedProtoGrant(t, trusted, keyID, envelopeKey, now, "lease-1", 1,
		platformv1.LeaseAction_LEASE_ACTION_GRANT)
	if got := arb.HandleProtoLease(valid); got.Result != Accepted {
		t.Fatalf("valid grant=%+v", got)
	}
}

func TestProtoLeaseRequiresEnvelopeAuthentication(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	arb, priv, envelopeKey, keyID := testArbiter(t, &now)
	valid := signedProtoGrant(t, priv, keyID, envelopeKey, now, "lease-proto", 1,
		platformv1.LeaseAction_LEASE_ACTION_GRANT)
	if got := arb.HandleProtoLease(valid); got.Result != Accepted {
		t.Fatalf("valid protobuf grant=%+v", got)
	}

	bad := signedProtoGrant(t, priv, keyID, envelopeKey, now, "lease-bad", 2,
		platformv1.LeaseAction_LEASE_ACTION_GRANT)
	bad.AuthTag = []byte("invalid")
	if got := arb.HandleProtoLease(bad); got.Result != RejectedAuth {
		t.Fatalf("protobuf grant without valid Envelope.auth_tag=%+v", got)
	}
}

func TestSecurityAttackMatrix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	arb, priv, macKey, keyID := testArbiter(t, &now)
	if got := arb.HandleProtoLease(signedProtoGrant(t, priv, keyID, macKey, now,
		"lease-1", 1, platformv1.LeaseAction_LEASE_ACTION_GRANT)); got.Result != Accepted {
		t.Fatalf("grant=%+v", got)
	}

	cmd := signedProtoCommand(t, macKey, "lease-1", 1, 1, now,
		platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)
	if got := arb.HandleProtoCommand(cmd); got.Result != Accepted {
		t.Fatalf("valid command=%+v", got)
	}
	if got := arb.HandleProtoCommand(cmd); got.Result != RejectedStale {
		t.Fatalf("replay=%+v, want stale", got)
	}
	wrongFence := signedProtoCommand(t, macKey, "lease-1", 2, 2, now,
		platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)
	if got := arb.HandleProtoCommand(wrongFence); got.Result != RejectedFencing {
		t.Fatalf("wrong fencing=%+v", got)
	}
	stale := signedProtoCommand(t, macKey, "lease-1", 1, 3, now.Add(-time.Second),
		platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)
	if got := arb.HandleProtoCommand(stale); got.Result != RejectedStale {
		t.Fatalf("stale command=%+v", got)
	}
	tampered := signedProtoCommand(t, macKey, "lease-1", 1, 4, now,
		platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)
	tampered.Payload[0] ^= 0xff
	if got := arb.HandleProtoCommand(tampered); got.Result != RejectedAuth {
		t.Fatalf("tampered command=%+v", got)
	}

	now = now.Add(600 * time.Millisecond)
	if got := arb.Tick(); !got.MinimalRisk || got.RiskReason != "control_watchdog_timeout" {
		t.Fatalf("watchdog=%+v", got)
	}
	if got := arb.HandleProtoCommand(signedProtoCommand(t, macKey, "lease-1", 1, 5, now,
		platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION)); got.Result != RejectedLease {
		t.Fatalf("late packet after watchdog=%+v", got)
	}
	if got := arb.HandleProtoLease(signedProtoGrant(t, priv, keyID, macKey, now,
		"lease-2", 2, platformv1.LeaseAction_LEASE_ACTION_GRANT)); got.Result != Accepted {
		t.Fatalf("higher fencing re-arm=%+v", got)
	}
}
