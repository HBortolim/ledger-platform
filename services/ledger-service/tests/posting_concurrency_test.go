package tests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestPostingConcurrency smoke-tests the SELECT ... FOR UPDATE locking
// described in ADR-002. The full 50-iteration TST-CONCURRENCY suite is
// Milestone 4's scope — these are cheaper guards that the locking path
// pulled forward by decision #4 actually works.
func TestPostingConcurrency(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool)

	t.Run("ConcurrentDebitsSameAccount/ExactlyOneSucceeds", func(t *testing.T) {
		source := uuid.New()
		destA, destB := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, source, "100.00")

		txA := newBalancedTransfer(source, destA, "60.00")
		txB := newBalancedTransfer(source, destB, "60.00")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = repo.Post(ctx, txA) }()
		go func() { defer wg.Done(); errs[1] = repo.Post(ctx, txB) }()
		wg.Wait()

		successes, failures := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				successes++
			case errors.As(err, &domain.ErrInsufficientFunds{}):
				failures++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if successes != 1 || failures != 1 {
			t.Fatalf("got %d successes, %d insufficient-funds failures; want exactly 1 and 1 (never both succeed, never both fail)", successes, failures)
		}

		var finalBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", source).Scan(&finalBalance); err != nil {
			t.Fatalf("final balance query: %v", err)
		}
		if finalBalance != "40.00" {
			t.Errorf("final source balance = %s, want 40.00 (100.00 seed - exactly one 60.00 debit)", finalBalance)
		}
	})

	t.Run("ConcurrentOppositeOrderPostings/NoDeadlock", func(t *testing.T) {
		accountA, accountB := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, accountA, "1000000.00")
		seedAccountBalance(t, pool, accountB, "1000000.00")

		const iterations = 25
		for i := 0; i < iterations; i++ {
			iterCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

			// One posting debits A (entries built A-then-B), the other
			// debits B (entries built B-then-A) — sortedDebitIDs' sort,
			// not entry order, must normalize the FOR UPDATE lock order.
			txAtoB := newBalancedTransfer(accountA, accountB, "1.00")
			txBtoA := newBalancedTransfer(accountB, accountA, "1.00")

			var wg sync.WaitGroup
			errs := make([]error, 2)
			wg.Add(2)
			go func() { defer wg.Done(); errs[0] = repo.Post(iterCtx, txAtoB) }()
			go func() { defer wg.Done(); errs[1] = repo.Post(iterCtx, txBtoA) }()
			wg.Wait()
			cancel()

			for j, err := range errs {
				if err != nil {
					t.Fatalf("iteration %d: Post()[%d] = %v, want nil (deadlock or timeout)", i, j, err)
				}
			}
		}
	})
}
