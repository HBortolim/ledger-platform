# Task 02 — Disjoint pairs don't block (TST-CONCURRENCY-3)

**Status:** Not started
**Owner:** Ledger Service
**Depends on:** Task 01 (reuses `setupLedgerDBHighConcurrency` / `newHighConcurrencyPool`)
**Blocks:** Task 04
**Spec reference:** [`SPEC.md` §10.4](../../SPEC.md) (TST-CONCURRENCY-3), AC-5.18, ADR-002's "Consequences" section

---

## Goal

Prove the negative claim that AC-5.18 and ADR-002 both make: locking one wallet's account row never blocks a transfer on an unrelated wallet. `posting_concurrency_test.go`'s existing tests only ever contend on a *shared* account (same source, or the same two accounts in both entry orders) — none of them prove disjoint transfers run in parallel rather than merely "don't deadlock." This task adds that proof, as a timing comparison.

## Step 1: Write the test

**Files:**
- Create: `services/ledger-service/tests/posting_throughput_test.go`

```go
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
	_, appDSN := setupLedgerDBHighConcurrency(t)
	pool := newHighConcurrencyPool(t, appDSN)
	ctx := context.Background()

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	const fanOut = 50
	const amount = "1.00"

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
```

- [ ] Run: `cd services/ledger-service && go test ./tests/... -run TestDisjointPairsDontBlock -v -timeout 60s`
- [ ] Expected: `PASS`, with the logged ratio comfortably above `2.00x`. Note the actual ratio in the task's implementation record once it's run — this is the number a reviewer will look at.

## Step 2: Guard against flakiness from container warm-up

The first `Post()` call after `setupLedgerDBHighConcurrency` pays for connection establishment and query planning that later calls don't. If `disjointElapsed` comes out anomalously small (e.g., a handful of milliseconds) causing an inflated, meaningless ratio, add a throwaway warm-up transfer before Phase 1 starts:

```go
	// Warm-up: pay connection/plan-cache costs once, outside both timed phases.
	warmSrc, warmDst := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, warmSrc, "10.00")
	if err := repo.Post(ctx, newBalancedTransfer(warmSrc, warmDst, "1.00")); err != nil {
		t.Fatalf("warm-up transfer: %v", err)
	}
```

Insert this immediately after `repo := repository.NewPostingRepository(...)` and before Phase 1. Only add this if Step 1's run shows it's needed — don't add speculative code the first run doesn't justify.

- [ ] If added, re-run: `go test ./tests/... -run TestDisjointPairsDontBlock -v -timeout 60s -count=5` to confirm the ratio is stable across 5 runs.

## Step 3: Commit

```bash
git add services/ledger-service/tests/posting_throughput_test.go
git commit -m "test: add TST-CONCURRENCY-3, disjoint wallet pairs don't block each other"
```

## Acceptance criteria

| Check | Expected |
|---|---|
| TST-CONCURRENCY-3 | `sharedElapsed > 2 * disjointElapsed`, logged ratio recorded below |
| Repeatability | 5 consecutive runs all pass without adjusting the threshold |

## Done when

`TestDisjointPairsDontBlock` passes reliably across repeated local runs with a comfortable margin over the 2x threshold.

## Notes

- This test intentionally reuses `setupLedgerDBHighConcurrency`/`newHighConcurrencyPool` from Task 01 rather than duplicating a second connection-limit workaround.
- The `2x` threshold is deliberately conservative (50 truly serialized transfers should be far more than 2x slower than 50 parallel ones) to avoid flaking on a loaded CI box. If real-world runs consistently show a much larger ratio (e.g., 10x+), that's fine — the assertion only needs to prove the qualitative claim, not pin an exact number.
- Do not reuse `pairs` or `sharedSource` across the two phases — Phase 2 must start from a clean, fully-funded lock row so its serialization cost is due to lock contention alone, not leftover balance-check failures.

## Implementation record

_(Fill in after running: the actual measured `T`, `T'`, and ratio, plus confirmation this task's tests are folded into the Task 04 stability pass.)_
