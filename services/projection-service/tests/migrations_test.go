package tests

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/ory/dockertest/v3"
)

// Applies services/projection-service/migrations/ against a fresh Postgres container,
// the same way the projection-migrate compose service does in production, and asserts
// the resulting schema matches what ADR-004 expects.
func TestProjectionMigrationsApplyCleanly(t *testing.T) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("could not connect to docker: %v", err)
	}

	resource, err := pool.Run("postgres", "16-alpine", []string{
		"POSTGRES_USER=ledger",
		"POSTGRES_PASSWORD=ledger",
		"POSTGRES_DB=ledger",
	})
	if err != nil {
		t.Fatalf("could not start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Purge(resource); err != nil {
			t.Logf("could not purge postgres container: %v", err)
		}
	})

	hostPort := resource.GetPort("5432/tcp")
	ownerDSN := fmt.Sprintf("postgres://ledger:ledger@localhost:%s/ledger?sslmode=disable", hostPort)

	ctx := context.Background()
	if err := pool.Retry(func() error {
		conn, err := pgx.Connect(ctx, ownerDSN)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		return conn.Ping(ctx)
	}); err != nil {
		t.Fatalf("postgres never became ready: %v", err)
	}

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=projection_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrations failed to apply: %v", err)
	}

	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("could not connect for assertions: %v", err)
	}
	defer conn.Close(ctx)

	for _, table := range []string{"wallet_balances", "projection_offsets"} {
		var found int
		err := conn.QueryRow(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'projection_db' AND table_name = $1",
			table).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("expected projection_db.%s to exist, err=%v found=%d", table, err, found)
		}
	}

	// Re-running against an already-migrated database must be a clean no-op, not an error —
	// this is the exact regression ADR-004 exists to prevent.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Errorf("re-running migrations against an up-to-date schema should be a no-op, got: %v", err)
	}
}
