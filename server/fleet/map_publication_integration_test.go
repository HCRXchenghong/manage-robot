package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

// This opt-in test closes the map publication state machine against the real
// PostgreSQL schema: approval/outbox atomicity, broker-delivery projection,
// vehicle ACK activation, and a repeatable rollback. No simulated vehicle or
// fake transport is used; the final ACK is an explicit protocol fixture.
func TestMapPublicationLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL map publication test")
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
	vehicleID := "map-pub-integration-" + randHex(8)
	gatewayID := "gateway-map-pub-" + randHex(8)
	_, err = db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, vin, lifecycle_state)
		VALUES ($1,$2,'ROS1','', 'online')`, vehicleID, gatewayID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO vehicle_gateways
		(gateway_id, vehicle_id, certificate_sha256, certificate_subject, certificate_not_after, status)
		VALUES ($1,$2,$3,$4,now()+interval '1 hour','active')`, gatewayID, vehicleID,
		gatewayID+"-cert", spiffeGatewayID(vehicleID, gatewayID))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := json.Marshal(capabilitySnapshot{VehicleID: vehicleID, GatewayID: gatewayID,
		MapFormats: []string{"pcd"}, CoordinateFrame: "map"})
	_, err = db.ExecContext(ctx, `INSERT INTO vehicle_capabilities
		(vehicle_id, gateway_id, matrix_entry, stack, stack_version, gateway_version,
		 adapter_version, safety_arbiter_version, topic_mapping_version,
		 monitoring_allowed, control_allowed, control_reason, snapshot)
		VALUES ($1,$2,'ros1-noetic-gateway-0.1.0','ROS1','ROS 1 Noetic','0.1.0',
		'0.1.0','0.1.0','ros1-map-v0.1',true,true,'',$3::jsonb)`, vehicleID, gatewayID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM platform_outbox WHERE aggregate_type='map_publication' AND aggregate_id IN (SELECT id FROM map_publications WHERE vehicle_id=$1)", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM map_publications WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_capabilities WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicle_gateways WHERE gateway_id=$1", gatewayID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM maps WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM events WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})

	key := []byte("01234567890123456789012345678901")
	st := &State{db: db, hub: NewHub(), envelopeAuthKey: key}
	store, err := NewMapStore(t.TempDir(), "", "", nil, st)
	if err != nil {
		t.Fatal(err)
	}
	dataV1 := []byte("# .PCD v0.7\nVERSION 0.7\nFIELDS x y z\nSIZE 4 4 4\nTYPE F F F\nCOUNT 1 1 1\nPOINTS 3\nDATA ascii\n0 0 0\n1 0 0\n0 1 0\n")
	entry, changed, err := store.Register(vehicleID, "warehouse.pcd", "manual", "operator-a", dataV1)
	if err != nil || !changed {
		t.Fatalf("register v1 changed=%v err=%v", changed, err)
	}
	dataV2 := append(append([]byte(nil), dataV1...), "2"[0])
	if _, changed, err := store.Register(vehicleID, "warehouse.pcd", "manual", "operator-a", dataV2); err != nil || !changed {
		t.Fatalf("register v2 changed=%v err=%v", changed, err)
	}

	first, err := store.RequestMapPublication(ctx, entry.ID, vehicleID, 1, "apply", "map", "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveMapPublication(ctx, first.ID, "operator-b"); err != nil {
		t.Fatal(err)
	}
	var env platformv1.Envelope
	var encoded []byte
	if err := db.QueryRowContext(ctx, `SELECT payload FROM platform_outbox WHERE aggregate_type='map_publication' AND aggregate_id=$1`, first.ID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(encoded, &env); err != nil {
		t.Fatal(err)
	}
	var command platformv1.MapPublicationCommand
	if err := proto.Unmarshal(env.GetPayload(), &command); err != nil || command.GetMapVersion() != 1 || command.GetMapId() != entry.ID {
		t.Fatalf("unexpected map command: %v %+v", err, &command)
	}
	store.markMapPublicationDispatched(first.ID)
	if err := st.HandleMapPublicationAck(vehicleID, gatewayID, "mqtt", &platformv1.MapPublicationAck{
		PublicationId: first.ID, MapId: entry.ID, VehicleId: vehicleID, MapVersion: 1,
		Result:          platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_APPLIED,
		AppliedAtUnixNs: time.Now().UnixNano(),
	}); err != nil {
		t.Fatal(err)
	}

	second, err := store.RequestMapPublication(ctx, entry.ID, vehicleID, 2, "apply", "map", "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveMapPublication(ctx, second.ID, "operator-b"); err != nil {
		t.Fatal(err)
	}
	if err := st.HandleMapPublicationAck(vehicleID, gatewayID, "mqtt", &platformv1.MapPublicationAck{
		PublicationId: second.ID, MapId: entry.ID, VehicleId: vehicleID, MapVersion: 2,
		Result: platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_APPLIED,
	}); err != nil {
		t.Fatal(err)
	}

	rollback, err := store.RollbackMapPublication(ctx, second.ID, "operator-b")
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Action != "rollback" || rollback.Version != 1 || rollback.PreviousPublicationID != second.ID || rollback.RollbackTargetID != first.ID {
		t.Fatalf("unexpected rollback: %+v", rollback)
	}
	if _, err := store.ApproveMapPublication(ctx, rollback.ID, "operator-c"); err != nil {
		t.Fatal(err)
	}
	if err := st.HandleMapPublicationAck(vehicleID, gatewayID, "mqtt", &platformv1.MapPublicationAck{
		PublicationId: rollback.ID, MapId: entry.ID, VehicleId: vehicleID, MapVersion: 1,
		Result: platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_APPLIED,
	}); err != nil {
		t.Fatal(err)
	}
	var active, rolledBack, oldState string
	if err := db.QueryRowContext(ctx, "SELECT state FROM map_publications WHERE id=$1", first.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT state FROM map_publications WHERE id=$1", second.ID).Scan(&rolledBack); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT state FROM map_publications WHERE id=$1", rollback.ID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if active != "active" || rolledBack != "rolled_back" || oldState != "confirmed" {
		t.Fatalf("rollback states incorrect: first=%s second=%s rollback=%s", active, rolledBack, oldState)
	}
}
