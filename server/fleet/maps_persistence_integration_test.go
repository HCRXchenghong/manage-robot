package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in test exercises the real PostgreSQL metadata authority and the
// local content backend. It intentionally creates no vehicle-like runtime
// data outside the test transaction/cleanup scope.
func TestMapPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL map test")
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
	vehicleID := "map-integration-" + randHex(8)
	if _, err := db.ExecContext(ctx, `INSERT INTO vehicles(id, gateway_id, stack, vin) VALUES ($1,'gw-map-integration','test','')`, vehicleID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM maps WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM events WHERE vehicle_id=$1", vehicleID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM vehicles WHERE id=$1", vehicleID)
	})

	dir := t.TempDir()
	st := &State{db: db, hub: NewHub()}
	store, err := NewMapStore(dir, "", "", nil, st)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("# .PCD v0.7 - Point Cloud Data file format\n")
	entry, changed, err := store.Register(vehicleID, "warehouse.pcd", "manual", "integration", first)
	if err != nil || !changed {
		t.Fatalf("first map register: changed=%v err=%v", changed, err)
	}
	if len(entry.Versions) != 1 || entry.Versions[0].ContentSHA256 == "" {
		t.Fatalf("missing version manifest: %+v", entry)
	}
	if _, err := os.Stat(filepath.Join(dir, entry.ID, "v1", "warehouse.pcd")); err != nil {
		t.Fatalf("content backend did not write object: %v", err)
	}

	if _, changed, err := store.Register(vehicleID, "warehouse.pcd", "manual", "integration", first); err != nil || changed {
		t.Fatalf("same content should deduplicate: changed=%v err=%v", changed, err)
	}
	second := append(append([]byte(nil), first...), "1"[0])
	if _, changed, err := store.Register(vehicleID, "warehouse.pcd", "manual", "integration", second); err != nil || !changed {
		t.Fatalf("second map version: changed=%v err=%v", changed, err)
	}

	reloaded, err := NewMapStore(dir, "", "", nil, &State{db: db, hub: NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Get(entry.ID)
	if got == nil || len(got.Versions) != 2 || got.Versions[1].Files[0] != "warehouse.pcd" {
		t.Fatalf("database manifest was not restored: %+v", got)
	}
	if _, err := reloaded.File(entry.ID, "warehouse.pcd", 2); err != nil {
		t.Fatalf("restored version file unavailable: %v", err)
	}

	if err := reloaded.Delete(entry.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM maps WHERE id=$1", entry.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("map authority row survived delete: %d", count)
	}
}
