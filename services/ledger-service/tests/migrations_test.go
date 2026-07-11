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

// Applies services/ledger-service/migrations/ against a fresh Postgres container,
// the same way the ledger-migrate compose service does in production, and asserts
// the resulting schema, seed data, and grants match what ADR-004 expects.
func TestLedgerMigrationsApplyCleanly(t *testing.T) {
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

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
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

	for _, table := range []string{"ledger_transactions", "ledger_entries", "account_balances_locked", "outbox"} {
		var found int
		err := conn.QueryRow(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'ledger_db' AND table_name = $1",
			table).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("expected ledger_db.%s to exist, err=%v found=%d", table, err, found)
		}
	}

	var seeded int
	err = conn.QueryRow(ctx,
		"SELECT count(*) FROM ledger_db.account_balances_locked WHERE account_id = '00000000-0000-0000-0000-000000000001'",
	).Scan(&seeded)
	if err != nil || seeded != 1 {
		t.Errorf("expected system funding account to be seeded, err=%v found=%d", err, seeded)
	}

	var roleExists int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname = 'ledger_app'").Scan(&roleExists)
	if err != nil || roleExists != 1 {
		t.Errorf("expected ledger_app role to exist, err=%v found=%d", err, roleExists)
	}

	appDSN := fmt.Sprintf("postgres://ledger_app:ledger_app@localhost:%s/ledger?sslmode=disable", hostPort)
	appConn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatalf("could not connect as ledger_app: %v", err)
	}
	defer appConn.Close(ctx)

	if _, err := appConn.Exec(ctx, "UPDATE ledger_db.ledger_entries SET amount = amount"); err == nil {
		t.Error("expected ledger_app to be denied UPDATE on ledger_entries, but it succeeded")
	}
	if _, err := appConn.Exec(ctx, "DELETE FROM ledger_db.ledger_entries"); err == nil {
		t.Error("expected ledger_app to be denied DELETE on ledger_entries, but it succeeded")
	}
	if _, err := appConn.Exec(ctx, "SELECT * FROM ledger_db.ledger_entries"); err != nil {
		t.Errorf("expected ledger_app to retain SELECT on ledger_entries, got: %v", err)
	}

	// Re-running against an already-migrated database must be a clean no-op, not an error —
	// this is the exact regression ADR-004 exists to prevent.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Errorf("re-running migrations against an up-to-date schema should be a no-op, got: %v", err)
	}
}
