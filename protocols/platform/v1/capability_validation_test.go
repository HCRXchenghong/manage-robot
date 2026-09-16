package platformv1

import "testing"

func compatibleGatewayCapabilities() *GatewayCapabilities {
	return &GatewayCapabilities{
		VehicleId: "vehicle-1", GatewayVersion: "0.1.0",
		Stack: AutonomyStack_AUTONOMY_STACK_ROS1, StackVersion: "ROS 1 Noetic",
		SupportedControlModes: []ControlMode{ControlMode_CONTROL_MODE_TARGET_MOTION},
		TopicMappingVersion:   "ros1-map-v0.1", AdapterVersion: "0.1.0",
		SafetyArbiterVersion: "0.1.0", CertificateInstalled: true,
	}
}

func TestAssessGatewayCapabilitiesSeparatesMonitoringFromControl(t *testing.T) {
	caps := compatibleGatewayCapabilities()
	decision, err := AssessGatewayCapabilities(caps)
	if err != nil {
		t.Fatalf("capability declaration rejected: %v", err)
	}
	if !decision.MonitoringAllowed || decision.ControlAllowed {
		t.Fatalf("incomplete hardware must be monitoring-only: %+v", decision)
	}
	if decision.MatrixEntry != "ros1-noetic-platform-v1" || decision.ControlReason == "" {
		t.Fatalf("missing auditable compatibility result: %+v", decision)
	}

	caps.TpmAvailable = true
	caps.Modems = []*ModemInfo{{ModemId: "m-a"}, {ModemId: "m-b"}}
	decision, err = AssessGatewayCapabilities(caps)
	if err != nil || !decision.MonitoringAllowed || !decision.ControlAllowed {
		t.Fatalf("complete hardware must be control-eligible: decision=%+v err=%v", decision, err)
	}
}

func TestAssessGatewayCapabilitiesRejectsUnknownOrMalformedDeclarations(t *testing.T) {
	caps := compatibleGatewayCapabilities()
	caps.GatewayVersion = "9.9.9"
	decision, err := AssessGatewayCapabilities(caps)
	if err != nil {
		t.Fatalf("unknown version should be an auditable incompatibility, not parser failure: %v", err)
	}
	if decision.MonitoringAllowed || decision.ControlAllowed || decision.MatrixEntry != "" {
		t.Fatalf("unknown version was admitted: %+v", decision)
	}

	caps = compatibleGatewayCapabilities()
	caps.Modems = []*ModemInfo{{ModemId: "duplicate"}, {ModemId: "duplicate"}}
	if _, err := AssessGatewayCapabilities(caps); err == nil {
		t.Fatal("duplicate Modem IDs were accepted")
	}

	caps = compatibleGatewayCapabilities()
	caps.SupportedControlModes = nil
	if _, err := AssessGatewayCapabilities(caps); err == nil {
		t.Fatal("missing control mode declaration was accepted")
	}

	caps = compatibleGatewayCapabilities()
	caps.SupportedControlModes = []ControlMode{ControlMode(99)}
	if _, err := AssessGatewayCapabilities(caps); err == nil {
		t.Fatal("unknown control mode was accepted")
	}
}
