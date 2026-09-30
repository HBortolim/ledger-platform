package tests

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		defer func() { _ = conn.Close(ctx) }()
		return conn.Ping(ctx)
	}); err != nil {
		t.Fatalf("postgres never became ready: %v", err)
	}

	// 0004 grants on ledger_db, which the Ledger Service's migrations own; see seedLedgerSchema.
	seedLedgerSchema(t, ownerDSN)

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
	defer func() { _ = conn.Close(ctx) }()

	for _, table := range []string{"wallet_balances", "projection_offsets"} {
		var found int
		err := conn.QueryRow(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'projection_db' AND table_name = $1",
			table).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("expected projection_db.%s to exist, err=%v found=%d", table, err, found)
		}
	}

	var roleExists int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname = 'projection_app'").Scan(&roleExists)
	if err != nil || roleExists != 1 {
		t.Errorf("expected projection_app role to exist, err=%v found=%d", err, roleExists)
	}

	appDSN := fmt.Sprintf("postgres://projection_app:projection_app@localhost:%s/ledger?sslmode=disable", hostPort)
	appConn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatalf("could not connect as projection_app: %v", err)
	}
	defer func() { _ = appConn.Close(ctx) }()

	if _, err := appConn.Exec(ctx, "DELETE FROM projection_db.wallet_balances"); err != nil {
		t.Errorf("expected projection_app to retain DELETE on wallet_balances (rebuild flow), got: %v", err)
	}
	if _, err := appConn.Exec(ctx, "DELETE FROM projection_db.projection_offsets"); err == nil {
		t.Error("expected projection_app to be denied DELETE on projection_offsets, but it succeeded")
	}

	// Re-running against an already-migrated database must be a clean no-op, not an error —
	// this is the exact regression ADR-004 exists to prevent.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Errorf("re-running migrations against an up-to-date schema should be a no-op, got: %v", err)
	}
}

// permissionDenied reports whether err is Postgres's insufficient_privilege (42501).
// Matching the SQLSTATE, not the message text, so a typo'd table name (42P01) can
// never masquerade as a passing "denied" assertion.
func permissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

// TestMigration0004_GrantsLedgerReadOnly_AndRollsBack asserts the Rebuild grant is exactly
// what NFR-AUDIT-1 allows -- read-only on ledger_entries -- and that its .down.sql takes
// it away again.
func TestMigration0004_GrantsLedgerReadOnly_AndRollsBack(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t) // already migrated, 0004 included
	app := connectPool(t, appDSN)
	ctx := context.Background()

	if _, err := app.Exec(ctx, `SELECT count(*) FROM ledger_db.ledger_entries`); err != nil {
		t.Fatalf("projection_app SELECT on ledger_db.ledger_entries = error %v, want nil", err)
	}

	// The ledger is append-only and owned by the Ledger Service: the projection may read
	// it and must not be able to change a single row (NFR-AUDIT-1).
	for name, stmt := range map[string]string{
		"INSERT": `INSERT INTO ledger_db.ledger_entries (id, transaction_id, account_id, entry_type, amount)
		           VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 'CREDIT', 1.00)`,
		"UPDATE": `UPDATE ledger_db.ledger_entries SET amount = 1.00`,
		"DELETE": `DELETE FROM ledger_db.ledger_entries`,
	} {
		if _, err := app.Exec(ctx, stmt); !permissionDenied(err) {
			t.Errorf("projection_app %s on ledger_db.ledger_entries = %v, want permission denied (42501)", name, err)
		}
	}

	// Roll back exactly one step: 0004 is the newest migration.
	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=projection_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("rolling back 0004: %v", err)
	}

	// Postgres checks privileges per statement, so the same pool sees the revoke immediately.
	if _, err := app.Exec(ctx, `SELECT count(*) FROM ledger_db.ledger_entries`); !permissionDenied(err) {
		t.Errorf("after 0004.down, projection_app SELECT = %v, want permission denied (42501)", err)
	}
	// ...and the earlier migrations' grants are untouched.
	if _, err := app.Exec(ctx, `DELETE FROM projection_db.wallet_balances`); err != nil {
		t.Errorf("after 0004.down, projection_app DELETE on wallet_balances = %v, want nil", err)
	}

	// Up again must restore it (the .down is a true inverse).
	if err := m.Up(); err != nil {
		t.Fatalf("re-applying 0004: %v", err)
	}
	if _, err := app.Exec(ctx, `SELECT count(*) FROM ledger_db.ledger_entries`); err != nil {
		t.Errorf("after re-applying 0004, projection_app SELECT = %v, want nil", err)
	}
}
