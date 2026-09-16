package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

// This opt-in test proves that a navigation ACK advances the route only when
// its inbound sequence is accepted, and that a late ACK cannot overwrite the
// newer projection. It uses PostgreSQL as the authority; no vehicle simulator
// or fake transport is involved.
func TestNavigationAckPersistenceAndOrdering(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL navigation ACK test")
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	vehicleID := "nav-ack-integration-" + randHex(8)
	gatewayID := "gateway-nav-ack-" + randHex(8)
	routeID := "route-nav-ack-" + randHex(8)
	points, _ := json.Marshal([]NavPoint{{Name: "A", X: 0, Y: 0}, {Name: "B", X: 10, Y: 5}})
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, lifecycle_state)
		VALUES ($1,$2,'integration','online')`, vehicleID, gatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicle_gateways
		(gateway_id, vehicle_id, certificate_sha256, certificate_subject, certificate_not_after, status)
		VALUES ($1,$2,$3,$4,now()+interval '1 hour','active')`, gatewayID, vehicleID,
		gatewayID+"-cert", spiffeGatewayID(vehicleID, gatewayID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO navigation_routes
		(id, vehicle_id, name, points, status, created_at, updated_at, origin)
		VALUES ($1,$2,'integration route',$3::jsonb,'dispatched',now(),now(),'integration')`,
		routeID, vehicleID, string(points)); err != nil {
		t.Fatal(err)
	}
	// Register the cleanup before data cleanup: testing.Cleanup is LIFO.
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM inbound_message_dedup WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM inbound_message_cursors WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM navigation_routes WHERE id=$1", routeID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM events WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_gateways WHERE gateway_id=$1", gatewayID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})

	st := &State{db: db, hub: NewHub()}
	ns := NewNavStore(st)
	registry := NewGatewayRegistry(db)
	ack := func(result platformv1.NavigationResult, seq uint64) (bool, error) {
		return st.HandleNavigationAck(ctx, registry, vehicleID, gatewayID, "mqtt", "nav-ack-session", seq,
			&platformv1.NavigationAck{Version: 1, Action: platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH,
				RouteId: routeID, VehicleId: vehicleID, Result: result, CurrentPoint: 1})
	}
	accepted, err := ack(platformv1.NavigationResult_NAVIGATION_RESULT_ACCEPTED, 1)
	if err != nil || !accepted {
		t.Fatalf("accepted ACK failed: accepted=%v err=%v", accepted, err)
	}
	accepted, err = ack(platformv1.NavigationResult_NAVIGATION_RESULT_STARTED, 3)
	if err != nil || !accepted {
		t.Fatalf("started ACK failed: accepted=%v err=%v", accepted, err)
	}
	// Sequence 2 is valid for the state transition but is older than the
	// durable cursor at 3, so it must be ignored without changing the route.
	accepted, err = ack(platformv1.NavigationResult_NAVIGATION_RESULT_STARTED, 2)
	if err != nil || accepted {
		t.Fatalf("late ACK was not rejected by ordering gate: accepted=%v err=%v", accepted, err)
	}
	var status, result string
	if err := db.QueryRowContext(ctx, "SELECT status, vehicle_ack_result FROM navigation_routes WHERE id=$1", routeID).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	if status != "running" || result != platformv1.NavigationResult_NAVIGATION_RESULT_STARTED.String() {
		t.Fatalf("late ACK changed route: status=%q result=%q", status, result)
	}
	accepted, err = ack(platformv1.NavigationResult_NAVIGATION_RESULT_COMPLETED, 4)
	if err != nil || !accepted {
		t.Fatalf("completed ACK failed: accepted=%v err=%v", accepted, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT status FROM navigation_routes WHERE id=$1", routeID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("final status=%q want completed", status)
	}
	_ = ns
}
