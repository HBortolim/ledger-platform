package tests

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/consumer"
	"github.com/ledger-platform/projection-service/internal/repository"
)

// insertLedgerEntry writes one ledger_db.ledger_entries row as the owner role -- the way the
// Ledger Service would have -- and returns its id. createdAt is explicit so a test controls
// the replay order rather than depending on the insertion clock.
func insertLedgerEntry(t *testing.T, owner *pgxpool.Pool, accountID uuid.UUID, entryType, amount string, createdAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	insertLedgerEntryWithID(t, owner, id, accountID, entryType, amount, createdAt)
	return id
}

func insertLedgerEntryWithID(t *testing.T, owner *pgxpool.Pool, id, accountID uuid.UUID, entryType, amount string, createdAt time.Time) {
	t.Helper()
	_, err := owner.Exec(context.Background(), `
		INSERT INTO ledger_db.ledger_entries (id, transaction_id, account_id, entry_type, amount, created_at)
		VALUES ($1, $2, $3, $4, $5::numeric, $6)`,
		id, uuid.New(), accountID, entryType, amount, createdAt)
	if err != nil {
		t.Fatalf("insert ledger entry: %v", err)
	}
}

// seedWalletBalance plants a projection row directly, standing in for a corrupted (or stale) one.
func seedWalletBalance(t *testing.T, owner *pgxpool.Pool, walletID uuid.UUID, balance string) {
	t.Helper()
	_, err := owner.Exec(context.Background(),
		`INSERT INTO projection_db.wallet_balances (wallet_id, balance) VALUES ($1, $2::numeric)`,
		walletID, balance)
	if err != nil {
		t.Fatalf("seed wallet balance: %v", err)
	}
}

// lastEntryID returns wallet_balances.last_entry_id, or nil when the column is NULL.
func lastEntryID(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) *uuid.UUID {
	t.Helper()
	var id *uuid.UUID
	err := pool.QueryRow(context.Background(),
		`SELECT last_entry_id FROM projection_db.wallet_balances WHERE wallet_id = $1`, walletID).Scan(&id)
	if err != nil {
		t.Fatalf("read last_entry_id for %s: %v", walletID, err)
	}
	return id
}

func equalUUIDPtr(got *uuid.UUID, want uuid.UUID) bool {
	return got != nil && *got == want
}

// TestRebuild is the mechanism behind SPEC.md §9.7 / TST-E2E-6's unit-level twin. One Postgres
// container is shared by the subtests; each uses fresh random wallet ids, so rows never collide.
//
// Rebuild runs as projection_app -- the role the service really runs as -- so every subtest also
// proves migration 0004's SELECT grant is sufficient.
func TestRebuild(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t)
	owner := connectPool(t, ownerDSN)
	app := connectPool(t, appDSN)
	repo := repository.NewRebuildRepository(app)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	t.Run("credits and debits net to a signed balance", func(t *testing.T) {
		wallet := uuid.New()
		insertLedgerEntry(t, owner, wallet, "CREDIT", "500.00", base)
		insertLedgerEntry(t, owner, wallet, "DEBIT", "200.00", base.Add(time.Second))

		got, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got.WalletID != wallet {
			t.Errorf("Result.WalletID = %s, want %s", got.WalletID, wallet)
		}
		if got.Balance.StringFixed(2) != "300.00" {
			t.Errorf("Result.Balance = %s, want 300.00", got.Balance.StringFixed(2))
		}
		if got.EntriesApplied != 2 {
			t.Errorf("Result.EntriesApplied = %d, want 2", got.EntriesApplied)
		}
		// RebuiltAt comes from Postgres now() (§9.10): assert it is a plausible "just now", not zero.
		if d := time.Since(got.RebuiltAt); d < -time.Minute || d > time.Minute {
			t.Errorf("Result.RebuiltAt = %s, want within a minute of now", got.RebuiltAt)
		}
		if b := walletBalance(t, app, wallet); b != "300.00" {
			t.Errorf("persisted balance = %s, want 300.00", b)
		}
	})

	t.Run("last_entry_id is the chronologically last entry, not the last inserted", func(t *testing.T) {
		wallet := uuid.New()
		// Insert the LATER entry first. Without ORDER BY created_at a sequential scan returns
		// insertion order and would pick the earlier entry as "last".
		later := insertLedgerEntry(t, owner, wallet, "CREDIT", "10.00", base.Add(time.Minute))
		insertLedgerEntry(t, owner, wallet, "CREDIT", "5.00", base)

		if _, err := repo.Rebuild(ctx, wallet); err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got := lastEntryID(t, app, wallet); !equalUUIDPtr(got, later) {
			t.Errorf("last_entry_id = %v, want %s (the entry with the later created_at)", got, later)
		}
	})

	t.Run("entries sharing a timestamp are ordered by id", func(t *testing.T) {
		wallet := uuid.New()
		// Several entries share a created_at inside one posting. (created_at, id) makes the
		// replay -- and therefore last_entry_id -- deterministic. Postgres orders uuid bytewise.
		a, b := uuid.New(), uuid.New()
		if bytes.Compare(a[:], b[:]) > 0 {
			a, b = b, a // a < b
		}
		// Insert the greater id FIRST, so insertion order and id order disagree.
		insertLedgerEntryWithID(t, owner, b, wallet, "CREDIT", "1.00", base)
		insertLedgerEntryWithID(t, owner, a, wallet, "CREDIT", "1.00", base)

		if _, err := repo.Rebuild(ctx, wallet); err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got := lastEntryID(t, app, wallet); !equalUUIDPtr(got, b) {
			t.Errorf("last_entry_id = %v, want %s (greater id wins the tie)", got, b)
		}
	})

	// TST-E2E-6's unit-level twin: a wrong pre-existing row must be REPLACED, not merged.
	t.Run("a corrupted row is replaced with the ledger-derived value", func(t *testing.T) {
		wallet := uuid.New()
		insertLedgerEntry(t, owner, wallet, "CREDIT", "500.00", base)
		seedWalletBalance(t, owner, wallet, "999999.00")

		got, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got.Balance.StringFixed(2) != "500.00" {
			t.Errorf("Result.Balance = %s, want 500.00", got.Balance.StringFixed(2))
		}
		if b := walletBalance(t, app, wallet); b != "500.00" {
			t.Errorf("persisted balance = %s, want 500.00 (corrupted 999999.00 must not survive)", b)
		}
	})

	t.Run("a wallet with no entries gets a zero row, not a missing one", func(t *testing.T) {
		wallet := uuid.New()

		got, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got.EntriesApplied != 0 || !got.Balance.IsZero() {
			t.Errorf("Result = {entries: %d, balance: %s}, want {0, 0.00}", got.EntriesApplied, got.Balance)
		}
		if b := walletBalance(t, app, wallet); b != "0.00" {
			t.Errorf("persisted balance = %s, want 0.00 (a row, not %q)", b, "<no row>")
		}
		if id := lastEntryID(t, app, wallet); id != nil {
			t.Errorf("last_entry_id = %s, want NULL for a wallet with no entries", id)
		}
	})

	t.Run("a wallet with only debits rebuilds to a negative balance", func(t *testing.T) {
		wallet := uuid.New()
		insertLedgerEntry(t, owner, wallet, "DEBIT", "10.00", base)

		got, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got.Balance.StringFixed(2) != "-10.00" {
			t.Errorf("Result.Balance = %s, want -10.00", got.Balance.StringFixed(2))
		}
		if b := walletBalance(t, app, wallet); b != "-10.00" {
			t.Errorf("persisted balance = %s, want -10.00", b)
		}
	})

	t.Run("other accounts' entries are ignored", func(t *testing.T) {
		wallet, other := uuid.New(), uuid.New()
		insertLedgerEntry(t, owner, wallet, "CREDIT", "10.00", base)
		insertLedgerEntry(t, owner, other, "CREDIT", "999.00", base)

		got, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if got.Balance.StringFixed(2) != "10.00" || got.EntriesApplied != 1 {
			t.Errorf("Result = {balance: %s, entries: %d}, want {10.00, 1}", got.Balance.StringFixed(2), got.EntriesApplied)
		}
	})

	t.Run("rebuilding one wallet leaves every other row alone", func(t *testing.T) {
		wallet, bystander := uuid.New(), uuid.New()
		insertLedgerEntry(t, owner, wallet, "CREDIT", "10.00", base)
		// The bystander's row is deliberately "wrong"; it must survive untouched, because the
		// rebuild endpoint is per-wallet by design (§9.7) -- there is no all-wallets variant.
		seedWalletBalance(t, owner, bystander, "42.00")

		if _, err := repo.Rebuild(ctx, wallet); err != nil {
			t.Fatalf("Rebuild() = error %v, want nil", err)
		}

		if b := walletBalance(t, app, bystander); b != "42.00" {
			t.Errorf("bystander balance = %s, want 42.00 (untouched)", b)
		}
	})

	t.Run("rebuilding twice gives the same result", func(t *testing.T) {
		wallet := uuid.New()
		insertLedgerEntry(t, owner, wallet, "CREDIT", "500.00", base)
		insertLedgerEntry(t, owner, wallet, "DEBIT", "200.00", base.Add(time.Second))

		first, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() #1 = error %v, want nil", err)
		}
		second, err := repo.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() #2 = error %v, want nil", err)
		}

		if !first.Balance.Equal(second.Balance) || first.EntriesApplied != second.EntriesApplied {
			t.Errorf("second = {%s, %d}, want the same as first {%s, %d}",
				second.Balance, second.EntriesApplied, first.Balance, first.EntriesApplied)
		}
		if b := walletBalance(t, app, wallet); b != "300.00" {
			t.Errorf("persisted balance after two rebuilds = %s, want 300.00", b)
		}
	})

	t.Run("a cancelled context fails cleanly and leaves the row untouched", func(t *testing.T) {
		wallet := uuid.New()
		insertLedgerEntry(t, owner, wallet, "CREDIT", "1.00", base)
		seedWalletBalance(t, owner, wallet, "999999.00")

		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		if _, err := repo.Rebuild(cancelled, wallet); err == nil {
			t.Fatal("Rebuild(cancelled ctx) = nil error, want an error")
		}
		if b := walletBalance(t, app, wallet); b != "999999.00" {
			t.Errorf("balance after a failed rebuild = %s, want the original 999999.00", b)
		}
	})
}

// TestRebuild_LedgerReadFailure_RollsBackTheDelete proves the transaction is atomic where it
// matters: Rebuild DELETEs the old row BEFORE it reads the ledger, so if the read fails the
// delete must roll back. Otherwise an operator's failed rebuild would leave the wallet with NO
// projection row at all -- worse than the corrupted one they were trying to fix.
func TestRebuild_LedgerReadFailure_RollsBackTheDelete(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t)
	owner := connectPool(t, ownerDSN)
	repo := repository.NewRebuildRepository(connectPool(t, appDSN))
	ctx := context.Background()

	wallet := uuid.New()
	seedWalletBalance(t, owner, wallet, "999999.00")

	// Make the ledger read fail (after the DELETE has already run) by taking the grant away.
	if _, err := owner.Exec(ctx, `REVOKE SELECT ON ledger_db.ledger_entries FROM projection_app`); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := repo.Rebuild(ctx, wallet); err == nil {
		t.Fatal("Rebuild() = nil error, want the ledger read to fail")
	}

	if b := walletBalance(t, owner, wallet); b != "999999.00" {
		t.Errorf("balance after a failed rebuild = %s, want 999999.00 (the DELETE must have rolled back, not <no row>)", b)
	}
}

// TestRebuild_ThenLedgerEvent_AppliesOnTopExactlyOnce is Step 3's concurrency claim, tested end
// to end through the real consumer: after a rebuild,
//   - a redelivery of the entry the rebuild already folded in is a no-op (last_entry_id matches), and
//   - an entry that arrives afterwards is applied on top, not dropped.
//
// It deliberately does NOT cover a consumer event for an EARLIER entry than last_entry_id: the
// consumer's guard only remembers the last entry, so that case would double-apply (the known
// limitation recorded in Task 04, Step 3).
func TestRebuild_ThenLedgerEvent_AppliesOnTopExactlyOnce(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t)
	owner := connectPool(t, ownerDSN)
	app := connectPool(t, appDSN)
	repo := repository.NewRebuildRepository(app)
	ctx := context.Background()

	wallet, counterparty := uuid.New(), uuid.New()
	folded := insertLedgerEntry(t, owner, wallet, "CREDIT", "100.00", time.Now().UTC().Add(-time.Hour))
	seedWalletBalance(t, owner, wallet, "999999.00")

	got, err := repo.Rebuild(ctx, wallet)
	if err != nil {
		t.Fatalf("Rebuild() = error %v, want nil", err)
	}
	if !got.Balance.Equal(decimal.RequireFromString("100.00")) {
		t.Fatalf("Rebuild().Balance = %s, want 100.00", got.Balance)
	}
	if id := lastEntryID(t, app, wallet); !equalUUIDPtr(id, folded) {
		t.Fatalf("last_entry_id after rebuild = %v, want %s", id, folded)
	}

	// 1) Redelivery of the entry the rebuild already consumed. Must be a no-op.
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: folded, AccountID: wallet, EntryType: "CREDIT", Amount: "100.00"},
		{EntryID: uuid.New(), AccountID: counterparty, EntryType: "DEBIT", Amount: "100.00"},
	}, "")
	// 2) A genuinely new entry. Must be applied on top of the rebuilt balance.
	fresh := uuid.New()
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: fresh, AccountID: wallet, EntryType: "CREDIT", Amount: "25.00"},
		{EntryID: uuid.New(), AccountID: counterparty, EntryType: "DEBIT", Amount: "25.00"},
	}, "")

	c := consumer.NewLedgerPostedConsumer(app, &config.Config{
		KafkaBrokers: []string{bootstrap},
		KafkaGroupID: "test-rebuild-group",
	})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() = error %v, want nil", err)
	}
	t.Cleanup(c.Close)

	// 100.00 (rebuilt) + 25.00 (new). A double-applied redelivery would give 225.00 and never
	// settle on 125.00; a dropped new entry would stick at 100.00.
	tickUntil(t, c, func() bool { return walletBalance(t, app, wallet) == "125.00" })

	if id := lastEntryID(t, app, wallet); !equalUUIDPtr(id, fresh) {
		t.Errorf("last_entry_id = %v, want %s (the new entry)", id, fresh)
	}
}
