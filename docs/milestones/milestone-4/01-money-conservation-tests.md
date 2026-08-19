# Task 01 — Money conservation tests (TST-CONCURRENCY-1, -2)

**Status:** Complete
**Owner:** Ledger Service
**Depends on:** nothing new — builds on M2 Task 03's `PostingRepository` and M2 Task 07's `posting_concurrency_test.go`
**Blocks:** Task 04
**Spec reference:** [`SPEC.md` §10.4](../../SPEC.md) (TST-CONCURRENCY-1, TST-CONCURRENCY-2), §9.4 (the canonical race), ADR-002

---

## Goal

Scale the existing 2-request concurrency smoke tests up to the spec's actual numbers, and add the money-conservation test that doesn't exist yet at all. These are the two tests a reviewer checks first: money is never created, and money is never destroyed.

## Why the existing smoke tests aren't enough

`services/ledger-service/tests/posting_concurrency_test.go` already proves the *mechanism* works (`ConcurrentDebitsSameAccount/ExactlyOneSucceeds` — 2 requests, 1 iteration). TST-CONCURRENCY-1 asks for **100 concurrent requests, 50 iterations** — two orders of magnitude more contention on the same lock row, which is exactly the regime where a subtle bug (a missed `FOR UPDATE`, a lock acquired outside the transaction, a connection pool starvation deadlock) would show up that 2 requests never would.

## Step 1: Add a high-concurrency DB helper

**Files:**
- Modify: `services/ledger-service/tests/testhelpers_test.go`

The existing `setupLedgerDB(t)` starts a plain `postgres:16-alpine` container and returns owner/app DSNs. Postgres's default `max_connections=100` isn't enough headroom for 100 simultaneous app-side transactions plus the harness's migration connection — add a sibling helper that raises it, used only by the M4 scale tests:

```go
// setupLedgerDBHighConcurrency is setupLedgerDB with a higher Postgres
// max_connections, for tests that hold 100+ simultaneous connections open
// (one per in-flight PostingRepository.Post call). The default
// max_connections=100 isn't enough headroom once the harness's own
// migration connection and Postgres's reserved superuser slots are
// subtracted — hitting that ceiling produces a flaky "sorry, too many
// clients already" failure that looks like, but isn't, the race the test
// exists to catch.
func setupLedgerDBHighConcurrency(t *testing.T) (ownerDSN, appDSN string) {
	t.Helper()

	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("could not connect to docker: %v", err)
	}

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "16-alpine",
		Env: []string{
			"POSTGRES_USER=ledger",
			"POSTGRES_PASSWORD=ledger",
			"POSTGRES_DB=ledger",
		},
		Cmd: []string{"postgres", "-c", "max_connections=300"},
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
	ownerDSN = fmt.Sprintf("postgres://ledger:ledger@localhost:%s/ledger?sslmode=disable", hostPort)
	appDSN = fmt.Sprintf("postgres://ledger_app:ledger_app@localhost:%s/ledger?sslmode=disable", hostPort)

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

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrations failed to apply: %v", err)
	}

	return ownerDSN, appDSN
}

// newHighConcurrencyPool opens a pgxpool sized for 100+ simultaneous
// PostingRepository.Post calls against a setupLedgerDBHighConcurrency database.
func newHighConcurrencyPool(t *testing.T, appDSN string) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() = error %v, want nil", err)
	}
	cfg.MaxConns = 150

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
```

- [x] Add both functions to `testhelpers_test.go`, alongside the existing `setupLedgerDB`.
- [x] Run `cd services/ledger-service && go build ./tests/...` — expect it to compile (no test uses the new helpers yet).

## Step 2: TST-CONCURRENCY-1 — money cannot be created, at scale

**Files:**
- Create: `services/ledger-service/tests/posting_concurrency_scale_test.go`

```go
package tests

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
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

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

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
```

- [x] Run: `cd services/ledger-service && go test ./tests/... -run TestMoneyCannotBeCreated_100ConcurrentTransfers -v -timeout 300s`
- [x] Expected: `PASS`, all 50 iterations. Confirm the log shows a plausible mix of successes/failures per iteration (not all-succeed or all-fail, which would mean the lock isn't actually serializing anything).

## Step 3: TST-CONCURRENCY-2 — money cannot be destroyed

**Files:**
- Modify: `services/ledger-service/tests/posting_concurrency_scale_test.go`

```go
// TestMoneyCannotBeDestroyed_100ConcurrentTransfers is TST-CONCURRENCY-2:
// 100 concurrent transfers between two wallets, funded well above what all
// of them could possibly need, so every one succeeds. The amount debited
// from the source must exactly equal the amount credited to the
// destination — no leakage, no double-counting.
func TestMoneyCannotBeDestroyed_100ConcurrentTransfers(t *testing.T) {
	_, appDSN := setupLedgerDBHighConcurrency(t)
	pool := newHighConcurrencyPool(t, appDSN)
	ctx := context.Background()

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	const (
		concurrency = 100
		perTransfer = "10.00"
	)
	source, dest := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, source, "1000000.00")
	seedAccountBalance(t, pool, dest, "0.00")

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
	destDelta := destFinal.Sub(decimal.RequireFromString("0.00"))

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
```

Add `"github.com/jackc/pgx/v5/pgxpool"` to the file's imports for `queryLockedBalance`'s signature.

- [x] Run: `cd services/ledger-service && go test ./tests/... -run TestMoneyCannotBeDestroyed_100ConcurrentTransfers -v -timeout 60s`
- [x] Expected: `PASS`.

## Step 4: Run both together, then commit

- [x] Run: `cd services/ledger-service && go test ./... -timeout 300s` — confirm nothing else regressed (existing `posting_concurrency_test.go` smoke tests still pass alongside the new scale tests).
- [x] Commit:

```bash
git add services/ledger-service/tests/testhelpers_test.go services/ledger-service/tests/posting_concurrency_scale_test.go
git commit -m "test: add TST-CONCURRENCY-1/2 at spec scale (100 concurrent, 50 iterations)"
```

## Acceptance criteria

| Check | Expected |
|---|---|
| TST-CONCURRENCY-1 | 50/50 iterations: `successes × 10.00 ≤ 500.00`, final locked balance matches exactly, no unhandled errors |
| TST-CONCURRENCY-2 | 100/100 transfers succeed; `source_initial − source_final == dest_final − dest_initial == 1000.00` |
| `go test ./...` (ledger-service) | all green, including pre-existing M2 concurrency smoke tests |

## Done when

Both tests pass reliably (run the `-run` commands above 3–5 times locally to rule out obvious flakiness before moving to Task 04's formal 100-run stability pass).

## Notes

- If a run ever shows `successes` at exactly 100 or 0 in the money-cannot-be-created test, that's not a passing edge case — it means the lock isn't creating real contention (e.g., the pool never actually ran requests concurrently). Investigate before assuming it's fine.
- Keep `perTransfer` fixed across all 100 goroutines in TST-CONCURRENCY-1 so the exact-final-balance assertion (not just the `≤` bound) is meaningful — with equal amounts, the number of winners is deterministic given the lock works correctly.
- `newHighConcurrencyPool`'s `MaxConns = 150` must stay comfortably under the container's `max_connections=300` (leaving room for the harness's own migration connection and Postgres's reserved superuser slots).
