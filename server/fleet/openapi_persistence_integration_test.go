package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// This test is opt-in because it needs a real PostgreSQL instance. It verifies
// the production invariant that API key authorization survives a Fleet
// restart; it never uses a fake database and cleans only its own generated row.
func TestOpenAPIKeyPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL integration test")
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := &State{db: db, dbCh: make(chan dbJob, 32)}
	cfg := NewConfigStore(db)
	nav := NewNavStore(st)
	open := NewOpenAPI(st, cfg, nav)

	k, _, err := open.CreateKey("integration-key", "restart proof", []string{"vehicle.read"}, []string{"vehicle-1"}, nil, "integration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api_keys WHERE id=$1", k.ID)
	}()

	reloaded := NewOpenAPI(st, cfg, nav)
	got := findAPIKey(reloaded.ListKeys(), k.ID)
	if got == nil || got.Name != "integration-key" || len(got.Scopes) != 1 || got.Scopes[0] != "vehicle.read" {
		t.Fatalf("created key was not restored after reload: %+v", got)
	}
	if got.ExpiresNS <= time.Now().UnixNano() {
		t.Fatalf("created key expiry was not restored: %d", got.ExpiresNS)
	}
	durableAudit := reloaded.Audit(AuditFilter{Limit: 20, KeyID: k.ID})
	if len(durableAudit) == 0 || durableAudit[0].Path != "/api/openkeys" || durableAudit[0].Result != "ok" {
		t.Fatalf("API audit evidence was not restored: %+v", durableAudit)
	}
	updatedName := "integration-key-updated"
	if _, err := reloaded.UpdateKey(k.ID, KeyPatch{Name: &updatedName}, "integration"); err != nil {
		t.Fatal(err)
	}
	updated := NewOpenAPI(st, cfg, nav)
	if got := findAPIKey(updated.ListKeys(), k.ID); got == nil || got.Name != updatedName {
		t.Fatalf("updated key was not restored: %+v", got)
	}
	if err := updated.Revoke(k.ID, "integration"); err != nil {
		t.Fatal(err)
	}
	revoked := NewOpenAPI(st, cfg, nav)
	if got := findAPIKey(revoked.ListKeys(), k.ID); got == nil || got.RevokedNS == 0 {
		t.Fatalf("revoked key state was not restored: %+v", got)
	}
}

func findAPIKey(keys []*APIKey, id string) *APIKey {
	for _, k := range keys {
		if k.ID == id {
			return k
		}
	}
	return nil
}
