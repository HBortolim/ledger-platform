package tests

import (
	"context"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// Applies services/ledger-service/migrations/ against a fresh Postgres container,
// the same way the ledger-migrate compose service does in production, and asserts
// the resulting schema, seed data, grants, and privileges match what ADR-004 expects.
func TestLedgerMigrationsApplyCleanly(t *testing.T) {
	ownerDSN, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	assertLedgerSchemaHealthy(t, ctx, ownerDSN, appDSN)

	// Re-running against an already-migrated database must be a clean no-op, not an error —
	// this is the exact regression ADR-004 exists to prevent.
	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Errorf("re-running migrations against an up-to-date schema should be a no-op, got: %v", err)
	}
}
