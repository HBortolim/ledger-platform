package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
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

	// Roll back to version 3, undoing 0004 and anything applied after it (0005).
	// Targeting the version, not a step count, keeps this correct as newer migrations land.
	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=projection_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Migrate(3); err != nil {
		t.Fatalf("rolling back to 0003: %v", err)
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
		t.Fatalf("re-applying 0004 onward: %v", err)
	}
	if _, err := app.Exec(ctx, `SELECT count(*) FROM ledger_db.ledger_entries`); err != nil {
		t.Errorf("after re-applying 0004, projection_app SELECT = %v, want nil", err)
	}
}

// TestMigration0005_GrantsLedgerAppReadOnly_AndRollsBack asserts the reconciliation job's grant:
// ledger_app may read wallet_balances and nothing else. Drift is reported by the Ledger Service
// and repaired by the rebuild (SPEC §5.2); the reporter must never be able to "fix" a row itself.
func TestMigration0005_GrantsLedgerAppReadOnly_AndRollsBack(t *testing.T) {
	ownerDSN, _ := setupProjectionDB(t) // already migrated, 0005 included
	ledgerAppDSN := strings.Replace(ownerDSN, "ledger:ledger@", "ledger_app:ledger_app@", 1)
	ledgerApp := connectPool(t, ledgerAppDSN)
	ctx := context.Background()

	if _, err := ledgerApp.Exec(ctx, `SELECT count(*) FROM projection_db.wallet_balances`); err != nil {
		t.Fatalf("ledger_app SELECT on wallet_balances = error %v, want nil", err)
	}
	for name, stmt := range map[string]string{
		"INSERT": `INSERT INTO projection_db.wallet_balances (wallet_id, balance) VALUES (gen_random_uuid(), 1.00)`,
		"UPDATE": `UPDATE projection_db.wallet_balances SET balance = 1.00`,
		"DELETE": `DELETE FROM projection_db.wallet_balances`,
	} {
		if _, err := ledgerApp.Exec(ctx, stmt); !permissionDenied(err) {
			t.Errorf("ledger_app %s on wallet_balances = %v, want permission denied (42501)", name, err)
		}
	}

	// Explicit versions, not step counts: neither test breaks when a later migration lands.
	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=projection_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Migrate(4); err != nil {
		t.Fatalf("rolling back to 0004: %v", err)
	}
	if _, err := ledgerApp.Exec(ctx, `SELECT count(*) FROM projection_db.wallet_balances`); !permissionDenied(err) {
		t.Errorf("after 0005.down, ledger_app SELECT = %v, want permission denied (42501)", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("re-applying 0005: %v", err)
	}
	if _, err := ledgerApp.Exec(ctx, `SELECT count(*) FROM projection_db.wallet_balances`); err != nil {
		t.Errorf("after re-applying 0005, ledger_app SELECT = %v, want nil", err)
	}
}

// TestMigration0006_AlignsWatermark_AndGrantsPendingEntriesNarrowly covers Task 08's projection
// migration: last_seq is backfilled from the entry each wallet last applied, projection_app gets
// exactly SELECT/INSERT/DELETE on pending_entries (parked entries are never updated in place),
// and .down/.up round-trips.
func TestMigration0006_AlignsWatermark_AndGrantsPendingEntriesNarrowly(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t) // already migrated, 0006 included
	owner := connectPool(t, ownerDSN)
	app := connectPool(t, appDSN)
	ctx := context.Background()

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=projection_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Migrate(5); err != nil {
		t.Fatalf("rolling back to 0005: %v", err)
	}

	// A wallet whose projection last applied ledger entry seq 7, and one with no entry applied.
	applied, untouched, entryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := owner.Exec(ctx, `INSERT INTO ledger_db.ledger_entries (id, transaction_id, account_id, entry_type, amount, seq)
		VALUES ($1, gen_random_uuid(), $2, 'CREDIT', 5, 7)`, entryID, applied); err != nil {
		t.Fatalf("seed ledger entry: %v", err)
	}
	for _, w := range []struct {
		id    uuid.UUID
		entry *uuid.UUID
	}{{applied, &entryID}, {untouched, nil}} {
		if _, err := owner.Exec(ctx, `INSERT INTO projection_db.wallet_balances (wallet_id, balance, last_entry_id) VALUES ($1, 5, $2)`, w.id, w.entry); err != nil {
			t.Fatalf("seed wallet: %v", err)
		}
	}

	if err := m.Up(); err != nil {
		t.Fatalf("applying 0006: %v", err)
	}
	for wallet, want := range map[uuid.UUID]int64{applied: 7, untouched: 0} {
		var got int64
		if err := owner.QueryRow(ctx, `SELECT last_seq FROM projection_db.wallet_balances WHERE wallet_id = $1`, wallet).Scan(&got); err != nil || got != want {
			t.Errorf("last_seq of %s = %d, err=%v, want %d", wallet, got, err, want)
		}
	}

	if _, err := app.Exec(ctx, `INSERT INTO projection_db.pending_entries (wallet_id, seq, entry_id, delta) VALUES ($1, 9, $2, 1)`, applied, uuid.New()); err != nil {
		t.Errorf("projection_app INSERT on pending_entries = %v, want nil", err)
	}
	if _, err := app.Exec(ctx, `SELECT count(*) FROM projection_db.pending_entries`); err != nil {
		t.Errorf("projection_app SELECT on pending_entries = %v, want nil", err)
	}
	if _, err := app.Exec(ctx, `UPDATE projection_db.pending_entries SET delta = 2`); !permissionDenied(err) {
		t.Errorf("projection_app UPDATE on pending_entries = %v, want permission denied (42501)", err)
	}
	if _, err := app.Exec(ctx, `DELETE FROM projection_db.pending_entries`); err != nil {
		t.Errorf("projection_app DELETE on pending_entries = %v, want nil", err)
	}

	if err := m.Migrate(5); err != nil {
		t.Fatalf("rolling 0006 back: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("re-applying 0006: %v", err)
	}
}
