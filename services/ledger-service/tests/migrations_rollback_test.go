package tests

import (
	"context"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
)

// TestLedgerMigrationsDownUpRoundTrip exercises the .down.sql files ADR-004
// introduced (never run by anything else — Flyway's predecessor didn't
// require them). Down walks 0003 -> 0002 -> 0001, ending in
// DROP SCHEMA ledger_db CASCADE. Up must then reproduce the exact same
// end state, including re-hitting 0003's `IF NOT EXISTS` guard around
// CREATE ROLE ledger_app (Down revokes grants but never drops the role).
func TestLedgerMigrationsDownUpRoundTrip(t *testing.T) {
	ownerDSN, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}

	if err := m.Down(); err != nil {
		t.Fatalf("m.Down() = error %v, want nil", err)
	}

	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("could not connect for assertions: %v", err)
	}
	var schemaCount int
	err = conn.QueryRow(ctx,
		"SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'ledger_db'",
	).Scan(&schemaCount)
	conn.Close(ctx)
	if err != nil || schemaCount != 0 {
		t.Fatalf("expected ledger_db schema to be gone after Down, err=%v found=%d", err, schemaCount)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("m.Up() after Down = error %v, want nil (should redo real work, not ErrNoChange)", err)
	}

	assertLedgerSchemaHealthy(t, ctx, ownerDSN, appDSN)
}
