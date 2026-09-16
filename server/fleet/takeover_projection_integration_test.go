package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestTakeoverProjectionIntegration proves that an operator view can be
// rebuilt from the durable lease table after a Fleet process restart. It does
// not use a fake vehicle in the runtime: all rows are scoped test fixtures and
// are removed before the test exits.
func TestTakeoverProjectionIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL takeover projection test")
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
	vehicleID := "takeover-projection-" + randHex(8)
	gatewayID := "gateway-takeover-projection-" + randHex(6)
	leaseID := "lease-takeover-projection-" + randHex(6)
	driverID := "driver-takeover-projection-" + randHex(6)
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, vin)
		VALUES ($1,$2,'integration','')`, vehicleID, gatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicle_gateways
		(gateway_id, vehicle_id, certificate_sha256, certificate_subject, certificate_not_after, status)
		VALUES ($1,$2,$3,$4,now()+interval '1 hour','active')`, gatewayID, vehicleID, gatewayID+"-cert", "spiffe://integration/"+gatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO control_leases
		(id, vehicle_id, gateway_id, driver_id, device_id, fencing_token, state, issued_at, valid_until)
		VALUES ($1,$2,$3,$4,'device-integration',1,'active',now(),now()+interval '1 hour')`, leaseID, vehicleID, gatewayID, driverID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM control_leases WHERE id=$1", leaseID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_gateways WHERE gateway_id=$1", gatewayID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})

	projection := NewTakeoverReg(db)
	got := projection.Mine(driverID)
	if got == nil || got.LeaseID != leaseID || got.VehicleID != vehicleID || got.GatewayID != gatewayID || got.Fencing != 1 {
		t.Fatalf("active lease was not projected from PostgreSQL: %+v", got)
	}
	if got := projection.ByVehicle(vehicleID); got == nil || got.Driver != driverID {
		t.Fatalf("vehicle lease projection missing: %+v", got)
	}
	if active := projection.All(); len(active) != 1 || active[0].LeaseID != leaseID {
		t.Fatalf("unexpected active lease projection: %+v", active)
	}
}
