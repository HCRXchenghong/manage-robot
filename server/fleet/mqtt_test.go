package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

func signedMQTTEnvelope(t *testing.T, messageType, vehicleID, gatewayID string, key []byte) []byte {
	t.Helper()
	var payload proto.Message
	switch messageType {
	case "platform.v1.GatewayCapabilities":
		payload = &platformv1.GatewayCapabilities{VehicleId: vehicleID, GatewayVersion: "test"}
	case "platform.v1.SignalUpdate":
		payload = &platformv1.SignalUpdate{Signals: []*platformv1.Signal{{Path: "Vehicle.Speed", Quality: platformv1.SignalQuality_SIGNAL_QUALITY_GOOD}}}
	case "platform.v1.GatewayStatus":
		payload = &platformv1.GatewayStatus{UptimeS: 1}
	default:
		t.Fatalf("unsupported test message type %q", messageType)
	}
	payloadBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	env := &platformv1.Envelope{
		SchemaMajor: 1, SchemaMinor: 0, MessageType: messageType,
		VehicleId: vehicleID, GatewayId: gatewayID, SessionId: "test-session",
		Sequence: 1, UtcTimeNs: time.Now().UnixNano(), MonotonicTimeNs: 1,
		TtlMs: 5000, TraceId: "test-trace", Payload: payloadBytes,
	}
	if err := platformv1.SignEnvelope(env, key); err != nil {
		t.Fatal(err)
	}
	return mustProtoBytes(t, env)
}

func mustProtoBytes(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeMQTTEnvelopeRequiresAuthenticatedTopicContract(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	raw := signedMQTTEnvelope(t, "platform.v1.SignalUpdate", "veh-1", "gw-1", key)
	env, gatewayID, vehicleID, kind, err := decodeMQTTEnvelope("gateway/gw-1/vehicle/veh-1/telemetry", raw, key)
	if err != nil || env == nil || gatewayID != "gw-1" || vehicleID != "veh-1" || kind != "telemetry" {
		t.Fatalf("valid MQTT envelope rejected: env=%v gateway=%q vehicle=%q kind=%q err=%v", env, gatewayID, vehicleID, kind, err)
	}

	if _, _, _, _, err := decodeMQTTEnvelope("gateway/gw-1/vehicle/veh-1/status", raw, key); err == nil {
		t.Fatal("message type accepted on the wrong MQTT topic category")
	}
	if _, _, _, _, err := decodeMQTTEnvelope("gateway/gw-1/vehicle/veh-1/telemetry", raw, []byte("abcdefabcdefabcdefabcdefabcdefab")); err == nil {
		t.Fatal("Envelope signed by a different key was accepted")
	}
}

func TestDecodeMQTTEnvelopeRejectsIdentityMismatch(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	raw := signedMQTTEnvelope(t, "platform.v1.GatewayStatus", "veh-1", "gw-1", key)
	if _, _, _, _, err := decodeMQTTEnvelope("gateway/gw-2/vehicle/veh-1/status", raw, key); err == nil {
		t.Fatal("topic/Gateway identity mismatch was accepted")
	}
	if _, _, _, _, err := decodeMQTTEnvelope("gateway/gw-1/vehicle/veh-2/status", raw, key); err == nil {
		t.Fatal("topic/vehicle identity mismatch was accepted")
	}
}

func TestOutboundNavigationSessionsAreFresh(t *testing.T) {
	a := newOutboundSessionID()
	b := newOutboundSessionID()
	if a == "" || b == "" || a == b {
		t.Fatalf("navigation session IDs must be non-empty and unique: %q %q", a, b)
	}
}
