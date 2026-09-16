package platformv1

import (
	"encoding/hex"
	"testing"
	"time"
)

func TestEnvelopeAuthTagRoundTripAndTamperDetection(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	key := []byte("0123456789abcdef0123456789abcdef")
	env := &Envelope{
		SchemaMajor: 1, SchemaMinor: 0, MessageType: "platform.v1.ControlCommand",
		VehicleId: "vehicle-1", GatewayId: "gateway-1", SessionId: "session-1",
		Sequence: 7, UtcTimeNs: now.UnixNano(), MonotonicTimeNs: 99, TtlMs: 300,
		TraceId: "trace-1", Payload: []byte{1, 2, 3},
	}
	if err := ValidateEnvelope(env, "platform.v1.ControlCommand", "vehicle-1", "gateway-1", now); err != nil {
		t.Fatalf("validate before signing: %v", err)
	}
	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !VerifyEnvelopeAuth(env, key) {
		t.Fatal("valid auth tag was rejected")
	}
	env.Payload[0] ^= 0xff
	if VerifyEnvelopeAuth(env, key) {
		t.Fatal("tampered payload passed auth verification")
	}
}

func TestEnvelopeAuthTagCrossLanguageGoldenVector(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	env := &Envelope{
		SchemaMajor: 1, SchemaMinor: 0, MessageType: "platform.v1.ControlCommand",
		VehicleId: "vehicle-1", GatewayId: "gateway-1", SessionId: "session-1",
		Sequence: 7, UtcTimeNs: 1700000000000000000, MonotonicTimeNs: 99, TtlMs: 300,
		TraceId: "trace-1", Payload: []byte{8, 1},
	}
	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("sign golden vector: %v", err)
	}
	want := "94f7b4c22afaac815f0a6cfe3cb04521cdecb618f1698ce8d0f3e4fbefde0957"
	if got := hex.EncodeToString(env.GetAuthTag()); got != want {
		t.Fatalf("cross-language auth tag mismatch: got=%s want=%s", got, want)
	}
}
