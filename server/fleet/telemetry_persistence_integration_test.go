package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestApplyTelemetryDurablyCommitsProjectionAndDedupTogether(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL telemetry test")
	}
	db, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Register the DB close before the data cleanup. testing.Cleanup runs in
	// LIFO order, so all rows are removed while the connection is still open.
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
	vehicleID := "telemetry-integration-" + randHex(8)
	gatewayID := "gateway-telemetry-" + randHex(8)
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, group_id)
		VALUES ($1,$2,'integration','telemetry-group')`, vehicleID, gatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicle_gateways
		(gateway_id, vehicle_id, certificate_sha256, certificate_subject, certificate_not_after, status)
		VALUES ($1,$2,$3,$4,now()+interval '1 hour','active')`, gatewayID, vehicleID,
		gatewayID+"-cert", "spiffe://robot-agent/vehicle/"+vehicleID+"/gateway/"+gatewayID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM inbound_message_dedup WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM inbound_message_cursors WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM telemetry_samples WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_state WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_capabilities WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM events WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_gateways WHERE gateway_id=$1", gatewayID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})

	st := &State{db: db, hub: NewHub(), vehicles: map[string]*vehicleState{}}
	registry := NewGatewayRegistry(db)
	speed := 1.25
	soc := 82.0
	sampleAt := time.Now().UTC()
	sigs := []signalVal{
		{Path: "Vehicle.Speed", Num: &speed, SampleAt: sampleAt},
		{Path: "Vehicle.Powertrain.TractionBattery.StateOfCharge", Num: &soc, SampleAt: sampleAt},
	}
	accepted, err := st.ApplyTelemetryDurably(ctx, registry, vehicleID, gatewayID,
		"platform.v1.SignalUpdate", "telemetry-session", 1, sigs)
	if err != nil || !accepted {
		t.Fatalf("first telemetry was not durably accepted: accepted=%v err=%v", accepted, err)
	}
	var persistedSpeed, persistedSOC float64
	if err := db.QueryRowContext(ctx, `SELECT speed_mps, soc FROM vehicle_state WHERE vehicle_id=$1`, vehicleID).
		Scan(&persistedSpeed, &persistedSOC); err != nil {
		t.Fatal(err)
	}
	if persistedSpeed != speed || persistedSOC != soc {
		t.Fatalf("vehicle_state projection mismatch: speed=%v soc=%v", persistedSpeed, persistedSOC)
	}
	var samples, dedup int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM telemetry_samples WHERE vehicle_id=$1", vehicleID).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM inbound_message_dedup
		WHERE vehicle_id=$1 AND session_id='telemetry-session'`, vehicleID).Scan(&dedup); err != nil {
		t.Fatal(err)
	}
	if samples != len(sigs) || dedup != 1 {
		t.Fatalf("durable telemetry counts mismatch: samples=%d dedup=%d", samples, dedup)
	}

	accepted, err = st.ApplyTelemetryDurably(ctx, registry, vehicleID, gatewayID,
		"platform.v1.SignalUpdate", "telemetry-session", 1, sigs)
	if err != nil || accepted {
		t.Fatalf("duplicate telemetry was projected again: accepted=%v err=%v", accepted, err)
	}
	var samplesAfterDuplicate, dedupAfterDuplicate int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM telemetry_samples WHERE vehicle_id=$1", vehicleID).Scan(&samplesAfterDuplicate); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM inbound_message_dedup
		WHERE vehicle_id=$1 AND session_id='telemetry-session'`, vehicleID).Scan(&dedupAfterDuplicate); err != nil {
		t.Fatal(err)
	}
	if samplesAfterDuplicate != samples || dedupAfterDuplicate != dedup {
		t.Fatalf("duplicate changed durable counts: samples=%d/%d dedup=%d/%d", samplesAfterDuplicate, samples, dedupAfterDuplicate, dedup)
	}

	newSpeed := 4.5
	newSigs := []signalVal{{Path: "Vehicle.Speed", Num: &newSpeed, SampleAt: time.Now().UTC()}}
	accepted, err = st.ApplyTelemetryDurably(ctx, registry, vehicleID, gatewayID,
		"platform.v1.SignalUpdate", "telemetry-session", 2, newSigs)
	if err != nil || !accepted {
		t.Fatalf("second telemetry was not accepted: accepted=%v err=%v", accepted, err)
	}
	oldSpeed := 0.25
	accepted, err = st.ApplyTelemetryDurably(ctx, registry, vehicleID, gatewayID,
		"platform.v1.SignalUpdate", "telemetry-session", 1,
		[]signalVal{{Path: "Vehicle.Speed", Num: &oldSpeed, SampleAt: time.Now().UTC()}})
	if err != nil || accepted {
		t.Fatalf("late telemetry sequence was projected: accepted=%v err=%v", accepted, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT speed_mps FROM vehicle_state WHERE vehicle_id=$1", vehicleID).Scan(&persistedSpeed); err != nil {
		t.Fatal(err)
	}
	if persistedSpeed != newSpeed {
		t.Fatalf("late sequence overwrote newer state: speed=%v want=%v", persistedSpeed, newSpeed)
	}

	missingVehicle := "missing-telemetry-" + randHex(8)
	accepted, err = st.ApplyTelemetryDurably(ctx, registry, missingVehicle, gatewayID,
		"platform.v1.SignalUpdate", "failed-session", 1, sigs)
	if err == nil || accepted {
		t.Fatalf("missing vehicle incorrectly accepted: accepted=%v err=%v", accepted, err)
	}
	var rolledBackDedup int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM inbound_message_dedup
		WHERE vehicle_id=$1 AND session_id='failed-session'`, missingVehicle).Scan(&rolledBackDedup); err != nil {
		t.Fatal(err)
	}
	if rolledBackDedup != 0 {
		t.Fatal("failed projection consumed its deduplication sequence")
	}
}
