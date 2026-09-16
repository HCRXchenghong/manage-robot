package platformv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestControlCommandBinaryRoundTrip(t *testing.T) {
	want := &ControlCommand{
		ControlSessionId:  "session-1",
		LeaseId:           "lease-1",
		FencingToken:      9,
		CommandSequence:   12,
		IssuedMonotonicNs: 123456,
		TtlMs:             250,
		Mode:              ControlMode_CONTROL_MODE_TARGET_MOTION,
		Command: &ControlCommand_Motion{Motion: &TargetMotion{
			TargetSpeedMps: 1.25,
		}},
		EndToEndMac: []byte{1, 2, 3, 4},
	}

	encoded, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("protobuf encoding is empty")
	}

	got := new(ControlCommand)
	if err := proto.Unmarshal(encoded, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(want, got) {
		t.Fatalf("round trip mismatch:\nwant=%v\ngot=%v", want, got)
	}
}

func TestEnvelopeCarriesOpaquePayloadAndAuthTag(t *testing.T) {
	want := &Envelope{
		SchemaMajor:     1,
		SchemaMinor:     0,
		MessageType:     "platform.v1.ControlCommand",
		VehicleId:       "vehicle-1",
		GatewayId:       "gateway-1",
		SessionId:       "session-1",
		Sequence:        12,
		UtcTimeNs:       1700000000000000000,
		MonotonicTimeNs: 987654,
		TtlMs:           250,
		TraceId:         "trace-1",
		Payload:         []byte{0x0a, 0x01, 0x78},
		AuthTag:         []byte{0xde, 0xad, 0xbe, 0xef},
	}

	encoded, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := new(Envelope)
	if err := proto.Unmarshal(encoded, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(want, got) {
		t.Fatalf("envelope round trip mismatch:\nwant=%v\ngot=%v", want, got)
	}
}
