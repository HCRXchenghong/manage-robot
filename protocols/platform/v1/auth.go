package platformv1

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
)

const (
	SupportedSchemaMajor uint32 = 1
	SupportedSchemaMinor uint32 = 0
	MaxEnvelopePayload          = 1024 * 1024
	MaxEnvelopeTTLMS     uint32 = 60_000
)

// monotonicEpoch is process-local. It is intentionally never compared across
// hosts; cross-host freshness uses utc_time_ns, while this value is used only
// for local execution/latency measurements and watchdog evidence.
var monotonicEpoch = time.Now()

func MonotonicNowNS() uint64 {
	n := time.Since(monotonicEpoch).Nanoseconds()
	if n <= 0 {
		return 1
	}
	return uint64(n)
}

// ValidateEnvelope validates the transport-level security contract. Payload
// semantics remain the responsibility of the message-specific validator.
func ValidateEnvelope(env *Envelope, expectedType, vehicleID, gatewayID string, now time.Time) error {
	if env == nil {
		return fmt.Errorf("envelope is nil")
	}
	if env.GetSchemaMajor() != SupportedSchemaMajor || env.GetSchemaMinor() != SupportedSchemaMinor {
		return fmt.Errorf("unsupported schema version %d.%d", env.GetSchemaMajor(), env.GetSchemaMinor())
	}
	if expectedType != "" && env.GetMessageType() != expectedType {
		return fmt.Errorf("unexpected message type %q", env.GetMessageType())
	}
	if env.GetVehicleId() == "" || env.GetVehicleId() != vehicleID || env.GetGatewayId() == "" || env.GetGatewayId() != gatewayID {
		return fmt.Errorf("vehicle/gateway identity mismatch")
	}
	if env.GetSessionId() == "" || env.GetSequence() == 0 || env.GetUtcTimeNs() <= 0 || env.GetMonotonicTimeNs() == 0 {
		return fmt.Errorf("missing envelope freshness fields")
	}
	if env.GetTtlMs() == 0 || env.GetTtlMs() > MaxEnvelopeTTLMS || env.GetTraceId() == "" {
		return fmt.Errorf("invalid envelope ttl or trace")
	}
	if len(env.GetPayload()) == 0 || len(env.GetPayload()) > MaxEnvelopePayload {
		return fmt.Errorf("invalid envelope payload size")
	}
	if now.IsZero() {
		now = time.Now()
	}
	age := now.Sub(time.Unix(0, env.GetUtcTimeNs()))
	if age < -2*time.Second || age > time.Duration(env.GetTtlMs())*time.Millisecond+2*time.Second {
		return fmt.Errorf("envelope expired or clock skew is excessive")
	}
	return nil
}

// SignEnvelope computes the end-to-end tag over deterministic protobuf bytes
// with auth_tag cleared. The caller must still validate the envelope and the
// message-specific payload before sending it.
func SignEnvelope(env *Envelope, key []byte) error {
	if env == nil || len(key) < 32 {
		return fmt.Errorf("envelope and a 256-bit key are required")
	}
	unsigned := proto.Clone(env).(*Envelope)
	unsigned.AuthTag = nil
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(unsigned)
	if err != nil {
		return fmt.Errorf("marshal unsigned envelope: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	env.AuthTag = mac.Sum(nil)
	return nil
}

// VerifyEnvelopeAuth checks the tag in constant time and rejects a missing or
// malformed tag. It does not mutate the caller's message.
func VerifyEnvelopeAuth(env *Envelope, key []byte) bool {
	if env == nil || len(key) < 32 || len(env.GetAuthTag()) != sha256.Size {
		return false
	}
	got := append([]byte(nil), env.GetAuthTag()...)
	unsigned := proto.Clone(env).(*Envelope)
	unsigned.AuthTag = nil
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(unsigned)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return hmac.Equal(got, mac.Sum(nil))
}

// ControlCommandMACInput returns the canonical bytes authenticated by the
// short-lived control-session MAC. The nested command MAC is cleared before
// serialization, and the surrounding Envelope metadata is included. This
// keeps the end-to-end MAC independent of JSON field ordering and prevents a
// relay from changing routing/freshness fields without invalidating it.
func ControlCommandMACInput(env *Envelope, command *ControlCommand) ([]byte, error) {
	if env == nil || command == nil {
		return nil, fmt.Errorf("Envelope 和 ControlCommand 不能为空")
	}
	unsignedCommand := proto.Clone(command).(*ControlCommand)
	unsignedCommand.EndToEndMac = nil
	commandBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(unsignedCommand)
	if err != nil {
		return nil, fmt.Errorf("marshal unsigned ControlCommand: %w", err)
	}
	unsignedEnvelope := proto.Clone(env).(*Envelope)
	unsignedEnvelope.Payload = commandBytes
	unsignedEnvelope.AuthTag = nil
	return (proto.MarshalOptions{Deterministic: true}).Marshal(unsignedEnvelope)
}

// SignControlCommandMAC adds the end-to-end command MAC. Callers must sign
// the enclosing Envelope after changing its payload.
func SignControlCommandMAC(env *Envelope, command *ControlCommand, key []byte) error {
	if len(key) < 32 {
		return fmt.Errorf("ControlCommand MAC 需要至少 256 位密钥")
	}
	canonical, err := ControlCommandMACInput(env, command)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	command.EndToEndMac = mac.Sum(nil)
	return nil
}

// VerifyControlCommandMAC verifies the end-to-end command MAC in constant
// time without mutating either message.
func VerifyControlCommandMAC(env *Envelope, command *ControlCommand, key []byte) bool {
	if env == nil || command == nil || len(key) < 32 || len(command.GetEndToEndMac()) != sha256.Size {
		return false
	}
	canonical, err := ControlCommandMACInput(env, command)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return hmac.Equal(command.GetEndToEndMac(), mac.Sum(nil))
}
