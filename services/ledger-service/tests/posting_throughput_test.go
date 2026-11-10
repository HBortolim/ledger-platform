package tests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestDisjointPairsDontBlock is TST-CONCURRENCY-3: locking one wallet's account row never
// blocks a transfer on an unrelated wallet (AC-5.18, ADR-002).
//
// It is deterministic, not a timing comparison. An earlier version compared the wall time of 50
// disjoint transfers against 50 same-source ones and asserted a 2x ratio. That batch time is the
// slowest of 50 transfers, on a ~25ms baseline, and one pooled connection would intermittently
// stall for ~200ms for the whole run (it failed about one run in four, on a tree without this
// task's code too), collapsing the ratio whatever the locking did.
//
// Here the contended lock is held explicitly: A's row is locked by an open transaction, so a
// transfer touching A MUST block. A transfer between two unrelated accounts MUST still complete.
// A ledger-wide lock would make the unrelated transfer queue behind the blocked one and never
// finish.
func TestDisjointPairsDontBlock(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)
	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seedAccountBalance(t, pool, a, "1000.00")
	seedAccountBalance(t, pool, c, "1000.00")

	// Hold A's lock row exactly the way Post does (SELECT ... FOR UPDATE).
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_ = holder.Rollback(ctx)
		}
	}
	t.Cleanup(release)
	if _, err := holder.Exec(ctx, `SELECT 1 FROM ledger_db.account_balances_locked WHERE account_id = $1 FOR UPDATE`, a); err != nil {
		t.Fatalf("lock A: %v", err)
	}

	blocked := make(chan error, 1)
	go func() { blocked <- repo.Post(ctx, newBalancedTransfer(a, b, "1.00")) }()

	// Wait until Postgres reports a session waiting on a lock: A->B is now provably blocked on A,
	// not merely slow to start.
	waitFor(t, 10*time.Second, "A->B to block on A's row lock", func() bool {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatalf("query pg_stat_activity: %v", err)
		}
		return waiting > 0
	})

	// The unrelated pair must complete while A->B is stuck. The generous bound only catches a
	// hang; it is not a performance threshold.
	disjoint := make(chan error, 1)
	go func() { disjoint <- repo.Post(ctx, newBalancedTransfer(c, d, "1.00")) }()
	select {
	case err := <-disjoint:
		if err != nil {
			t.Fatalf("C->D Post() = error %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("C->D did not complete while A->B was blocked on A: transfers are serializing beyond the contended account")
	}

	select {
	case err := <-blocked:
		t.Fatalf("A->B completed (err=%v) while A's row was locked by another transaction", err)
	default:
	}

	release()
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("A->B Post() after release = error %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("A->B never completed after A's lock was released")
	}
}

// waitFor polls cond every 20ms until it is true, failing the test after timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
