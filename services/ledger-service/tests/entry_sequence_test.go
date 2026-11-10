package tests

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

func entrySeqs(t *testing.T, pool *pgxpool.Pool, account uuid.UUID) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT seq FROM ledger_db.ledger_entries WHERE account_id = $1 ORDER BY seq`, account)
	if err != nil {
		t.Fatalf("query seqs: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func entryCounter(t *testing.T, pool *pgxpool.Pool, account uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT entry_seq FROM ledger_db.account_balances_locked WHERE account_id = $1`, account).Scan(&n); err != nil {
		t.Fatalf("query entry_seq: %v", err)
	}
	return n
}

// TestEntrySequence covers SEQ-1..SEQ-3: Post numbers each account's entries 1, 2, 3, ... in
// posting order, the lock row's counter tracks it, and a rejected posting consumes nothing.
func TestEntrySequence(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = %v", err)
	}
	t.Cleanup(pool.Close)
	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	t.Run("NumbersEachAccountsEntriesFromOne", func(t *testing.T) {
		a, b := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, a, "1000.00")

		for i, want := range [][2]int64{{1, 1}, {2, 2}, {3, 3}} {
			if err := repo.Post(ctx, newBalancedTransfer(a, b, "1.00")); err != nil {
				t.Fatalf("Post #%d = %v", i+1, err)
			}
			if got := entryCounter(t, pool, a); got != want[0] {
				t.Errorf("after post %d, entry_seq(a) = %d, want %d", i+1, got, want[0])
			}
			if got := entryCounter(t, pool, b); got != want[1] {
				t.Errorf("after post %d, entry_seq(b) = %d, want %d", i+1, got, want[1])
			}
		}
		for _, acct := range []uuid.UUID{a, b} {
			got := entrySeqs(t, pool, acct)
			if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
				t.Errorf("seqs for %s = %v, want [1 2 3]", acct, got)
			}
		}
	})

	t.Run("TwoEntriesOnOneAccountAreNumberedInPostingOrder", func(t *testing.T) {
		a, b := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, a, "1000.00")
		money := func(s string) domain.Money { m, _ := domain.NewMoney(s); return m }
		txID := uuid.New()
		first, second, third := uuid.New(), uuid.New(), uuid.New()
		tx := domain.LedgerTransaction{ID: txID, Type: domain.TransactionTransfer, Entries: []domain.LedgerEntry{
			{ID: first, TransactionID: txID, AccountID: a, EntryType: domain.Debit, Amount: money("10.00")},
			{ID: second, TransactionID: txID, AccountID: a, EntryType: domain.Debit, Amount: money("5.00")},
			{ID: third, TransactionID: txID, AccountID: b, EntryType: domain.Credit, Amount: money("15.00")},
		}}
		if err := repo.Post(ctx, tx); err != nil {
			t.Fatalf("Post() = %v", err)
		}
		seqOf := func(id uuid.UUID) int64 {
			var s int64
			if err := pool.QueryRow(ctx, `SELECT seq FROM ledger_db.ledger_entries WHERE id = $1`, id).Scan(&s); err != nil {
				t.Fatalf("seq of %s: %v", id, err)
			}
			return s
		}
		if seqOf(first) != 1 || seqOf(second) != 2 || seqOf(third) != 1 {
			t.Errorf("seqs = %d,%d,%d, want 1,2,1 (posting order within an account)", seqOf(first), seqOf(second), seqOf(third))
		}
		if got := entryCounter(t, pool, a); got != 2 {
			t.Errorf("entry_seq(a) = %d, want 2", got)
		}
		if got := tx.Entries[0].Seq; got != 0 {
			t.Errorf("caller's entry was mutated: Seq = %d, want 0", got)
		}

		// GetTransactionByID returns the numbers too.
		got, err := repo.GetTransactionByID(ctx, txID)
		if err != nil {
			t.Fatalf("GetTransactionByID() = %v", err)
		}
		bySeq := map[uuid.UUID]int64{}
		for _, e := range got.Entries {
			bySeq[e.ID] = e.Seq
		}
		if bySeq[first] != 1 || bySeq[second] != 2 || bySeq[third] != 1 {
			t.Errorf("GetTransactionByID seqs = %v, want first=1 second=2 third=1", bySeq)
		}
	})

	t.Run("RejectedPostingConsumesNoNumbers", func(t *testing.T) {
		poor, rich := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, poor, "5.00")
		if err := repo.Post(ctx, newBalancedTransfer(poor, rich, "1.00")); err != nil {
			t.Fatalf("first Post() = %v", err)
		}
		err := repo.Post(ctx, newBalancedTransfer(poor, rich, "999.00"))
		var insufficient domain.ErrInsufficientFunds
		if !errors.As(err, &insufficient) {
			t.Fatalf("overdraft Post() = %v, want ErrInsufficientFunds", err)
		}
		if err := repo.Post(ctx, newBalancedTransfer(poor, rich, "1.00")); err != nil {
			t.Fatalf("third Post() = %v", err)
		}
		for _, acct := range []uuid.UUID{poor, rich} {
			got := entrySeqs(t, pool, acct)
			if len(got) != 2 || got[0] != 1 || got[1] != 2 {
				t.Errorf("seqs for %s = %v, want [1 2] (the rejected posting must not burn a number)", acct, got)
			}
			if c := entryCounter(t, pool, acct); c != 2 {
				t.Errorf("entry_seq(%s) = %d, want 2", acct, c)
			}
		}
	})
}

// TestEntrySequence_ConcurrentPostingsToOneWallet is the test SEQ-2 exists for: many goroutines
// post to the same wallets at once and every account must end up with exactly 1..N, no
// duplicates, no gaps. The UNIQUE (account_id, seq) constraint makes a double-assignment fail a
// posting loudly, so a clean run also proves the counter was read under the lock.
func TestEntrySequence_ConcurrentPostingsToOneWallet(t *testing.T) {
	_, appDSN := setupLedgerDBHighConcurrency(t)
	pool := newHighConcurrencyPool(t, appDSN)
	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	shared := uuid.New()
	seedAccountBalance(t, pool, shared, "1000000.00")
	const workers, perWorker = 40, 5

	counterparties := make([]uuid.UUID, workers)
	for i := range counterparties {
		counterparties[i] = uuid.New()
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// Alternate direction so the shared wallet is both debited and credited.
				var tx domain.LedgerTransaction
				if i%2 == 0 {
					tx = newBalancedTransfer(shared, counterparties[w], "1.00")
				} else {
					tx = newBalancedTransfer(systemFundingAccountID, shared, "1.00")
				}
				if err := repo.Post(context.Background(), tx); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Post() = %v", err)
	}

	total := int64(workers * perWorker)
	got := entrySeqs(t, pool, shared)
	if int64(len(got)) != total {
		t.Fatalf("shared wallet has %d entries, want %d", len(got), total)
	}
	for i, s := range got {
		if s != int64(i)+1 {
			t.Fatalf("seq[%d] = %d, want %d: duplicate or gap in the shared wallet's numbering", i, s, i+1)
		}
	}
	if c := entryCounter(t, pool, shared); c != total {
		t.Errorf("entry_seq(shared) = %d, want %d", c, total)
	}
}

// TestMigration0006_BackfillsHistory seeds entries at schema version 5 (no seq column), applies
// 0006 and checks every account is numbered 1..n in (created_at, id) order with the counter
// equal to n; then proves .down and a second .up round-trip.
func TestMigration0006_BackfillsHistory(t *testing.T) {
	ownerDSN, _ := setupLedgerDB(t)
	ctx := context.Background()
	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Migrate(5); err != nil {
		t.Fatalf("Migrate(5) = %v", err)
	}

	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	a, b := uuid.New(), uuid.New()
	var aEntries []uuid.UUID
	for i, ago := range []string{"3 hours", "2 hours", "1 hour"} {
		txID, e1, e2 := uuid.New(), uuid.New(), uuid.New()
		aEntries = append(aEntries, e1)
		if _, err := conn.Exec(ctx, `INSERT INTO ledger_db.ledger_transactions(id, type, created_at) VALUES ($1, 'TRANSFER', now() - $2::interval)`, txID, ago); err != nil {
			t.Fatalf("seed tx %d: %v", i, err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO ledger_db.ledger_entries(id, transaction_id, account_id, entry_type, amount, created_at) VALUES
			($1, $3, $4, 'DEBIT', 10, now() - $6::interval), ($2, $3, $5, 'CREDIT', 10, now() - $6::interval)`, e1, e2, txID, a, b, ago); err != nil {
			t.Fatalf("seed entries %d: %v", i, err)
		}
	}
	// Every account with entries must already have a lock row for the counter backfill.
	for _, acct := range []uuid.UUID{a, b} {
		if _, err := conn.Exec(ctx, `INSERT INTO ledger_db.account_balances_locked(account_id) VALUES ($1) ON CONFLICT DO NOTHING`, acct); err != nil {
			t.Fatalf("seed lock row: %v", err)
		}
	}

	if err := m.Up(); err != nil {
		t.Fatalf("Up() = %v", err)
	}

	pool, err := pgxpool.New(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	for _, acct := range []uuid.UUID{a, b} {
		got := entrySeqs(t, pool, acct)
		if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
			t.Errorf("backfilled seqs for %s = %v, want [1 2 3]", acct, got)
		}
		if c := entryCounter(t, pool, acct); c != 3 {
			t.Errorf("backfilled entry_seq(%s) = %d, want 3", acct, c)
		}
	}
	// Oldest entry of a got seq 1: numbering follows (created_at, id).
	var firstSeq int64
	if err := pool.QueryRow(ctx, `SELECT seq FROM ledger_db.ledger_entries WHERE id = $1`, aEntries[0]).Scan(&firstSeq); err != nil || firstSeq != 1 {
		t.Errorf("oldest entry seq = %d, err=%v, want 1", firstSeq, err)
	}

	// The unique constraint is the database-level guarantee of SEQ-1.
	if _, err := pool.Exec(ctx, `UPDATE ledger_db.ledger_entries SET seq = 1 WHERE id = $1`, aEntries[2]); err == nil {
		t.Error("duplicate (account_id, seq) was accepted, want a unique violation")
	}

	if err := m.Migrate(5); err != nil {
		t.Fatalf("down to 5 = %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("second Up() = %v", err)
	}
	if got := entrySeqs(t, pool, a); len(got) != 3 || got[2] != 3 {
		t.Errorf("seqs after down/up = %v, want [1 2 3]", got)
	}
}
