package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

func TestGatewayCapabilityPersistenceAndControlGateIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL capability test")
	}
	db, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations := os.Getenv("ROBOT_AGENT_MIGRATIONS")
	if migrations == "" {
		migrations = "../migrations"
	}
	if err := RunMigrations(db, migrations); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vehicleID := "capability-integration-" + randHex(8)
	gatewayID := "gateway-capability-" + randHex(8)
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, vin, group_id)
		VALUES ($1,$2,'integration','', 'platform-group')`, vehicleID, gatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicle_gateways
		(gateway_id, vehicle_id, certificate_sha256, certificate_subject, certificate_not_after, status)
		VALUES ($1,$2,$3,$4,now()+interval '1 hour','active')`, gatewayID, vehicleID,
		gatewayID+"-cert", "spiffe://robot-agent/vehicle/"+vehicleID+"/gateway/"+gatewayID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_capabilities WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM control_security_events WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_state WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_gateways WHERE gateway_id=$1", gatewayID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicle_state(vehicle_id, online, updated_at)
		VALUES ($1,true,now())`, vehicleID); err != nil {
		t.Fatal(err)
	}

	caps := &platformv1.GatewayCapabilities{
		VehicleId: vehicleID, GatewayVersion: "0.1.0",
		Stack: platformv1.AutonomyStack_AUTONOMY_STACK_ROS1, StackVersion: "ROS 1 Noetic",
		SupportedControlModes: []platformv1.ControlMode{platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION},
		TopicMappingVersion:   "ros1-map-v0.1", AdapterVersion: "0.1.0",
		SafetyArbiterVersion: "0.1.0", CertificateInstalled: true,
		GroupId: "untrusted-declared-group",
		Modems:  []*platformv1.ModemInfo{{ModemId: "m-a", Iccid: "private"}},
	}
	decision, err := platformv1.AssessGatewayCapabilities(caps)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewGatewayRegistry(db)
	if err := registry.PersistCapabilities(ctx, gatewayID, caps, decision); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	var snapshot string
	if err := db.QueryRowContext(ctx, `SELECT control_allowed, snapshot::text FROM vehicle_capabilities
		WHERE vehicle_id=$1 AND gateway_id=$2`, vehicleID, gatewayID).Scan(&allowed, &snapshot); err != nil {
		t.Fatal(err)
	}
	if allowed || !strings.Contains(snapshot, "untrusted-declared-group") || strings.Contains(snapshot, "private") {
		t.Fatalf("capability snapshot/control gate incorrect: allowed=%v snapshot=%s", allowed, snapshot)
	}
	var group string
	if err := db.QueryRowContext(ctx, "SELECT group_id FROM vehicles WHERE id=$1", vehicleID).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if group != "platform-group" {
		t.Fatalf("Gateway declaration changed platform-owned group: %q", group)
	}

	caps.TpmAvailable = true
	caps.Modems = []*platformv1.ModemInfo{{ModemId: "m-a"}, {ModemId: "m-b"}}
	decision, err = platformv1.AssessGatewayCapabilities(caps)
	if err != nil || !decision.ControlAllowed {
		t.Fatalf("complete declaration was not control-eligible: decision=%+v err=%v", decision, err)
	}
	if err := registry.PersistCapabilities(ctx, gatewayID, caps, decision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT control_allowed FROM vehicle_capabilities
		WHERE vehicle_id=$1 AND gateway_id=$2`, vehicleID, gatewayID).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("updated complete capability was not persisted as control-eligible")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireVehicleLiveForControlTx(ctx, tx, vehicleID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("live vehicle was rejected by the transactional control gate: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE vehicle_state SET online=false WHERE vehicle_id=$1`, vehicleID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireVehicleLiveForControlTx(ctx, tx, vehicleID); err == nil {
		_ = tx.Rollback()
		t.Fatal("offline vehicle passed the transactional control gate")
	}
	_ = tx.Rollback()
}
