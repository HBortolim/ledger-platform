package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestDisjointPairsDontBlock is TST-CONCURRENCY-3: 50 concurrent transfers
// across 50 distinct wallet pairs must not serialize against each other
// (time T). 50 concurrent transfers that all share one source wallet MUST
// serialize behind that wallet's lock row (time T'). If disjoint transfers
// were secretly taking a global lock, T and T' would be roughly equal;
// this test asserts T' is meaningfully larger, proving the lock is scoped
// to the contended account, not the whole ledger.
func TestDisjointPairsDontBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipped in -short mode — see docs/milestones/milestone-4/02-disjoint-pairs-throughput-test.md")
	}

	_, appDSN := setupLedgerDBHighConcurrency(t)
	pool := newHighConcurrencyPool(t, appDSN)
	ctx := context.Background()

	repo := repository.NewPostingRepository(pool, testDailyCap)

	const fanOut = 50
	const amount = "1.00"

	// Warm-up: pay connection/plan-cache costs once by running a disjoint batch
	// matching the fanOut (not just a single transfer). Initial testing showed
	// that a single-transfer warm-up was insufficient: Phase 1 (disjoint) paid
	// database cold-start overhead while Phase 2 (shared) benefited from that
	// warmth, causing the shared (serialized) phase to appear faster than the
	// disjoint (parallel) phase, masking the serialization effect. A full 50-
	// concurrent-transfer warm-up primes the query planner and connection pool.
	warmupPairs := make([][2]uuid.UUID, fanOut)
	for i := range warmupPairs {
		src, dst := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, src, "1000000.00")
		warmupPairs[i] = [2]uuid.UUID{src, dst}
	}
	runConcurrentTransfers(t, ctx, repo, fanOut, func(i int) (uuid.UUID, uuid.UUID) {
		return warmupPairs[i][0], warmupPairs[i][1]
	}, amount)

	// Phase 1: 50 disjoint wallet pairs, all funded, all independent.
	pairs := make([][2]uuid.UUID, fanOut)
	for i := range pairs {
		src, dst := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, src, "1000000.00")
		pairs[i] = [2]uuid.UUID{src, dst}
	}

	disjointStart := time.Now()
	runConcurrentTransfers(t, ctx, repo, fanOut, func(i int) (uuid.UUID, uuid.UUID) {
		return pairs[i][0], pairs[i][1]
	}, amount)
	disjointElapsed := time.Since(disjointStart)

	// Phase 2: 50 transfers that all share one source wallet — forces
	// serialization on that one account's lock row.
	sharedSource := uuid.New()
	seedAccountBalance(t, pool, sharedSource, "1000000.00")

	sharedStart := time.Now()
	runConcurrentTransfers(t, ctx, repo, fanOut, func(i int) (uuid.UUID, uuid.UUID) {
		return sharedSource, uuid.New()
	}, amount)
	sharedElapsed := time.Since(sharedStart)

	t.Logf("disjoint pairs (T)=%s, shared source (T')=%s, ratio=%.2fx", disjointElapsed, sharedElapsed, float64(sharedElapsed)/float64(disjointElapsed))

	if sharedElapsed < 2*disjointElapsed {
		t.Fatalf("shared-source transfers (T'=%s) were not meaningfully slower than disjoint-pair transfers (T=%s) — "+
			"expected T' > 2xT, got %.2fx; this suggests transfers are serializing globally, not per-account",
			sharedElapsed, disjointElapsed, float64(sharedElapsed)/float64(disjointElapsed))
	}
}

// runConcurrentTransfers fires n balanced 1-entry-pair transfers concurrently via
// pairOf(i), waiting for all to complete, and fails the test on any error.
func runConcurrentTransfers(t *testing.T, ctx context.Context, repo *repository.PostingRepository, n int, pairOf func(i int) (uuid.UUID, uuid.UUID), amount string) {
	t.Helper()

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			src, dst := pairOf(i)
			tx := newBalancedTransfer(src, dst, amount)
			errs[i] = repo.Post(ctx, tx)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("transfer %d: Post() = %v, want nil", i, err)
		}
	}
}
