package tests

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestMoneyCannotBeCreated_100ConcurrentTransfers is TST-CONCURRENCY-1: 100
// concurrent transfers from a wallet with insufficient cumulative funds to
// cover all of them. Some succeed, some fail with INSUFFICIENT_FUNDS, and
// the sum of what succeeded never exceeds the initial balance — run 50
// times to make the race window matter.
func TestMoneyCannotBeCreated_100ConcurrentTransfers(t *testing.T) {
	_, appDSN := setupLedgerDBHighConcurrency(t)
	pool := newHighConcurrencyPool(t, appDSN)
	ctx := context.Background()

	repo := repository.NewPostingRepository(pool, testDailyCap)

	const (
		iterations     = 50
		concurrency    = 100
		perTransfer    = "10.00"
		initialBalance = "500.00" // covers at most 50 of the 100 concurrent 10.00 transfers
	)
	perTransferAmount := decimal.RequireFromString(perTransfer)
	initial := decimal.RequireFromString(initialBalance)

	for iter := 0; iter < iterations; iter++ {
		source := uuid.New()
		seedAccountBalance(t, pool, source, initialBalance)

		var wg sync.WaitGroup
		errs := make([]error, concurrency)
		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			i := i
			go func() {
				defer wg.Done()
				tx := newBalancedTransfer(source, uuid.New(), perTransfer)
				errs[i] = repo.Post(ctx, tx)
			}()
		}
		wg.Wait()

		successes := 0
		for _, err := range errs {
			switch {
			case err == nil:
				successes++
			case errors.As(err, &domain.ErrInsufficientFunds{}):
				// expected for the requests that lost the race
			default:
				t.Fatalf("iteration %d: unexpected error: %v", iter, err)
			}
		}

		failures := concurrency - successes
		t.Logf("iteration %d: successes=%d failures=%d", iter, successes, failures)

		// With a correct lock, the winner count is deterministic: exactly
		// initial/perTransfer = 500.00/10.00 = 50 of the 100 concurrent
		// transfers can succeed. Fewer than 50 means spurious rejections
		// (contention wrongly rejecting requests that should have won);
		// more than 50 means money was created that the initial balance
		// didn't cover. This is a stronger check than the spent<=initial
		// bound below: it catches a regression that miscounts successes in
		// a way the spent/balance checks (which are derived from
		// successes itself) would not.
		const wantSuccesses = 50
		if successes != wantSuccesses {
			t.Fatalf("iteration %d: successes = %d, want exactly %d (500.00/10.00) — fewer means spurious rejections under contention, more means money was created from nothing",
				iter, successes, wantSuccesses)
		}

		spent := perTransferAmount.Mul(decimal.NewFromInt(int64(successes)))
		if spent.GreaterThan(initial) {
			t.Fatalf("iteration %d: %d successes spent %s, want <= initial balance %s (money created from nothing)",
				iter, successes, spent, initial)
		}

		var finalBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", source).Scan(&finalBalance); err != nil {
			t.Fatalf("iteration %d: final balance query: %v", iter, err)
		}
		wantFinal := initial.Sub(spent).StringFixed(2)
		if finalBalance != wantFinal {
			t.Fatalf("iteration %d: final balance = %s, want %s (initial %s minus %d successful %s transfers)",
				iter, finalBalance, wantFinal, initial, successes, perTransfer)
		}
	}
}

// TestMoneyCannotBeDestroyed_100ConcurrentTransfers is TST-CONCURRENCY-2:
// 100 concurrent transfers between two wallets, funded well above what all
// of them could possibly need, so every one succeeds. The amount debited
// from the source must exactly equal the amount credited to the
// destination — no leakage, no double-counting.
func TestMoneyCannotBeDestroyed_100ConcurrentTransfers(t *testing.T) {
	_, appDSN := setupLedgerDBHighConcurrency(t)
	pool := newHighConcurrencyPool(t, appDSN)
	ctx := context.Background()

	repo := repository.NewPostingRepository(pool, testDailyCap)

	const (
		concurrency = 100
		perTransfer = "10.00"
	)
	source, dest := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, source, "1000000.00")
	seedAccountBalance(t, pool, dest, "0.01")

	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			tx := newBalancedTransfer(source, dest, perTransfer)
			errs[i] = repo.Post(ctx, tx)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("transfer %d: Post() = %v, want nil (source is funded well beyond what 100x10.00 needs)", i, err)
		}
	}

	sourceFinal := queryLockedBalance(t, ctx, pool, source)
	destFinal := queryLockedBalance(t, ctx, pool, dest)

	sourceDelta := decimal.RequireFromString("1000000.00").Sub(sourceFinal)
	destDelta := destFinal.Sub(decimal.RequireFromString("0.01"))

	if !sourceDelta.Equal(destDelta) {
		t.Fatalf("source lost %s but dest gained %s — money was created or destroyed", sourceDelta, destDelta)
	}
	want := decimal.RequireFromString(perTransfer).Mul(decimal.NewFromInt(concurrency))
	if !sourceDelta.Equal(want) {
		t.Fatalf("source lost %s, want exactly %s (%d transfers x %s, all should have succeeded)", sourceDelta, want, concurrency, perTransfer)
	}
}

func queryLockedBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID) decimal.Decimal {
	t.Helper()
	var balStr string
	if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", accountID).Scan(&balStr); err != nil {
		t.Fatalf("query locked balance for %s: %v", accountID, err)
	}
	d, err := decimal.NewFromString(balStr)
	if err != nil {
		t.Fatalf("parse locked balance %q: %v", balStr, err)
	}
	return d
}
