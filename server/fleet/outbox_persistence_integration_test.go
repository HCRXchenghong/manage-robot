package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNavigationOutboxPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL outbox test")
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
	vehicleID := "outbox-integration-" + randHex(8)
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, vin) VALUES ($1,'gw-outbox-integration','test','')`, vehicleID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM platform_outbox WHERE aggregate_type='navigation_route' AND aggregate_id IN (SELECT id FROM navigation_routes WHERE vehicle_id=$1)", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM navigation_routes WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM events WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})

	st := &State{
		vehicles:          map[string]*vehicleState{vehicleID: {id: vehicleID, gatewayID: "gw-outbox-integration"}},
		hub:               NewHub(),
		db:                db,
		dbCh:              make(chan dbJob, 32),
		envelopeAuthKey:   []byte("01234567890123456789012345678901"),
		outboundSessionID: "integration-navigation-session",
		outboundSeq:       map[string]uint64{},
		outboxWake:        make(chan struct{}, 1),
	}
	ns := NewNavStore(st)
	route, err := ns.Submit(vehicleID, "巡检路线", "integration", "trace-outbox", "持久化 outbox 验证", []NavPoint{
		{Name: "A", X: 0, Y: 0}, {Name: "B", X: 10, Y: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.Status != "queued" {
		t.Fatalf("route must wait for Broker acceptance, got %q", route.Status)
	}

	items, err := st.claimOutbox()
	if err != nil {
		t.Fatal(err)
	}
	var found *outboxItem
	for i := range items {
		if items[i].aggregateType == "navigation_route" && items[i].aggregateID == route.ID {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("navigation command was not stored in outbox: %+v", items)
	}
	st.markOutbox(*found, true, found.attempts, "")
	if got, ok := ns.Get(route.ID); !ok || got.Status != "dispatched" {
		t.Fatalf("Broker acceptance did not project route status: ok=%v route=%+v", ok, got)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM navigation_routes WHERE id=$1", route.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dispatched" {
		t.Fatalf("route status was not persisted after Broker acceptance: %q", status)
	}
}
