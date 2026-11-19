# M3 Task 08 — Integration + E2E + Unit Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove Milestone 3 at three altitudes per `docs/milestones/milestone-3/08-integration-e2e-tests.md`: close the remaining wallet-service IT gaps (in-progress, §9.2 recovery), close remaining unit-test gaps, measure and (where already true) enforce ≥80% domain/application coverage, and build a brand-new `tests/e2e` Go module that drives the full `docker compose` stack through real HTTP for TST-E2E-1..4. Two blocking prerequisites, discovered during reconnaissance, are fixed first: `make test` is currently broken at the repo root (no `services/wallet-service/Makefile` exists despite the root `Makefile` delegating to it), and the Ledger Service's system funding account has no real overdraft exemption (Task 09, M3's own explicit dependency for E2E — nothing can fund a wallet through the public API without it).

**Architecture:** No architectural changes — this task is entirely tests plus one production fix (the system-account exemption, which is Task 09's documented scope, not a design change). The new `tests/e2e` module is a separate Go module (own `go.mod`) at the repo root, gated behind the `e2e` build tag, driving the stack purely over the network (HTTP to Wallet/Ledger Service, direct Postgres reads for ledger-row assertions, direct Kafka consumption for event assertions) — it never imports code from `services/`.

**Tech Stack:** Java 26 / Spring Boot 4 / JUnit 5 / Mockito / WireMock (Wallet Service tests); Go 1.25 / dockertest (existing Ledger + Projection Service tests); Go 1.25 / `golang-jwt/jwt/v5` (new `tests/e2e` module, its own dependency set).

## Global Constraints

- `make test` must pass with **no Docker orchestration** (SPEC.md §10.2's IT harnesses use Testcontainers/dockertest directly, not `docker compose`) — this is already true for the existing suites; the new wallet-service Makefile must not change that.
- `make test-e2e` **requires `make up` first** (a live compose stack) — it must never be invoked from `make test` or CI.
- E2E test wallets are owned by a fresh random UUID per test run (never a fixed seeded user) so reruns against a warm stack don't collide (task doc's explicit note, mirroring the M2 "distinct test accounts" convention).
- E2E balance assertions poll `GET /wallets/{id}/balance` every 200ms with a 5s deadline — never a blind `sleep`, per NFR-CONS-2's SLO and the task doc's explicit instruction.
- The system-account overdraft exemption (Task 09) must be **exactly one account wide** — matched only on the configured `SystemAccountID`, never inferred from "account absent from `account_balances_locked`" (every account auto-provisions at 0 on first touch; wallets must keep failing overdrafts).
- Coverage enforcement is added only where it's honestly already true today — see Task 5's measure-first design. Do not lower or fabricate a passing threshold.

---

## Context this plan assumes you know

- **`make test` is currently broken.** The root `Makefile`'s `build`/`test`/`lint`/`fmt`/`clean` targets all run `$(MAKE) -C services/wallet-service <target>`, but `services/wallet-service/Makefile` does not exist and never has (confirmed via `git ls-files`). CI never caught this because `.github/workflows/ci.yml` invokes `./mvnw -q verify` directly per-service, bypassing the root `Makefile` entirely. Fixing this is Task 1 below — a prerequisite for every other task's own `make test` verification step.
- **Task 09 (system account overdraft) is not implemented**, despite its own doc's "Depends on: —" / "Blocks: 08" framing implying it should have landed already. `services/ledger-service/internal/repository/posting.go`'s `checkAvailableBalance` has no exemption for any account; the system funding account (`00000000-0000-0000-0000-000000000001`) is seeded at a **workaround balance of `1000`** via `migrations/0002_seed_system_accounts.up.sql`, not `0`. This works for small ad-hoc demo amounts but has no real exemption — a `DEBIT` over `1000` against that account fails exactly like it would against a normal wallet. `tests/testhelpers_test.go`'s `seedAccountBalance` helper already has a doc comment describing this exact gap (calling it "a Milestone 6 concern" — that comment predates the M3 overview's decision #8, which supersedes it; this plan does not need to rewrite that comment, just be aware it's stale prose, not stale behavior). Task 2 below implements the real exemption.
- **`docs/decisions/000X-*.md` ADR format**: `# ADR-000X: Title`, then `**Status:** Accepted` / `**Date:** YYYY-MM-DD`, then `## Context` / `## Decision` / `## Alternatives considered` / `## Consequences`. Follow `docs/decisions/0011-daily-transfer-cap-in-ledger-service.md`'s structure and tone exactly.
- **`repository.NewPostingRepository(pool, dailyCap)` has 8 call sites** across `services/ledger-service/{cmd/server/main.go, tests/outbox_test.go (×3), tests/posting_concurrency_test.go (×2), tests/posting_http_test.go (×1), tests/posting_test.go (×2)}`. Task 2 adds a third parameter to this constructor, so **every one of these 8 call sites must be updated** — exact locations are given in Task 2.
- **`RequestFingerprint.of(CreateTransferRequest)`** is `public static` and callable directly from any wallet-service test — no need to hand-roll SHA-256 in new tests.
- **`IdempotencyStatus.PENDING` records are never "stale"** (`IdempotencyService.isStale`'s `switch` returns `false` unconditionally for `PENDING`, regardless of `createdAt` age) — a fresh `POST /transfers` against an existing PENDING row *always* goes through `awaitCompletion`'s ~5s poll-then-`InProgress` path, whether the row is 1 second or 2 hours old. Row age only matters for `IdempotencyJanitor.sweep()`'s `failStalePending(cutoff)` query, which needs `created_at` older than the janitor's hardcoded 60s `PENDING_TIMEOUT` to actually flip a row to `FAILED`. This is why Task 3's two new wallet ITs seed different `createdAt` values (one deliberately 2 hours stale, one `now`) even though both hit the identical 5s wait on their first request.
- **A `POST /transfers` against a PENDING key with no matching data behind it and no WireMock stub for the recovery `GET`** naturally 404s (WireMock's default unmatched-request response), which `LedgerClient.getTransaction` already maps to `Optional.empty()` — producing the plain 409 `IN_PROGRESS` outcome with **no explicit stub required**. Only the recovery-success scenario needs an explicit `GET /admin/ledger/transactions/{id}` stub.
- **`transferId` *is* the `transactionId`** (deterministic UUIDv5 from `(userId, idempotencyKey)`, per ADR-0006) — the new E2E suite uses the `transferId` a `POST /transfers` response returns directly as the transaction ID for its Postgres/Kafka assertions; no separate lookup needed.
- **Wallet balances render as bare JSON numbers**, not strings (`BalanceResponse.balance` is a `BigDecimal`, Jackson's default numeric serialization) — confirmed live: `{"walletId":"...","balance":400.00,...}`. The E2E suite's Go struct uses `float64`. All E2E test amounts are whole-number BRL values (100.00, 400.00, 50.00 — never fractional cents), so direct `float64` equality is safe (these are exactly representable; no epsilon comparison needed).
- **M3 Task 06 (`GET /wallets/{id}/balance`) is already fully implemented**, not a stub — `GetBalanceUseCase` computes the `stale` flag from a real 5-second threshold (SPEC.md AC-2.4's literal cutoff; NFR-CONS-2's 2s figure is a separate p95-lag *target*, not this flag's threshold) and has 7 existing unit tests. No task in this plan touches it; it's listed here only because Task 08's own "Depends on" line names it and this plan should make clear that dependency is already satisfied, not silently skipped.
- **Projection apply logic's `last_entry_id` skip branch has no pure unit test, and this plan does not add one.** The skip logic lives entirely in a SQL `ON CONFLICT ... WHERE wallet_balances.last_entry_id IS DISTINCT FROM EXCLUDED.last_entry_id` clause (`services/projection-service/internal/consumer/apply.go`) — inherently DB-dependent, with no way to isolate it from a real Postgres connection. Its only coverage is TST-INT-4 (`TestConsumer_DuplicateDelivery_IsNoOp`, already passing), which is the correct altitude for this logic — manufacturing a fake "unit" test around a hand-rolled SQL-semantics stand-in would test the stand-in, not the actual behavior.

---

## Task 1: Fix `make test` — add `services/wallet-service/Makefile`

**Files:**
- Create: `services/wallet-service/Makefile`

**Interfaces:**
- Consumes: nothing new.
- Produces: `build`, `test`, `lint`, `fmt`, `clean` targets the root `Makefile` already expects via `$(MAKE) -C services/wallet-service <target>`. No other task depends on this file's internals — only on `make test`/`make build`/`make clean` succeeding at the repo root afterward.

- [ ] **Step 1: Write the Makefile**

```makefile
# services/wallet-service/Makefile

.PHONY: build test lint fmt clean

build:
	./mvnw -q -DskipTests package

test:
	./mvnw -q verify

lint:
	@echo "no Java linter configured for wallet-service yet (nothing to run)"

fmt:
	@echo "no Java formatter configured for wallet-service yet (nothing to run)"

clean:
	./mvnw -q clean
```

- [ ] **Step 2: Verify the root Makefile's delegation now works**

Run (from the repo root): `make build`
Expected: succeeds, ends by producing `services/wallet-service/target/*.jar`, and also runs `go build ./...` cleanly for `ledger-service`/`projection-service` (unchanged, already worked).

Run: `make test`
Expected: runs `./mvnw -q verify` for wallet-service (unit + IT suites — this will take a few minutes due to Testcontainers), then `go test ./...` for `ledger-service` and `projection-service`. All green. This is the **first time `make test` has ever succeeded** from the repo root — previously it failed immediately with `No rule to make target 'test'`.

Run: `make lint`
Expected: succeeds (Go services lint via `golangci-lint`; wallet-service prints the no-op message and exits 0).

Run: `make clean`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add services/wallet-service/Makefile
git commit -m "fix: add missing services/wallet-service/Makefile so \`make test\` works from the repo root"
```

---

## Task 2: Ledger Service — system funding account overdraft exemption (M3 Task 09)

**Files:**
- Modify: `services/ledger-service/internal/config/config.go`
- Modify: `services/ledger-service/internal/repository/posting.go`
- Modify: `services/ledger-service/cmd/server/main.go`
- Modify: `docker-compose.yml`
- Create: `services/ledger-service/internal/repository/posting_unit_test.go`
- Modify: `services/ledger-service/tests/posting_test.go`
- Modify: `services/ledger-service/tests/outbox_test.go`
- Modify: `services/ledger-service/tests/posting_concurrency_test.go`
- Modify: `services/ledger-service/tests/posting_http_test.go`
- Create: `docs/decisions/0008-system-account-may-overdraw.md`

**Interfaces:**
- Consumes: nothing new externally.
- Produces: `config.Config` gains a `SystemAccountID uuid.UUID` field (env `SYSTEM_ACCOUNT_ID`, default `00000000-0000-0000-0000-000000000001`). `repository.NewPostingRepository` changes signature from `(pool *pgxpool.Pool, dailyCap decimal.Decimal)` to `(pool *pgxpool.Pool, dailyCap decimal.Decimal, systemAccountID uuid.UUID)` — **every one of its 8 call sites listed below must be updated in this task**. `checkAvailableBalance` becomes a method on `*PostingRepository` (was a free function) so it can read `r.systemAccountID`.

- [ ] **Step 1: Add `SystemAccountID` to config**

In `services/ledger-service/internal/config/config.go`, replace the whole file with:

```go
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Config struct {
	DatabaseURL      string
	KafkaBrokers     []string
	AppPort          string
	DailyTransferCap decimal.Decimal
	SystemAccountID  uuid.UUID
}

func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		return nil, fmt.Errorf("KAFKA_BROKERS is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	capStr := os.Getenv("DAILY_TRANSFER_CAP")
	if capStr == "" {
		capStr = "100000.00"
	}
	dailyCap, err := decimal.NewFromString(capStr)
	if err != nil {
		return nil, fmt.Errorf("invalid DAILY_TRANSFER_CAP %q: %w", capStr, err)
	}

	systemAccountIDStr := os.Getenv("SYSTEM_ACCOUNT_ID")
	if systemAccountIDStr == "" {
		systemAccountIDStr = "00000000-0000-0000-0000-000000000001"
	}
	systemAccountID, err := uuid.Parse(systemAccountIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid SYSTEM_ACCOUNT_ID %q: %w", systemAccountIDStr, err)
	}

	return &Config{
		DatabaseURL:      dbURL,
		KafkaBrokers:     strings.Split(brokers, ","),
		AppPort:          ":" + port,
		DailyTransferCap: dailyCap,
		SystemAccountID:  systemAccountID,
	}, nil
}
```

- [ ] **Step 2: Thread `systemAccountID` into `PostingRepository` and exempt it in `checkAvailableBalance`**

In `services/ledger-service/internal/repository/posting.go`, replace:

```go
type PostingRepository struct {
	pool     *pgxpool.Pool
	dailyCap decimal.Decimal
}

func NewPostingRepository(pool *pgxpool.Pool, dailyCap decimal.Decimal) *PostingRepository {
	return &PostingRepository{pool: pool, dailyCap: dailyCap}
}
```

with:

```go
type PostingRepository struct {
	pool            *pgxpool.Pool
	dailyCap        decimal.Decimal
	systemAccountID uuid.UUID
}

func NewPostingRepository(pool *pgxpool.Pool, dailyCap decimal.Decimal, systemAccountID uuid.UUID) *PostingRepository {
	return &PostingRepository{pool: pool, dailyCap: dailyCap, systemAccountID: systemAccountID}
}
```

Replace the call site inside `Post`:

```go
	// Step 3: Available-balance check — cached balance minus total debit must be non-negative.
	if err := checkAvailableBalance(tx.Entries, lockedBalances); err != nil {
		return err
	}
```

with:

```go
	// Step 3: Available-balance check — cached balance minus total debit must be non-negative.
	if err := r.checkAvailableBalance(tx.Entries, lockedBalances); err != nil {
		return err
	}
```

Replace the free function:

```go
func checkAvailableBalance(entries []domain.LedgerEntry, balances map[uuid.UUID]decimal.Decimal) error {
	totalDebit := make(map[uuid.UUID]decimal.Decimal)
	for _, e := range entries {
		if e.EntryType == domain.Debit {
			totalDebit[e.AccountID] = totalDebit[e.AccountID].Add(e.Amount.Decimal())
		}
	}
	for accountID, debitAmt := range totalDebit {
		if balances[accountID].LessThan(debitAmt) {
			return domain.ErrInsufficientFunds{AccountID: accountID}
		}
	}
	return nil
}
```

with:

```go
// checkAvailableBalance rejects any debit that would overdraw its account,
// except the configured system funding account: per NFR-CONS-5, its balance
// is definitionally the negative mirror of all money in circulation, so it
// must be allowed to go negative (ADR-0008). The exemption matches on this
// one configured UUID only — never on "account absent from
// account_balances_locked", since every account auto-provisions at 0 on
// first touch and every wallet must keep failing overdrafts.
func (r *PostingRepository) checkAvailableBalance(entries []domain.LedgerEntry, balances map[uuid.UUID]decimal.Decimal) error {
	totalDebit := make(map[uuid.UUID]decimal.Decimal)
	for _, e := range entries {
		if e.EntryType == domain.Debit {
			totalDebit[e.AccountID] = totalDebit[e.AccountID].Add(e.Amount.Decimal())
		}
	}
	for accountID, debitAmt := range totalDebit {
		if accountID == r.systemAccountID {
			continue
		}
		if balances[accountID].LessThan(debitAmt) {
			return domain.ErrInsufficientFunds{AccountID: accountID}
		}
	}
	return nil
}
```

- [ ] **Step 3: Update the production call site**

In `services/ledger-service/cmd/server/main.go`, replace:

```go
	postingRepo := repository.NewPostingRepository(pool, cfg.DailyTransferCap)
```

with:

```go
	postingRepo := repository.NewPostingRepository(pool, cfg.DailyTransferCap, cfg.SystemAccountID)
```

- [ ] **Step 4: Update every test call site (mechanical — only the argument list changes)**

`systemFundingAccountID` is already defined in `services/ledger-service/tests/testhelpers_test.go:25` (`uuid.MustParse("00000000-0000-0000-0000-000000000001")`) — reuse it, don't redefine it. Apply these exact one-line replacements:

In `services/ledger-service/tests/posting_test.go`:
- Line 32: `repo := repository.NewPostingRepository(pool, testDailyCap)` → `repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)`
- Line 172: `cappedRepo := repository.NewPostingRepository(pool, decimal.RequireFromString("100.00"))` → `cappedRepo := repository.NewPostingRepository(pool, decimal.RequireFromString("100.00"), systemFundingAccountID)`

In `services/ledger-service/tests/outbox_test.go` (all three identical, apply to lines 34, 123, 180):
- `repo := repository.NewPostingRepository(pool, testDailyCap)` → `repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)`

In `services/ledger-service/tests/posting_concurrency_test.go`:
- Line 32: `repo := repository.NewPostingRepository(pool, testDailyCap)` → `repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)`
- Line 77: `cappedRepo := repository.NewPostingRepository(pool, decimal.RequireFromString("100.00"))` → `cappedRepo := repository.NewPostingRepository(pool, decimal.RequireFromString("100.00"), systemFundingAccountID)`

In `services/ledger-service/tests/posting_http_test.go`:
- Line 39: `repo := repository.NewPostingRepository(pool, testDailyCap)` → `repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)`

Run: `cd services/ledger-service && go build ./... && go vet ./...`
Expected: PASS — this confirms every call site was found and updated (a missed call site is a compile error naming its exact file:line).

- [ ] **Step 5: Write the unit test**

Create `services/ledger-service/internal/repository/posting_unit_test.go`:

```go
package repository

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

func TestCheckAvailableBalance_SystemAccountExemptFromOverdraftCheck(t *testing.T) {
	systemAccountID := uuid.New()
	r := &PostingRepository{systemAccountID: systemAccountID}

	amount, err := domain.NewMoney("500.00")
	if err != nil {
		t.Fatalf("domain.NewMoney() = error %v, want nil", err)
	}
	entries := []domain.LedgerEntry{
		{AccountID: systemAccountID, EntryType: domain.Debit, Amount: amount},
	}
	balances := map[uuid.UUID]decimal.Decimal{systemAccountID: decimal.Zero}

	if err := r.checkAvailableBalance(entries, balances); err != nil {
		t.Errorf("checkAvailableBalance() = error %v, want nil (system account must be exempt)", err)
	}
}

func TestCheckAvailableBalance_NormalAccountStillRejectsOverdraft(t *testing.T) {
	systemAccountID := uuid.New()
	normalAccountID := uuid.New()
	r := &PostingRepository{systemAccountID: systemAccountID}

	amount, err := domain.NewMoney("500.00")
	if err != nil {
		t.Fatalf("domain.NewMoney() = error %v, want nil", err)
	}
	entries := []domain.LedgerEntry{
		{AccountID: normalAccountID, EntryType: domain.Debit, Amount: amount},
	}
	balances := map[uuid.UUID]decimal.Decimal{normalAccountID: decimal.Zero}

	err = r.checkAvailableBalance(entries, balances)
	var insufficient domain.ErrInsufficientFunds
	if !errors.As(err, &insufficient) {
		t.Fatalf("checkAvailableBalance() = %v, want ErrInsufficientFunds", err)
	}
	if insufficient.AccountID != normalAccountID {
		t.Errorf("ErrInsufficientFunds.AccountID = %s, want %s", insufficient.AccountID, normalAccountID)
	}
}
```

Run: `cd services/ledger-service && go test ./internal/repository/... -run TestCheckAvailableBalance -v`
Expected: PASS, 2/2.

- [ ] **Step 6: Extend the integration test**

In `services/ledger-service/tests/posting_test.go`, add two new `t.Run` subtests to `TestPostingRepository`, placed after the existing `"DailyCapExceeded/ReturnsTypedError"` subtest (i.e. insert before the `"DuplicateTransactionID/ReturnsTypedError"` subtest, which currently follows it):

```go
	t.Run("SystemAccountOverdraft/DepositEntersMoneyThroughAPI", func(t *testing.T) {
		wallet := uuid.New()
		// System account is seeded at 1000.00 by migration 0002; debit well past
		// that to prove the exemption, not just headroom within the seed value.
		deposit := newBalancedTransfer(systemFundingAccountID, wallet, "1500.00")
		if err := repo.Post(ctx, deposit); err != nil {
			t.Fatalf("deposit Post() = error %v, want nil (system account must be exempt from the overdraft check)", err)
		}

		var systemBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", systemFundingAccountID).Scan(&systemBalance); err != nil {
			t.Fatalf("query system account balance: %v", err)
		}
		if systemBalance != "-500.00" {
			t.Errorf("system account balance = %s, want -500.00 (1000.00 seed - 1500.00 debit, negative per NFR-CONS-5)", systemBalance)
		}

		var walletBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", wallet).Scan(&walletBalance); err != nil {
			t.Fatalf("query wallet balance: %v", err)
		}
		if walletBalance != "1500.00" {
			t.Errorf("wallet balance = %s, want 1500.00", walletBalance)
		}

		// The credited wallet can now fund a normal wallet-to-wallet transfer.
		destination := uuid.New()
		transfer := newBalancedTransfer(wallet, destination, "200.00")
		if err := repo.Post(ctx, transfer); err != nil {
			t.Fatalf("follow-up transfer Post() = error %v, want nil", err)
		}
	})

	t.Run("SystemAccountOverdraft/ExemptionIsExactlyOneAccountWide", func(t *testing.T) {
		zeroBalanceWallet, destination := uuid.New(), uuid.New()
		// zeroBalanceWallet is auto-provisioned at 0 on first touch -- not seeded.

		tx := newBalancedTransfer(zeroBalanceWallet, destination, "1.00")
		err := repo.Post(ctx, tx)

		var insufficient domain.ErrInsufficientFunds
		if !errors.As(err, &insufficient) {
			t.Fatalf("Post() = %v, want ErrInsufficientFunds (only the system account is exempt)", err)
		}
	})
```

Run: `cd services/ledger-service && go test ./tests/... -run TestPostingRepository -v`
Expected: PASS, including the two new subtests.

- [ ] **Step 7: Update `docker-compose.yml` for visibility**

In `docker-compose.yml`, in the `ledger-service` block's `environment:` section, add the new env var immediately after `DAILY_TRANSFER_CAP`:

```yaml
      DAILY_TRANSFER_CAP: "100000.00"
      SYSTEM_ACCOUNT_ID: "00000000-0000-0000-0000-000000000001"
```

- [ ] **Step 8: Write ADR-0008**

Create `docs/decisions/0008-system-account-may-overdraw.md`:

```markdown
# ADR-0008: System funding account may overdraw

**Status:** Accepted
**Date:** 2026-08-16

## Context

`docs/SPEC.md`'s NFR-CONS-5 defines the money-supply invariant: the sum of all wallet balances
plus the external funding account's balance equals zero at all times. This is only possible if
the external account's balance is allowed to go negative — every BRL credited into a wallet via
`POST /ledger/postings` (AC-6.1) is debited from this one account, and nothing ever credits it
back in v1 (withdrawals are M6 scope).

`checkAvailableBalance` in `PostingRepository.Post` rejected any debit exceeding the account's
cached balance, with no exception, for every account — including the system funding account,
seeded at a workaround balance of `1000` by `migrations/0002_seed_system_accounts.up.sql`. This
made the account's "exemption" an accident of the seed value being larger than whatever amount a
given demo or test happened to debit, not a real guarantee. Any deposit (or accumulation of
deposits) exceeding `1000` failed with `422 INSUFFICIENT_FUNDS`, exactly like it would for a
normal wallet — meaning nothing in Milestone 3 was demonstrable end-to-end without either a SQL
workaround or this fix, since every wallet starts at `0.00` and funding one requires debiting this
account.

## Decision

Exempt exactly one account — the ledger-service's configured `SystemAccountID` (env
`SYSTEM_ACCOUNT_ID`, default `00000000-0000-0000-0000-000000000001`) — from
`checkAvailableBalance`'s overdraft check. Everything else about the posting flow is unchanged:
the account is still locked `FOR UPDATE` in the same transaction, its cached balance in
`account_balances_locked` still tracks the (now negative) truth, and the double-entry invariant
trigger still enforces that every transaction balances to zero.

The match is on the configured UUID only — never inferred from "account has no
`account_balances_locked` row" or similar heuristics, since every account auto-provisions a
zero-balance lock row on first touch (`upsertAccountLocks`'s `ON CONFLICT DO NOTHING` upsert), and
a normal wallet's very first transfer would otherwise slip through the same heuristic.

## Alternatives considered

1. **Seed the account at a very large positive balance instead of a real exemption** — rejected:
   this is exactly the workaround already in place (seeded at `1000`) and it doesn't scale; any
   sustained demo, load test, or E2E suite rerunning against a warm stack eventually exhausts an
   arbitrarily large seed and starts failing for reasons unrelated to the scenario under test.
2. **Give the account a special `entry_type` or a dedicated posting endpoint that skips the check
   entirely** — rejected: `docs/SPEC.md` §2.2 explicitly scopes `/deposits`/`/withdrawals` as
   endpoints to Milestone 6. This ADR is deliberately the smallest possible M3 slice of FR-6 — the
   balance-check exemption only, reusing the existing `POST /ledger/postings` API exactly as
   wallet-to-wallet transfers do.
3. **Infer the exemption from account identity heuristics** (e.g. "any account never credited") —
   rejected: fragile, and every account (including brand-new wallets) starts in exactly that state
   before its first transfer; this would silently exempt the wrong accounts.

## Consequences

- `POST /ledger/postings` can now debit the system account arbitrarily far into negative
  territory, matching NFR-CONS-5's definition rather than approximating it with a large seed.
- The overview demo script's step 2 (seeding a wallet via a direct `DEBIT`/`CREDIT` posting)
  succeeds without any SQL workaround, unblocking the M3 demo and the new `tests/e2e` suite
  (Task 08).
- `migrations/0002_seed_system_accounts.up.sql`'s seed value of `1000` is now vestigial — harmless
  (the account can go negative from any starting point once exempt) but no longer load-bearing.
  It is left as-is rather than edited: this repo does not rewrite already-applied historical
  migrations (see ADR-0011's consequences section for the same convention applied to a different
  file).
- `services/ledger-service/tests/testhelpers_test.go`'s `seedAccountBalance` helper's doc comment,
  written before this ADR, calls the gap "a Milestone 6 concern" — that phrasing is now stale
  (the exemption is real and lands in M3), though the helper's actual behavior (seeding balances
  directly via SQL rather than through `PostingRepository.Post`) remains a valid, independent
  design choice for decoupling fixture setup from the code path under test.
```

- [ ] **Step 9: Run the full ledger-service suite**

Run: `cd services/ledger-service && go build ./... && go vet ./... && go test ./...`
Expected: PASS — this exercises every updated call site (Step 4) plus the new unit and integration tests together, confirming no regression in the existing M2 suite.

- [ ] **Step 10: Commit**

```bash
git add services/ledger-service/internal/config/config.go \
        services/ledger-service/internal/repository/posting.go \
        services/ledger-service/internal/repository/posting_unit_test.go \
        services/ledger-service/cmd/server/main.go \
        services/ledger-service/tests/posting_test.go \
        services/ledger-service/tests/outbox_test.go \
        services/ledger-service/tests/posting_concurrency_test.go \
        services/ledger-service/tests/posting_http_test.go \
        docker-compose.yml \
        docs/decisions/0008-system-account-may-overdraw.md
git commit -m "feat(ledger-service): exempt the system funding account from the overdraft check (M3 Task 09, ADR-0008)"
```

---

## Task 3: Wallet ITs — in-progress and §9.2 recovery scenarios

**Files:**
- Modify: `services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java`

**Interfaces:**
- Consumes: `IdempotencyRepository` (autowired, the real `IdempotencyJdbcRepository` bean), `IdempotencyJanitor` (autowired), `RequestFingerprint.of(CreateTransferRequest)`, `IdempotencyKeys.transactionId(UUID, String)` — all already exist, none of their signatures change.
- Produces: two new `@Test` methods. No other task depends on these tests' internals.
- **Both new tests incur a real ~5 second wait each** (production `IdempotencyService`'s hardcoded `DEFAULT_MAX_WAIT = Duration.ofSeconds(5)` — there is no Spring-config override for this constant, only the package-private test constructor used by `IdempotencyServiceTest` bypasses it). This is expected and matches the task doc's own wording ("~5 s wait then 409 IN_PROGRESS").

- [ ] **Step 1: Add the imports and autowired fields**

In `services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java`, add these imports alongside the existing ones:

```java
import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.application.idempotency.IdempotencyKeys;
import com.ledger.wallet.application.idempotency.IdempotencyRepository;
import com.ledger.wallet.application.idempotency.RequestFingerprint;
import com.ledger.wallet.domain.model.IdempotencyRecord;
import com.ledger.wallet.domain.model.IdempotencyStatus;
import com.ledger.wallet.infrastructure.idempotency.IdempotencyJanitor;

import java.math.BigDecimal;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
```

Add `import static com.github.tomakehurst.wiremock.client.WireMock.get;` alongside the other static WireMock imports.

Add two autowired fields just below the existing `meterRegistry` field:

```java
    @Autowired
    private IdempotencyRepository idempotencyRepository;

    @Autowired
    private IdempotencyJanitor idempotencyJanitor;
```

- [ ] **Step 2: Add the in-progress test**

Add this test method to the class:

```java
    @Test
    void pendingKeyNeverCompleted_returns409ThenBecomesReusableAfterSweep() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        String key = UUID.randomUUID().toString();

        CreateTransferRequest req = new CreateTransferRequest(
                UUID.fromString(source), UUID.fromString(destination), new BigDecimal("100.00"), null);
        String fingerprint = RequestFingerprint.of(req);
        // Deliberately stale (> IdempotencyJanitor's 60s PENDING_TIMEOUT) so the
        // sweep below actually flips this row -- PENDING itself is never "stale"
        // to IdempotencyService.begin, regardless of age; only the janitor cares.
        Instant longAgo = Instant.now().minus(2, ChronoUnit.HOURS);
        idempotencyRepository.insertPending(new IdempotencyRecord(
                key, userId, fingerprint, IdempotencyStatus.PENDING, null, null, longAgo, longAgo.plus(24, ChronoUnit.HOURS)));

        // No WireMock stub for GET /admin/ledger/transactions/{id}: the unmatched
        // request 404s by default, which recoverFromInProgress correctly treats as
        // "nothing to recover" -> TransferInProgressException -> 409.
        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "100.00")))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.code").value("IN_PROGRESS"));

        idempotencyJanitor.sweep();
        stubPosted(UUID.randomUUID().toString());

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "100.00")))
                .andExpect(status().isCreated());
    }
```

- [ ] **Step 3: Add the §9.2 recovery test**

Add this test method to the class:

```java
    @Test
    void pendingKeyAlreadyPostedByLedger_recoversAndReturns201() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        String key = UUID.randomUUID().toString();

        CreateTransferRequest req = new CreateTransferRequest(
                UUID.fromString(source), UUID.fromString(destination), new BigDecimal("100.00"), null);
        String fingerprint = RequestFingerprint.of(req);
        Instant now = Instant.now();
        idempotencyRepository.insertPending(new IdempotencyRecord(
                key, userId, fingerprint, IdempotencyStatus.PENDING, null, null, now, now.plus(24, ChronoUnit.HOURS)));

        UUID transactionId = IdempotencyKeys.transactionId(userId, key);
        LEDGER.stubFor(get(urlEqualTo("/admin/ledger/transactions/" + transactionId))
                .willReturn(aResponse().withStatus(200).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {"transactionId":"%s","type":"TRANSFER","postedAt":"2026-05-19T14:23:00.123Z","entries":[]}
                                """.formatted(transactionId))));

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "100.00")))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.transferId").value(transactionId.toString()));
    }
```

- [ ] **Step 4: Run both new tests**

Run: `cd services/wallet-service && ./mvnw test -Dtest=TransferControllerIT`
Expected: PASS, all 12 tests (10 existing + 2 new). Expect the suite to take roughly 10+ seconds longer than before (two real ~5s waits).

- [ ] **Step 5: Commit**

```bash
git add services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java
git commit -m "test(wallet-service): add IT coverage for the in-progress and §9.2 recovery transfer scenarios"
```

---

## Task 4: Unit test gaps — fingerprint stability and DAILY_LIMIT_EXCEEDED passthrough

**Files:**
- Modify: `services/wallet-service/src/test/java/com/ledger/wallet/application/idempotency/RequestFingerprintTest.java`
- Modify: `services/wallet-service/src/main/java/com/ledger/wallet/domain/exception/code/DomainErrorCode.java`
- Modify: `services/wallet-service/src/test/java/com/ledger/wallet/application/usecase/CreateTransferUseCaseTest.java`

**Interfaces:**
- Consumes: nothing new.
- Produces: `DomainErrorCode.DAILY_LIMIT_EXCEEDED` (a new `public static final String` constant) — used only by the new test in this task; no production code path branches on it (the passthrough mechanism is untyped, per ADR-0011).

**Decision carried into this task:** the task doc's "IdempotencyKeys.transactionId: ... random-with-warning when keyless" bullet is satisfied by `CreateTransferUseCase`'s existing `LOGGER.warn(...)` call (AC-5.15), not by `IdempotencyKeys.randomTransactionId()` itself, which has no warning responsibility of its own — ID generation and the decision to warn about a missing key are different concerns, and AC-5.15 is phrased as a wallet-service request-handling requirement, not an ID-generation one. This plan does not add log-capture test infrastructure to assert the warning's literal text: no existing test in this codebase captures SLF4J output, and introducing that pattern for one assertion is disproportionate to the value — the warning's *behavior* (the request still succeeds, `idempotencyService` is never touched) is already asserted by `CreateTransferUseCaseTest.keylessRequest_postsDirectlyAndNeverTouchesIdempotency`.

- [ ] **Step 1: Write the failing fingerprint-stability test**

Add this test to `services/wallet-service/src/test/java/com/ledger/wallet/application/idempotency/RequestFingerprintTest.java` (add the two imports shown alongside the existing ones, then the test method anywhere in the class):

```java
// new imports, alongside the existing ones at the top of the file:
import com.fasterxml.jackson.databind.ObjectMapper;
```

```java
    @Test
    void fingerprint_stableAcrossFieldOrderAndWhitespaceInSourceJson() throws Exception {
        ObjectMapper mapper = new ObjectMapper();
        UUID source = UUID.randomUUID();
        UUID destination = UUID.randomUUID();

        String compactReordered = String.format(
                "{\"amount\":100.00,\"destinationWalletId\":\"%s\",\"sourceWalletId\":\"%s\"}",
                destination, source);
        String whitespaced = String.format("""
                {
                  "sourceWalletId" :   "%s"  ,
                  "destinationWalletId":"%s",
                  "amount": 100.00
                }
                """, source, destination);

        CreateTransferRequest a = mapper.readValue(compactReordered, CreateTransferRequest.class);
        CreateTransferRequest b = mapper.readValue(whitespaced, CreateTransferRequest.class);

        assertThat(RequestFingerprint.of(a)).isEqualTo(RequestFingerprint.of(b));
    }
```

Note: `CreateTransferRequest` must already be imported in this file (it's the type `RequestFingerprint.of` takes) — if it isn't yet, add `import com.ledger.wallet.api.dto.CreateTransferRequest;`.

- [ ] **Step 2: Run it to verify it passes**

Run: `cd services/wallet-service && ./mvnw test -Dtest=RequestFingerprintTest`
Expected: PASS. (This is expected to pass immediately, not fail-then-pass — `RequestFingerprint.of` already canonicalizes via a fixed-order `LinkedHashMap` over the deserialized object's typed fields, so field order/whitespace in the *source* JSON was already structurally incapable of affecting the result. This test closes a *coverage* gap, not a *behavior* gap — it makes the existing guarantee explicit and regression-proof rather than implicit.)

- [ ] **Step 3: Add the `DAILY_LIMIT_EXCEEDED` constant**

In `services/wallet-service/src/main/java/com/ledger/wallet/domain/exception/code/DomainErrorCode.java`, replace:

```java
    public static final String INSUFFICIENT_FUNDS = "INSUFFICIENT_FUNDS";
```

with:

```java
    public static final String INSUFFICIENT_FUNDS = "INSUFFICIENT_FUNDS";
    public static final String DAILY_LIMIT_EXCEEDED = "DAILY_LIMIT_EXCEEDED"; // ADR-0011: enforced in Ledger Service, passed through verbatim
```

- [ ] **Step 4: Add the passthrough test**

In `services/wallet-service/src/test/java/com/ledger/wallet/application/usecase/CreateTransferUseCaseTest.java`, add this test immediately after `rejectedByLedger_returns422AndCompletes`:

```java
    @Test
    void rejectedByLedgerWithDailyLimitExceeded_returns422AndCompletes() {
        stubValidWallets();
        UUID transactionId = UUID.randomUUID();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Rejected(DomainErrorCode.DAILY_LIMIT_EXCEEDED, "daily cap exceeded"));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(422);
        assertThat(outcome.body()).contains(DomainErrorCode.DAILY_LIMIT_EXCEEDED);
        verify(idempotencyService).complete(userId, "key", 422, outcome.body());
    }
```

- [ ] **Step 5: Run both touched test classes**

Run: `cd services/wallet-service && ./mvnw test -Dtest=RequestFingerprintTest,CreateTransferUseCaseTest`
Expected: PASS — all previously-existing tests in both classes plus the two new ones.

- [ ] **Step 6: Commit**

```bash
git add services/wallet-service/src/test/java/com/ledger/wallet/application/idempotency/RequestFingerprintTest.java \
        services/wallet-service/src/main/java/com/ledger/wallet/domain/exception/code/DomainErrorCode.java \
        services/wallet-service/src/test/java/com/ledger/wallet/application/usecase/CreateTransferUseCaseTest.java
git commit -m "test(wallet-service): cover fingerprint stability across source-JSON formatting and the DAILY_LIMIT_EXCEEDED passthrough"
```

---

## Task 5: Coverage — measure, then enforce only what's already true

**Files:**
- Modify: `services/wallet-service/pom.xml`

**Interfaces:**
- Consumes: nothing new.
- Produces: if (and only if) measured coverage already clears 80% on `com.ledger.wallet.domain` and `com.ledger.wallet.application` packages, a JaCoCo `check` execution bound to `mvn verify` that fails the build below that measured floor. This task is a **measure-first** design — do not guess a threshold, read the actual number.

- [ ] **Step 1: Generate the current coverage report**

Run: `cd services/wallet-service && ./mvnw -q verify` (the existing `jacoco-maven-plugin` `report` execution already runs at the `test` phase — no config change needed to produce it)

Then open `services/wallet-service/target/site/jacoco/index.html` (or read the equivalent `target/site/jacoco/jacoco.csv`, which is easier to grep/parse than the HTML) and record the **line coverage percentage for every package under `com.ledger.wallet.domain.*` and `com.ledger.wallet.application.*`** (not `infrastructure` or `api` — those are explicitly not gated per SPEC.md §10.1).

- [ ] **Step 2: Decide based on the actual numbers**

**If every domain/application package's line coverage is already ≥ 80%:** add the enforcing rule (Step 3 below) with `<minimum>0.80</minimum>`, and report the actual measured percentages in your task report so the controller has evidence, not just a pass/fail.

**If any domain/application package is below 80%:** do not silently lower the bar or skip enforcement — instead:
1. Add the enforcing rule (Step 3) with `<minimum>` set to the actual lowest measured percentage among the gated packages, rounded down to the nearest whole percent (e.g. if the lowest is 76.3%, use `0.76`) — this makes today's true coverage the enforced floor, so the build fails on any *future* regression below what's already achieved, without fabricating an 80% claim that isn't true yet.
2. In your task report, list exactly which package(s) are below 80% and by how much, so the controller can decide whether closing the remaining gap is worth a follow-up task (it is out of scope for this plan to write arbitrary additional tests just to hit a number — see this plan's Global Constraints).

- [ ] **Step 3: Add the JaCoCo `check` execution**

In `services/wallet-service/pom.xml`, inside the existing `jacoco-maven-plugin` `<executions>` block, add a new `<execution>` after the existing `report` one:

```xml
            <plugin>
                <groupId>org.jacoco</groupId>
                <artifactId>jacoco-maven-plugin</artifactId>
                <version>0.8.15</version>
                <executions>
                    <execution>
                        <goals><goal>prepare-agent</goal></goals>
                    </execution>
                    <execution>
                        <id>report</id>
                        <phase>test</phase>
                        <goals><goal>report</goal></goals>
                    </execution>
                    <execution>
                        <id>jacoco-check</id>
                        <goals><goal>check</goal></goals>
                        <configuration>
                            <rules>
                                <rule>
                                    <element>PACKAGE</element>
                                    <includes>
                                        <include>com.ledger.wallet.domain.**</include>
                                        <include>com.ledger.wallet.application.**</include>
                                    </includes>
                                    <limits>
                                        <limit>
                                            <counter>LINE</counter>
                                            <value>COVEREDRATIO</value>
                                            <minimum>REPLACE_WITH_STEP_2_DECISION</minimum>
                                        </limit>
                                    </limits>
                                </rule>
                            </rules>
                        </configuration>
                    </execution>
                </executions>
            </plugin>
```

Replace `REPLACE_WITH_STEP_2_DECISION` with the actual decimal value from Step 2 (`0.80`, or the rounded-down actual floor). If the `<include>` wildcard pattern doesn't scope to exactly the intended packages (verify by checking the `check` goal's own console output, which lists what it evaluated), adjust the pattern — JaCoCo's include/exclude matching on package elements needs confirming empirically against this Maven/JaCoCo version rather than assumed from memory.

- [ ] **Step 4: Verify the check goal actually runs and passes**

Run: `cd services/wallet-service && ./mvnw -q verify`
Expected: PASS, and the console output includes JaCoCo's check-goal summary confirming the packages it evaluated and their measured ratios against the configured minimum.

- [ ] **Step 5: Measure Go coverage for projection-service (reporting only, no enforcement — see rationale)**

Run: `cd services/projection-service && go test ./internal/consumer/... -coverprofile=/tmp/projection-coverage.out && go tool cover -func=/tmp/projection-coverage.out`

Record the reported total coverage percentage for the `internal/consumer` package in your task report. Do not add a Go coverage-gating tool (no equivalent to JaCoCo's `check` goal is already established anywhere in this repo's Go services, and introducing one now — beyond a single measurement command — is out of this task's scope; SPEC.md §10.1's 80% target is scoped to "domain packages" and neither Go service has an explicit domain-layer split the way wallet-service does, making a direct percentage comparison less meaningful than for Java).

- [ ] **Step 6: Commit**

```bash
git add services/wallet-service/pom.xml
git commit -m "build(wallet-service): enforce measured line-coverage floor on domain/application packages via JaCoCo"
```

---

## Task 6: `tests/e2e` — new Go module for TST-E2E-1..4

**Files:**
- Create: `tests/e2e/go.mod`
- Create: `tests/e2e/helpers_test.go`
- Create: `tests/e2e/transfer_e2e_test.go`
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Consumes: the live `docker compose` stack over the network only — `http://localhost:8080` (Wallet Service), `http://localhost:8081` (Ledger Service), `postgres://ledger_app:ledger_app@localhost:5432/ledger` (read-only entry assertions), `localhost:9092` (Kafka, `ledger.posted.v1` topic) — and `keys/private.pem` on disk (already generated by `scripts/generate-jwt.sh` or by any prior `make up`, per this repo's existing convention).
- Produces: a `make test-e2e` target. Nothing else in this plan depends on this module's internals.

**This is the largest task in the plan.** Build it as one commit since `helpers_test.go` has no independent test value without `transfer_e2e_test.go` using it — the smallest unit that "carries its own test cycle" here is the whole module passing against a live stack.

- [ ] **Step 1: Create the Go module**

Create `tests/e2e/go.mod`:

```
module github.com/ledger-platform/tests-e2e

go 1.25.0

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/twmb/franz-go v1.21.5
)
```

- [ ] **Step 2: Write the shared helpers**

Create `tests/e2e/helpers_test.go`:

```go
//go:build e2e

// Package e2e drives the full docker-compose stack through real HTTP, direct
// Postgres reads, and direct Kafka consumption — SPEC.md §10.3's TST-E2E-1..4.
// Run via `make test-e2e` after `make up`; never invoked by `make test` or CI
// (no compose orchestration there). Requires keys/private.pem to already
// exist (generated by scripts/generate-jwt.sh or any prior `make up`).
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	walletBaseURL = "http://localhost:8080"
	ledgerBaseURL = "http://localhost:8081"
	postgresDSN   = "postgres://ledger_app:ledger_app@localhost:5432/ledger?sslmode=disable"
	kafkaBroker   = "localhost:9092"
	ledgerTopic   = "ledger.posted.v1"
	// systemAccountID mirrors ADR-0008's default SYSTEM_ACCOUNT_ID.
	systemAccountID = "00000000-0000-0000-0000-000000000001"
)

// signJWT mirrors scripts/generate-jwt.sh's claims shape (sub, role, iat,
// exp) and signing key (keys/private.pem, RS256) exactly, so tokens minted
// here are interchangeable with ones the shell script would produce.
func signJWT(t *testing.T, userID uuid.UUID, role string) string {
	t.Helper()

	keyPath := os.Getenv("E2E_PRIVATE_KEY_PATH")
	if keyPath == "" {
		keyPath = "../../keys/private.pem"
	}
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read private key at %s (run scripts/generate-jwt.sh once, or \\`make up\\`, to generate it): %v", keyPath, err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	if err != nil {
		t.Fatalf("parse RSA private key: %v", err)
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  userID.String(),
		"role": role,
		"iat":  now.Unix(),
		"exp":  now.Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return signed
}

func doRequest(t *testing.T, method, url, bearerToken, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(b)
}

// newWallet creates a wallet for ownerID and returns its ID. Each E2E test
// uses a fresh random ownerID per run so reruns against a warm stack never
// collide with a previous run's wallets.
func newWallet(t *testing.T, token string, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	body := fmt.Sprintf(`{"ownerId":"%s","currency":"BRL"}`, ownerID)
	resp := doRequest(t, http.MethodPost, walletBaseURL+"/wallets", token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /wallets = %d, want 201; body: %s", resp.StatusCode, readBody(t, resp))
	}
	var created struct {
		WalletID string `json:"walletId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode wallet creation response: %v", err)
	}
	return uuid.MustParse(created.WalletID)
}

// seedDeposit funds destinationWallet directly through the Ledger API's
// system funding account (ADR-0008's exemption) -- mirrors the overview demo
// script's step 2.
func seedDeposit(t *testing.T, destinationWallet uuid.UUID, amount string) {
	t.Helper()
	txID := uuid.New()
	body := fmt.Sprintf(
		`{"transactionId":"%s","type":"DEPOSIT","entries":[{"accountId":"%s","entryType":"DEBIT","amount":"%s"},{"accountId":"%s","entryType":"CREDIT","amount":"%s"}]}`,
		txID, systemAccountID, amount, destinationWallet, amount)
	resp := doRequest(t, http.MethodPost, ledgerBaseURL+"/ledger/postings", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed deposit POST /ledger/postings = %d, want 201; body: %s", resp.StatusCode, readBody(t, resp))
	}
}

func transferBody(source, destination uuid.UUID, amount string) string {
	return fmt.Sprintf(`{"sourceWalletId":"%s","destinationWalletId":"%s","amount":"%s"}`, source, destination, amount)
}

// postTransfer issues POST /transfers with the given Idempotency-Key and
// returns the response plus its already-read body (resp.Body is consumed).
func postTransfer(t *testing.T, token, idempotencyKey string, source, destination uuid.UUID, amount string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, walletBaseURL+"/transfers", strings.NewReader(transferBody(source, destination, amount)))
	if err != nil {
		t.Fatalf("build transfer request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /transfers: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read transfer response body: %v", err)
	}
	return resp, string(body)
}

type balanceResponse struct {
	WalletID string  `json:"walletId"`
	Balance  float64 `json:"balance"`
	Currency string  `json:"currency"`
	AsOf     string  `json:"asOf"`
	Stale    bool    `json:"stale"`
}

// pollBalance polls GET /wallets/{id}/balance every 200ms for up to 5s (the
// NFR-CONS-2 SLO) until want is reached, returning whatever it last observed
// either way so callers can assert on a timeout too. All E2E test amounts
// are whole-number BRL values, exactly representable as float64, so a direct
// equality check against want is safe here (no epsilon needed).
func pollBalance(t *testing.T, token string, walletID uuid.UUID, want float64) balanceResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last balanceResponse
	for time.Now().Before(deadline) {
		resp := doRequest(t, http.MethodGet, fmt.Sprintf("%s/wallets/%s/balance", walletBaseURL, walletID), token, "")
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&last); err != nil {
				t.Fatalf("decode balance response: %v", err)
			}
			resp.Body.Close()
			if last.Balance == want {
				return last
			}
		} else {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

// assertLedgerEntryCount asserts accountID has exactly want rows in
// ledger_db.ledger_entries, connecting directly to Postgres as ledger_app
// (the same restricted role the Ledger Service itself runs as).
func assertLedgerEntryCount(t *testing.T, accountID uuid.UUID, want int) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), postgresDSN)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer conn.Close(context.Background())

	var count int
	err = conn.QueryRow(context.Background(),
		"SELECT count(*) FROM ledger_db.ledger_entries WHERE account_id = $1", accountID).Scan(&count)
	if err != nil {
		t.Fatalf("query ledger_entries for account %s: %v", accountID, err)
	}
	if count != want {
		t.Errorf("ledger_entries count for account %s = %d, want %d", accountID, count, want)
	}
}

// assertLedgerEntriesForTransaction asserts transactionID has exactly want
// rows in ledger_db.ledger_entries.
func assertLedgerEntriesForTransaction(t *testing.T, transactionID uuid.UUID, want int) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), postgresDSN)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer conn.Close(context.Background())

	var count int
	err = conn.QueryRow(context.Background(),
		"SELECT count(*) FROM ledger_db.ledger_entries WHERE transaction_id = $1", transactionID).Scan(&count)
	if err != nil {
		t.Fatalf("query ledger_entries for transaction %s: %v", transactionID, err)
	}
	if count != want {
		t.Errorf("ledger_entries count for transaction %s = %d, want %d", transactionID, count, want)
	}
}

// assertLedgerPostedObserved consumes ledger.posted.v1 from the start with a
// fresh consumer group (so it always sees the whole topic history) until it
// finds an event for transactionID or a 10s deadline elapses.
func assertLedgerPostedObserved(t *testing.T, transactionID uuid.UUID) {
	t.Helper()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(kafkaBroker),
		kgo.ConsumeTopics(ledgerTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.ConsumerGroup("e2e-"+uuid.New().String()),
	)
	if err != nil {
		t.Fatalf("kafka client: %v", err)
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for LEDGER_POSTED for transaction %s on %s", transactionID, ledgerTopic)
		}
		found := false
		fetches.EachRecord(func(r *kgo.Record) {
			var event struct {
				TransactionID string `json:"transactionId"`
			}
			if err := json.Unmarshal(r.Value, &event); err == nil && event.TransactionID == transactionID.String() {
				found = true
			}
		})
		if found {
			return
		}
	}
}
```

- [ ] **Step 3: Write the four E2E scenarios**

Create `tests/e2e/transfer_e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestE2E_HappyPath covers TST-E2E-1: create 2 wallets, seed the source via
// the Ledger API, transfer, poll balances, and confirm the ledger row and
// Kafka event both exist -- this also completes TST-INT-1's "ledger rows
// exist" half, deferred here per the M3 overview's decision #5.
func TestE2E_HappyPath(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")

	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	resp, body := postTransfer(t, token, key, source, destination, "100.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /transfers = %d, want 201; body: %s", resp.StatusCode, body)
	}
	var created struct {
		TransferID string `json:"transferId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode transfer response: %v", err)
	}
	if created.Status != "COMPLETED" {
		t.Errorf("transfer status = %s, want COMPLETED", created.Status)
	}
	transactionID := uuid.MustParse(created.TransferID)

	srcBal := pollBalance(t, token, source, 400.00)
	if srcBal.Balance != 400.00 {
		t.Errorf("source balance = %.2f, want 400.00 within the 5s SLO poll window (last observed: %+v)", srcBal.Balance, srcBal)
	}
	dstBal := pollBalance(t, token, destination, 100.00)
	if dstBal.Balance != 100.00 {
		t.Errorf("destination balance = %.2f, want 100.00 within the 5s SLO poll window (last observed: %+v)", dstBal.Balance, dstBal)
	}

	assertLedgerPostedObserved(t, transactionID)
	assertLedgerEntriesForTransaction(t, transactionID, 2)
}

// TestE2E_IdempotentReplay covers TST-E2E-2: the same transfer twice with the
// same key returns identical responses, and exactly one transaction's worth
// of entries exist -- no double-post.
func TestE2E_IdempotentReplay(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	firstResp, firstBody := postTransfer(t, token, key, source, destination, "100.00")
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first POST /transfers = %d, want 201; body: %s", firstResp.StatusCode, firstBody)
	}
	var first struct {
		TransferID string `json:"transferId"`
	}
	if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
		t.Fatalf("decode first transfer response: %v", err)
	}

	secondResp, secondBody := postTransfer(t, token, key, source, destination, "100.00")
	if secondResp.StatusCode != http.StatusCreated {
		t.Fatalf("second POST /transfers = %d, want 201; body: %s", secondResp.StatusCode, secondBody)
	}
	if firstBody != secondBody {
		t.Errorf("replayed response = %s, want byte-identical to first response %s", secondBody, firstBody)
	}

	transactionID := uuid.MustParse(first.TransferID)
	assertLedgerEntriesForTransaction(t, transactionID, 2)
}

// TestE2E_IdempotencyKeyMismatch covers TST-E2E-3: same key, different body.
func TestE2E_IdempotencyKeyMismatch(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	firstResp, firstBody := postTransfer(t, token, key, source, destination, "100.00")
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first POST /transfers = %d, want 201; body: %s", firstResp.StatusCode, firstBody)
	}

	secondResp, secondBody := postTransfer(t, token, key, source, destination, "200.00")
	if secondResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second POST /transfers (mismatched body) = %d, want 422; body: %s", secondResp.StatusCode, secondBody)
	}
	if !strings.Contains(secondBody, "IDEMPOTENCY_KEY_MISMATCH") {
		t.Errorf("second response body = %s, want to contain IDEMPOTENCY_KEY_MISMATCH", secondBody)
	}
}

// TestE2E_InsufficientFunds covers TST-E2E-4: a transfer exceeding the
// source wallet's balance.
func TestE2E_InsufficientFunds(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "50.00") // not enough for the 999.00 attempt below

	key := uuid.New().String()
	resp, body := postTransfer(t, token, key, source, destination, "999.00")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /transfers (over balance) = %d, want 422; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "INSUFFICIENT_FUNDS") {
		t.Errorf("response body = %s, want to contain INSUFFICIENT_FUNDS", body)
	}

	assertLedgerEntryCount(t, source, 1) // only the seed deposit's CREDIT entry -- the rejected attempt wrote nothing
}
```

- [ ] **Step 4: Resolve dependencies**

Run: `cd tests/e2e && go mod tidy`
Expected: resolves `golang-jwt/jwt/v5`'s exact version plus every indirect dependency for `pgx/v5` and `franz-go`, generating `go.sum`. If `go mod tidy` picks a different `golang-jwt/jwt/v5` version than `v5.2.1`, that's fine — let it resolve to whatever the latest compatible release is; the exact patch version in `go.mod`'s `require` block isn't load-bearing.

Run: `cd tests/e2e && go build -tags=e2e ./... && go vet -tags=e2e ./...`
Expected: PASS (compiles cleanly; nothing to run yet without a live stack).

- [ ] **Step 5: Add the `make test-e2e` target**

In the repo root `Makefile`, update the `.PHONY` line:

```makefile
.PHONY: up down build test test-e2e load clean lint fmt
```

Add a new target, placed after `test:`'s block and before `lint:`:

```makefile
test-e2e:
	cd tests/e2e && go test -tags=e2e ./...
```

- [ ] **Step 6: Document it in the README**

In `README.md` (currently just the single line `# ledger-platform`), append:

```markdown

## Testing

- `make test` — unit + integration test suites for all three services (Testcontainers/dockertest manage their own ephemeral Postgres/Kafka; no `docker compose` stack required).
- `make up && make test-e2e` — end-to-end suite (`tests/e2e/`) against the full running stack. Requires `make up` first; never run by `make test` or CI.
```

- [ ] **Step 7: Run against the live stack**

Run: `make down && make up` (fresh stack)
Then run: `make test-e2e`
Expected: all four `TestE2E_*` functions PASS.

If `keys/private.pem` doesn't exist yet, generate it first: `./scripts/generate-jwt.sh 00000000-0000-0000-0000-000000000001` (this also regenerates `keys/public.pem`; if the stack was already running with a *different* previously-generated key pair, restart `wallet-service` afterward — `docker compose restart wallet-service` — so it picks up the new public key, exactly as happened during the M3 Task 07 plan's own live verification).

Run: `make down` to tear down afterward.

- [ ] **Step 8: Commit**

```bash
git add tests/e2e/go.mod tests/e2e/go.sum tests/e2e/helpers_test.go tests/e2e/transfer_e2e_test.go Makefile README.md
git commit -m "test: add tests/e2e Go module covering TST-E2E-1..4 against the live compose stack"
```

---

## Task 7: Final verification

**Files:** none — this task only runs commands and inspects output.

- [ ] **Step 1: Clean-checkout `make test`**

Run: `make test`
Expected: wallet-service (unit + IT), ledger-service (unit + IT), and projection-service (unit + IT) all green, no `docker compose` involvement, completing Task 1's original acceptance row.

- [ ] **Step 2: `make up && make test-e2e`**

Run: `make down && make up`
Wait for all services healthy (per SPEC.md §1.1, within 60s).
Run: `make test-e2e`
Expected: TST-E2E-1..4 all green, per Task 6.

- [ ] **Step 3: Spot-check TST-INT-3/4 once more against real Kafka + Postgres**

Run: `cd services/projection-service && go test ./tests/... -run TestConsumer -v`
Expected: PASS (already true going in — this just reconfirms no regression from this plan's other changes).

- [ ] **Step 4: Confirm coverage numbers from Task 5 are still accurate**

Run: `cd services/wallet-service && ./mvnw -q verify`
Expected: PASS, including the JaCoCo `check` goal added in Task 5.

- [ ] **Step 5: Tear down**

Run: `make down`

No commit for this task — it's verification only. If any step fails, return to the relevant task above and fix before considering Task 08 done.

---

## Definition of done (cross-check against the milestone doc)

- [ ] `make test` succeeds from the repo root (Task 1 fixes the prerequisite blocker; Task 7 confirms).
- [ ] `make up && make test-e2e` passes TST-E2E-1..4 (Task 6 builds it; Task 7 confirms).
- [ ] TST-INT-3/TST-INT-4 pass against real Kafka + Postgres via dockertest (already true; Task 7 reconfirms).
- [ ] In-progress and §9.2 recovery scenarios have real HTTP-level IT coverage, not just use-case-level unit coverage (Task 3).
- [ ] Fingerprint stability and the DAILY_LIMIT_EXCEEDED passthrough have dedicated unit tests (Task 4).
- [ ] Coverage on wallet-service domain/application packages is measured, and enforced at whatever floor is honestly true today (Task 5).
- [ ] The system funding account can fund wallets purely through the public Ledger API, exactly one account wide, documented in ADR-0008 (Task 2, M3's own Task 09).
- [ ] `docs/superpowers/plans/2026-08-16-m3-task08-integration-e2e-tests.md` (this file) is committed as the record of what was built.
