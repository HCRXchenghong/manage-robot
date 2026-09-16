package main

import (
	"encoding/json"
	"strings"
	"testing"

	platformv1 "robot-agent/protocols/platform/v1"
)

func TestCapabilitySnapshotExcludesCarrierICCIDAndGroupIsEvidenceOnly(t *testing.T) {
	caps := &platformv1.GatewayCapabilities{
		VehicleId: "vehicle-1", GatewayVersion: "0.1.0",
		Stack: platformv1.AutonomyStack_AUTONOMY_STACK_ROS1, StackVersion: "ROS 1 Noetic",
		SupportedControlModes: []platformv1.ControlMode{platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION},
		TopicMappingVersion:   "ros1-map-v0.1", AdapterVersion: "0.1.0", SafetyArbiterVersion: "0.1.0",
		CertificateInstalled: true, GroupId: "attacker-group",
		Modems: []*platformv1.ModemInfo{{ModemId: "modem-a", Carrier: "carrier-a", Iccid: "secret-iccid"}},
	}
	raw, err := buildCapabilitySnapshot("gateway-1", caps)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-iccid") {
		t.Fatal("capability snapshot persisted ICCID")
	}
	if decoded["declared_group_id"] != "attacker-group" {
		t.Fatalf("declared group evidence missing: %v", decoded)
	}
}
