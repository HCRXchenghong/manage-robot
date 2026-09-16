package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestAuditAppendOnlyIntegration verifies the database boundary itself rather
// than relying on application code to remember not to mutate evidence rows.
func TestAuditAppendOnlyIntegration(t *testing.T) {
	dsn := os.Getenv("ROBOT_AGENT_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set ROBOT_AGENT_INTEGRATION_DSN to run the PostgreSQL audit immutability test")
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
	for _, table := range []string{"audit_log", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if table == "audit_log" {
				_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(key_id, method, path, result, http_code)
					VALUES ('append-only-test','GET','/integration','ok',200)`)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(actor, action, detail)
					VALUES ('append-only-test','integration','{}')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+map[string]string{
				"audit_log":  "key_id='append-only-test'",
				"audit_logs": "actor='append-only-test'",
			}[table]); err == nil {
				t.Fatalf("%s accepted DELETE of audit evidence", table)
			}
		})
	}
}
