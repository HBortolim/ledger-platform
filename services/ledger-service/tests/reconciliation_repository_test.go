package tests

import (
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

// seedProjectionSchema creates projection_db.wallet_balances and emulates the grant that
// projection-service/migrations/0005_ledger_app_read_grants gives ledger_app. The DDL is a copy
// of projection-service/migrations/0001_projection_schema.up.sql (the authoritative one); that
// the grant is read-only is asserted by the projection service's own migration tests.
func seedProjectionSchema(t *testing.T, ownerDSN string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, stmt := range []string{
		`CREATE SCHEMA IF NOT EXISTS projection_db`,
		`CREATE TABLE IF NOT EXISTS projection_db.wallet_balances (
			wallet_id       UUID PRIMARY KEY,
			balance         NUMERIC(19, 2) NOT NULL DEFAULT 0,
			last_entry_id   UUID,
			last_applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`GRANT USAGE ON SCHEMA projection_db TO ledger_app`,
		`GRANT SELECT ON projection_db.wallet_balances TO ledger_app`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed projection schema: %v\n%s", err, stmt)
		}
	}
}

// reconFixture owns an owner connection used to build states the app role cannot.
type reconFixture struct {
	t     *testing.T
	owner *pgxpool.Pool
	app   *pgxpool.Pool
	repo  *repository.ReconciliationRepository
}

func newReconFixture(t *testing.T) (*reconFixture, string, string) {
	t.Helper()
	ownerDSN, appDSN := setupLedgerDB(t)
	seedProjectionSchema(t, ownerDSN)
	ctx := context.Background()

	owner, err := pgxpool.New(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	t.Cleanup(owner.Close)
	app, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)

	return &reconFixture{t: t, owner: owner, app: app, repo: repository.NewReconciliationRepository(app)}, ownerDSN, appDSN
}

func (f *reconFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.owner.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("fixture exec: %v\n%s", err, sql)
	}
}

// reset empties every table the checks read, back to the migrated state (system balance 0).
func (f *reconFixture) reset() {
	f.t.Helper()
	f.exec(`TRUNCATE ledger_db.ledger_entries, ledger_db.ledger_transactions, ledger_db.outbox CASCADE`)
	f.exec(`DELETE FROM ledger_db.account_balances_locked WHERE account_id <> $1`, systemFundingAccountID)
	f.exec(`UPDATE ledger_db.account_balances_locked SET balance = 0`)
	f.exec(`DELETE FROM projection_db.wallet_balances`)
}

// header commits a transaction header on its own, which the deferred balance trigger lets
// through (zero entries net to zero). ago is a Postgres interval, e.g. '1 hour'.
func (f *reconFixture) header(id uuid.UUID, ago string) {
	f.t.Helper()
	f.exec(`INSERT INTO ledger_db.ledger_transactions(id, type, created_at) VALUES ($1, 'TRANSFER', now() - $2::interval)`, id, ago)
}

// entry inserts one ledger entry in its own statement, after the header, so the trigger
// (which fires on header inserts only) never sees it.
func (f *reconFixture) entry(txID, account uuid.UUID, entryType, amount, ago string) {
	f.t.Helper()
	f.exec(`INSERT INTO ledger_db.ledger_entries(id, transaction_id, account_id, entry_type, amount, seq, created_at)
	        VALUES ($1, $2, $3, $4, $5::numeric,
	                (SELECT COALESCE(MAX(seq), 0) + 1 FROM ledger_db.ledger_entries WHERE account_id = $3),
	                now() - $6::interval)`, uuid.New(), txID, account, entryType, amount, ago)
}

// balancedTransfer commits a balanced debit/credit pair created `ago` in the past.
func (f *reconFixture) balancedTransfer(debit, credit uuid.UUID, amount, ago string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`WITH h AS (INSERT INTO ledger_db.ledger_transactions(id, type, created_at) VALUES ($1, 'TRANSFER', now() - $5::interval))
	        INSERT INTO ledger_db.ledger_entries(id, transaction_id, account_id, entry_type, amount, seq, created_at) VALUES
	          (gen_random_uuid(), $1, $2, 'DEBIT',  $4::numeric, (SELECT COALESCE(MAX(seq), 0) + 1 FROM ledger_db.ledger_entries WHERE account_id = $2), now() - $5::interval),
	          (gen_random_uuid(), $1, $3, 'CREDIT', $4::numeric, (SELECT COALESCE(MAX(seq), 0) + 1 FROM ledger_db.ledger_entries WHERE account_id = $3), now() - $5::interval)`, id, debit, credit, amount, ago)
	return id
}

func (f *reconFixture) project(wallet uuid.UUID, balance string) {
	f.t.Helper()
	f.exec(`INSERT INTO projection_db.wallet_balances(wallet_id, balance) VALUES ($1, $2::numeric)
	        ON CONFLICT (wallet_id) DO UPDATE SET balance = EXCLUDED.balance`, wallet, balance)
}

func (f *reconFixture) setCached(account uuid.UUID, balance string) {
	f.t.Helper()
	f.exec(`INSERT INTO ledger_db.account_balances_locked(account_id, balance) VALUES ($1, $2::numeric)
	        ON CONFLICT (account_id) DO UPDATE SET balance = EXCLUDED.balance`, account, balance)
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestReconciliationRepository_Checks(t *testing.T) {
	f, _, _ := newReconFixture(t)
	ctx := context.Background()
	const hour = time.Hour

	t.Run("Clean/PostedSequenceIsCleanEverywhere", func(t *testing.T) {
		f.reset()
		posting := repository.NewPostingRepository(f.app, testDailyCap, systemFundingAccountID)
		a, b := uuid.New(), uuid.New()
		for _, tx := range []domain.LedgerTransaction{
			newBalancedTransfer(systemFundingAccountID, a, "100.00"), // deposit
			newBalancedTransfer(a, b, "40.00"),
		} {
			if err := posting.Post(ctx, tx); err != nil {
				t.Fatalf("Post() = %v", err)
			}
		}
		f.project(a, "60.00")
		f.project(b, "40.00")
		f.project(systemFundingAccountID, "-100.00")

		inv, err := f.repo.UnbalancedTransactions(ctx, hour, 50)
		if err != nil || !inv.Clean() {
			t.Errorf("invariant = %+v, err=%v, want clean", inv, err)
		}
		drift, err := f.repo.ProjectionDrift(ctx, 0, 50)
		if err != nil || !drift.Clean() {
			t.Errorf("drift = %+v, err=%v, want clean (system account has a matching row and is not excluded)", drift, err)
		}
		supply, err := f.repo.MoneySupply(ctx)
		if err != nil {
			t.Fatalf("MoneySupply() = %v", err)
		}
		if !supply.LedgerTotal.IsZero() || !supply.CachedTotal.IsZero() {
			t.Errorf("supply = %+v, want exactly zero both", supply)
		}
	})

	t.Run("Invariant/UnbalancedAndEntryless", func(t *testing.T) {
		f.reset()
		a := uuid.New()
		unbalanced, entryless, old := uuid.New(), uuid.New(), uuid.New()
		f.header(unbalanced, "1 minute")
		f.entry(unbalanced, a, "CREDIT", "40.00", "1 minute")
		f.header(entryless, "30 seconds")
		f.header(old, "3 hours") // outside the 1h window
		f.entry(old, a, "CREDIT", "7.00", "3 hours")

		got, err := f.repo.UnbalancedTransactions(ctx, hour, 50)
		if err != nil {
			t.Fatalf("UnbalancedTransactions() = %v", err)
		}
		if got.Total != 2 || len(got.Sample) != 2 || got.Truncated() {
			t.Fatalf("got %+v, want exactly the unbalanced and the entry-less transaction", got)
		}
		// ordered by created_at: the unbalanced one is older
		if g := got.Sample[0]; g.TransactionID != unbalanced || !g.Net.Equal(dec("40")) || g.EntryCount != 1 {
			t.Errorf("first finding = %+v", g)
		}
		if g := got.Sample[1]; g.TransactionID != entryless || !g.Net.IsZero() || g.EntryCount != 0 {
			t.Errorf("second finding = %+v, want EntryCount 0", g)
		}
	})

	t.Run("Invariant/TotalIsTrueCountUnderLimit", func(t *testing.T) {
		f.reset()
		for i := 0; i < 5; i++ {
			f.header(uuid.New(), "1 minute")
		}
		got, err := f.repo.UnbalancedTransactions(ctx, hour, 2)
		if err != nil {
			t.Fatalf("UnbalancedTransactions() = %v", err)
		}
		if got.Total != 5 || len(got.Sample) != 2 || !got.Truncated() {
			t.Errorf("got Total=%d len=%d truncated=%v, want 5/2/true", got.Total, len(got.Sample), got.Truncated())
		}
	})

	t.Run("Drift/MismatchMissingOrphan", func(t *testing.T) {
		f.reset()
		mismatch, missing, orphan, healthy := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		counter := uuid.New()
		f.balancedTransfer(counter, mismatch, "50.00", "1 hour")
		f.balancedTransfer(counter, missing, "20.00", "1 hour")
		f.balancedTransfer(counter, healthy, "10.00", "1 hour")
		f.project(mismatch, "999.00")
		f.project(orphan, "5.00") // no ledger entries at all
		f.project(healthy, "10.00")
		f.project(counter, "-80.00")

		got, err := f.repo.ProjectionDrift(ctx, 10*time.Second, 50)
		if err != nil {
			t.Fatalf("ProjectionDrift() = %v", err)
		}
		if got.Total != 3 || len(got.Sample) != 3 {
			t.Fatalf("got %+v, want mismatch, missing and orphan", got)
		}
		byWallet := map[uuid.UUID]domain.ProjectionDrift{}
		for _, d := range got.Sample {
			byWallet[d.WalletID] = d
		}
		if d := byWallet[mismatch]; d.Kind != domain.DriftMismatch || !d.Projected.Equal(dec("999")) || !d.Actual.Equal(dec("50")) {
			t.Errorf("mismatch = %+v, want both numbers", d)
		}
		if d := byWallet[missing]; d.Kind != domain.DriftProjectionMissing || !d.Projected.IsZero() || !d.Actual.Equal(dec("20")) {
			t.Errorf("missing = %+v", d)
		}
		if d := byWallet[orphan]; d.Kind != domain.DriftProjectionOrphan || !d.Projected.Equal(dec("5")) || !d.Actual.IsZero() {
			t.Errorf("orphan = %+v (a bare grace clause drops this one)", d)
		}
		for i := 1; i < len(got.Sample); i++ {
			if got.Sample[i-1].WalletID.String() > got.Sample[i].WalletID.String() {
				t.Error("findings are not ordered by wallet id")
			}
		}
	})

	t.Run("Drift/GraceHidesMidFlightEntries", func(t *testing.T) {
		f.reset()
		w, counter := uuid.New(), uuid.New()
		f.balancedTransfer(counter, w, "50.00", "1 second")
		f.project(counter, "-50.00") // only w lags

		got, err := f.repo.ProjectionDrift(ctx, 10*time.Second, 50)
		if err != nil || !got.Clean() {
			t.Errorf("with grace: %+v err=%v, want clean (mid-flight, not drift)", got, err)
		}
		got, err = f.repo.ProjectionDrift(ctx, 0, 50)
		if err != nil || got.Total != 1 || got.Sample[0].WalletID != w {
			t.Errorf("grace 0: %+v err=%v, want w reported", got, err)
		}
	})

	t.Run("Drift/TotalIsTrueCountUnderLimit", func(t *testing.T) {
		f.reset()
		for i := 0; i < 4; i++ {
			f.project(uuid.New(), "1.00") // four orphans
		}
		got, err := f.repo.ProjectionDrift(ctx, 10*time.Second, 3)
		if err != nil {
			t.Fatalf("ProjectionDrift() = %v", err)
		}
		if got.Total != 4 || len(got.Sample) != 3 || !got.Truncated() {
			t.Errorf("got Total=%d len=%d, want 4/3", got.Total, len(got.Sample))
		}
	})

	t.Run("Drift/SystemAccountIsNotExcluded", func(t *testing.T) {
		f.reset()
		w := uuid.New()
		f.balancedTransfer(systemFundingAccountID, w, "100.00", "1 hour")
		f.project(w, "100.00")
		f.project(systemFundingAccountID, "-100.00")
		if got, err := f.repo.ProjectionDrift(ctx, 10*time.Second, 50); err != nil || !got.Clean() {
			t.Fatalf("matching system row: %+v err=%v, want clean", got, err)
		}

		f.project(systemFundingAccountID, "123.00")
		got, err := f.repo.ProjectionDrift(ctx, 10*time.Second, 50)
		if err != nil || got.Total != 1 || got.Sample[0].WalletID != systemFundingAccountID {
			t.Errorf("corrupt system row: %+v err=%v, want it reported", got, err)
		}
	})

	t.Run("MoneySupply/TotalsAreIndependent", func(t *testing.T) {
		f.reset()
		w := uuid.New()
		f.balancedTransfer(systemFundingAccountID, w, "100.00", "1 hour")

		f.setCached(w, "100.00") // cache +100 with no matching cache debit on the system account
		got, err := f.repo.MoneySupply(ctx)
		if err != nil {
			t.Fatalf("MoneySupply() = %v", err)
		}
		if !got.LedgerClean() || got.CachedClean() || !got.CachedTotal.Equal(dec("100")) {
			t.Errorf("got %+v, want cached unclean while the ledger stays clean", got)
		}

		f.entry(f.mustTx(), w, "CREDIT", "9.00", "1 minute")
		got, err = f.repo.MoneySupply(ctx)
		if err != nil || got.LedgerClean() || !got.LedgerTotal.Equal(dec("9")) {
			t.Errorf("got %+v err=%v, want ledger total 9", got, err)
		}
	})
}

// mustTx commits an entry-less header and returns its id.
func (f *reconFixture) mustTx() uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.header(id, "1 minute")
	return id
}

func TestReconciliationRepository_PassLock(t *testing.T) {
	f, _, appDSN := newReconFixture(t)
	ctx := context.Background()

	// A second pool stands for a second replica.
	pool2, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pool2: %v", err)
	}
	t.Cleanup(pool2.Close)
	repo2 := repository.NewReconciliationRepository(pool2)

	advisoryLocks := func() int {
		t.Helper()
		var n int
		if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`).Scan(&n); err != nil {
			t.Fatalf("count advisory locks: %v", err)
		}
		return n
	}

	t.Run("SecondReplicaIsRefusedUntilRelease", func(t *testing.T) {
		release, ok, err := f.repo.AcquirePassLock(ctx)
		if err != nil || !ok {
			t.Fatalf("first acquire = ok:%v err:%v", ok, err)
		}
		rel2, ok2, err := repo2.AcquirePassLock(ctx)
		if err != nil || ok2 || rel2 != nil {
			t.Fatalf("second acquire = release-nil:%v ok:%v err:%v, want refused", rel2 == nil, ok2, err)
		}
		release()

		rel3, ok3, err := repo2.AcquirePassLock(ctx)
		if err != nil || !ok3 {
			t.Fatalf("acquire after release = ok:%v err:%v", ok3, err)
		}
		rel3()
		if n := advisoryLocks(); n != 0 {
			t.Errorf("%d advisory locks left after release; a locked connection leaked into the pool", n)
		}
	})

	t.Run("ReleaseSurvivesADeadContext", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		release, ok, err := f.repo.AcquirePassLock(cctx)
		if err != nil || !ok {
			t.Fatalf("acquire = ok:%v err:%v", ok, err)
		}
		cancel()
		release()

		rel, ok, err := repo2.AcquirePassLock(ctx)
		if err != nil || !ok {
			t.Fatalf("acquire after cancelled release = ok:%v err:%v, want the lock free", ok, err)
		}
		rel()
	})

	t.Run("CrashedHolderFreesTheLock_AndReleaseDiscardsTheDeadConnection", func(t *testing.T) {
		crashPool, err := pgxpool.New(ctx, appDSN)
		if err != nil {
			t.Fatalf("crash pool: %v", err)
		}
		release, ok, err := repository.NewReconciliationRepository(crashPool).AcquirePassLock(ctx)
		if err != nil || !ok {
			t.Fatalf("acquire = ok:%v err:%v", ok, err)
		}
		// Kill the holder's session: Postgres must drop the lock without any release.
		if _, err := f.owner.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype = 'advisory'`); err != nil {
			t.Fatalf("terminate backend: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			rel, ok, err := repo2.AcquirePassLock(ctx)
			if err == nil && ok {
				rel()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("lock still held after the holder died: ok:%v err:%v", ok, err)
			}
			time.Sleep(50 * time.Millisecond)
		}

		// The late release now fails its unlock on a dead connection. It must not panic or hang, and
		// the pool must still close. (pgxpool discards a closed connection on Release anyway; the
		// Hijack branch in release is for a live connection whose unlock returns false.)
		release()
		closed := make(chan struct{})
		go func() { crashPool.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("pool.Close blocked after a failed release")
		}
	})
}

func TestMigration0005_ZeroesSystemSeedOnlyWhenUntouched(t *testing.T) {
	const migrationsURL = "file://../migrations"
	const table = "&x-migrations-table=ledger_schema_migrations"

	t.Run("FreshDatabase", func(t *testing.T) {
		ownerDSN, _ := setupLedgerDB(t)
		if got := systemCachedBalance(t, ownerDSN); !got.IsZero() {
			t.Errorf("fresh system balance = %s, want 0", got)
		}
	})

	t.Run("ExistingHistoryIsNotStomped", func(t *testing.T) {
		ownerDSN, _ := setupLedgerDB(t)
		ctx := context.Background()
		m, err := migrate.New(migrationsURL, ownerDSN+table)
		if err != nil {
			t.Fatalf("migrate.New: %v", err)
		}
		if err := m.Migrate(4); err != nil { // roll 0006 and 0005 back (no seq column yet); untouched seed returns to 1000
			t.Fatalf("Migrate(4) = %v", err)
		}
		if got := systemCachedBalance(t, ownerDSN); !got.Equal(dec("1000")) {
			t.Fatalf("after down, system balance = %s, want 1000 restored under the guard", got)
		}

		// A deposit has since happened: the cached balance now mirrors real history.
		conn, err := pgx.Connect(ctx, ownerDSN)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		txID, other := uuid.New(), uuid.New()
		if _, err := conn.Exec(ctx, `WITH h AS (INSERT INTO ledger_db.ledger_transactions(id, type) VALUES ($1, 'DEPOSIT'))
			INSERT INTO ledger_db.ledger_entries(id, transaction_id, account_id, entry_type, amount) VALUES
			(gen_random_uuid(), $1, $2, 'DEBIT', 25), (gen_random_uuid(), $1, $3, 'CREDIT', 25)`,
			txID, systemFundingAccountID, other); err != nil {
			t.Fatalf("seed history: %v", err)
		}
		if _, err := conn.Exec(ctx, `UPDATE ledger_db.account_balances_locked SET balance = 975 WHERE account_id = $1`, systemFundingAccountID); err != nil {
			t.Fatalf("set cached balance: %v", err)
		}

		if err := m.Up(); err != nil {
			t.Fatalf("Up() = %v", err)
		}
		if got := systemCachedBalance(t, ownerDSN); !got.Equal(dec("975")) {
			t.Errorf("system balance = %s after 0005, want 975 untouched", got)
		}
	})
}

func systemCachedBalance(t *testing.T, ownerDSN string) decimal.Decimal {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var s string
	if err := conn.QueryRow(ctx, `SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1`, systemFundingAccountID).Scan(&s); err != nil {
		t.Fatalf("read system balance: %v", err)
	}
	return dec(s)
}
