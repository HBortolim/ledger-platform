# Milestone 4 — Concurrency Correctness

**Status:** Complete
**Owner:** Ledger Service (Tasks 01–02) + Wallet Service (Task 03) + cross-service sign-off (Task 04)
**Spec reference:** [`SPEC.md` §11 — Milestone 4](../../SPEC.md), AC-5.17/5.18 (concurrency ACs), §9.4 (the canonical race), §10.4 (TST-CONCURRENCY-1..4), ADR-002 (locking strategy)
**Estimated effort:** 4 days (per roadmap) — reduced in practice; see below

---

## Goal

Prove, at the spec's actual scale, that the locking mechanism already built cannot let money be created or destroyed under concurrent load, that unrelated wallets never block each other, and that idempotency holds when the same key arrives many times at once. Fix anything the tests find.

**Demonstrable (the M4 acceptance bar):** the concurrency test suite (TST-CONCURRENCY-1..4) passes 100 iterations green.

---

## What already exists vs. what M4 builds

M4's headline mechanism — the account lock row plus `SELECT ... FOR UPDATE` — was **pulled forward into M2** (M2 overview, decision #4) specifically so posting was correct under contention from day one. M4 was never a green-field milestone; it is a scale-up of tests that already exist as smoke tests, plus one new idempotency-under-concurrency test that lives in a different service than the other three.

| Area | State | Where |
|---|---|---|
| `account_balances_locked` lock table | ✅ Done (M2) | `services/ledger-service/migrations/0001_ledger_schema.up.sql` |
| `SELECT ... FOR UPDATE`, sorted lock order (deadlock avoidance), available-balance check | ✅ Done (M2) | `services/ledger-service/internal/repository/posting.go` (`lockAccounts`, `sortedAccountIDs`) |
| Daily cap check inside the same locked transaction (closes the TOCTOU race) | ✅ Done (M3, ADR-0011) | `services/ledger-service/internal/repository/posting.go` (`checkDailyTransferCap`) |
| Concurrency smoke tests: 2 concurrent same-source debits, 2 concurrent near-cap transfers, 25-iteration opposite-order deadlock check | ✅ Done (M2 Task 07) | `services/ledger-service/tests/posting_concurrency_test.go` |
| Idempotency PENDING/COMPLETED/FAILED state machine, `awaitCompletion` 5s wait loop (AC-5.16), `DuplicateKeyException` race handling | ✅ Done (M3 Task 04) | `services/wallet-service/.../application/idempotency/IdempotencyService.java` |
| **TST-CONCURRENCY-1 at spec scale (100 concurrent, 50 iterations, money cannot be created)** | ❌ To build (2-request smoke version only) | Task 01 |
| **TST-CONCURRENCY-2 (money cannot be destroyed, conservation across 100 concurrent transfers)** | ❌ To build (does not exist) | Task 01 |
| **TST-CONCURRENCY-3 (disjoint pairs don't block — throughput/timing proof)** | ❌ To build (does not exist) | Task 02 |
| **TST-CONCURRENCY-4 (idempotency under concurrency — 20 parallel requests, same key)** | ❌ To build (does not exist) | Task 03 |
| **Stability verification: suite green across 100 runs; fix whatever it finds** | ❌ To build | Task 04 |

---

## Decisions for this milestone

| # | Decision | Rationale | Reference |
|---|---|---|---|
| 1 | No new production locking code. M4 is a **test-only milestone** unless the scaled-up tests surface a real bug in the M2/M3 locking or idempotency code — in which case the fix belongs to whichever task's test caught it. | The mechanism was deliberately pulled forward in M2 (see table above); re-implementing it here would just be redundant churn. | ADR-002 |
| 2 | TST-CONCURRENCY-1/2/3 need more than the default Postgres connection budget: 100 concurrent `Post()` calls each hold a connection for the lifetime of one DB transaction. A fresh `setupLedgerDBHighConcurrency(t)` test helper raises the dockertest Postgres container's `max_connections` and the app's `pgxpool.MaxConns`, used only by these heavier tests — the existing `setupLedgerDB(t)` (and everything that depends on its current connection budget) is untouched. | Postgres's default `max_connections=100` (minus reserved superuser slots) is not enough headroom for 100 simultaneous app-side transactions plus the harness's own migration connection; hitting the limit would produce flaky `"sorry, too many clients already"` failures that look like — but aren't — the bug the test is trying to catch. | Task 01 |
| 3 | The new Wallet Service concurrency test (Task 03) must run **outside** `BaseIntegrationTest`'s class-level `@Transactional`. It uses `@Transactional(propagation = Propagation.NOT_SUPPORTED)` on the test method, while still reusing `BaseIntegrationTest`'s singleton Postgres container and Spring context. | `@Transactional` test rollback binds the *test method's own thread* to one uncommitted transaction. Worker threads spawned inside the test get their own separate connections and can't see uncommitted rows from the main thread (e.g. the wallets the test just created) — the test would either deadlock, 404 on wallet lookups, or silently not exercise real cross-connection concurrency at all. Opting out per-test keeps every other IT's fast rollback-based isolation intact. | Task 03 |
| 4 | TST-CONCURRENCY-3's "don't block" claim is proven by wall-clock comparison, not a mocked clock: time 50 concurrent transfers across 50 distinct wallet pairs (`T`), then time 50 concurrent transfers from one shared source (`T'`), and assert `T' > 2×T`. | This is literally what the spec scenario describes (§10.4). A ratio-based assertion is more robust to CI machine variance than a fixed millisecond ceiling on either phase alone. | Task 02 |
| 5 | TST-CONCURRENCY-1 is 50 iterations of exactly 100 concurrent transfers per the spec table; TST-CONCURRENCY-2 is a single run of 100 concurrent transfers (the spec doesn't repeat "50 iterations" for it). No task inflates scope beyond what §10.4 states. | Matches the spec table literally; iteration count is itself part of the acceptance bar, not a free parameter. | Task 01 |

No new ADRs are required to *start* this milestone — Task 04 documents the exception: if the stability run in Task 04 finds a genuine design gap in ADR-002 (not just a test bug), amend ADR-002's Consequences section rather than writing a new ADR for what is still the same locking decision.

---

## Task order & dependencies

```
01 money-conservation-tests (TST-CONCURRENCY-1, -2) ──┐
02 disjoint-pairs-throughput-test (TST-CONCURRENCY-3) ─┼──► 04 stability-verification-and-signoff
03 idempotency-concurrency-test (TST-CONCURRENCY-4) ───┘
```

- **01** and **02** both live in `services/ledger-service/tests/` and both need the high-concurrency DB helper from decision #2 — build the helper once in **01**, reuse it in **02**.
- **03** is fully independent (Wallet Service, Java) — can be done in parallel with 01/02.
- **04** needs all three test suites to exist before it can run the 100-iteration stability pass and decide what (if anything) needs fixing.

---

## Definition of done (milestone)

- [x] TST-CONCURRENCY-1: 50 iterations of 100 concurrent transfers from an under-funded wallet; every iteration has `successes × amount ≤ initial balance`, no unhandled errors (Task 01).
- [x] TST-CONCURRENCY-2: 100 concurrent transfers between two wallets; `Σ(source_initial − source_final) == Σ(dest_final − dest_initial)` (Task 01).
- [x] TST-CONCURRENCY-3: 50 concurrent transfers across 50 disjoint wallet pairs complete in time `T`; 50 concurrent transfers from one shared source complete in `T' > 2×T` (Task 02).
- [x] TST-CONCURRENCY-4: the same `Idempotency-Key` submitted 20 times in parallel produces exactly one ledger transaction and 20 byte-identical responses (Task 03).
- [x] The full suite (Tasks 01–03) passes 100 consecutive runs with zero flakes; any bug it found along the way is fixed and re-verified (Task 04). **Verified 2026-08-18: ledger-service 300/300 sub-tests (100 iterations × 3 tests, 0 failures, ~19.8 min); wallet-service 100/100 iterations (0 failures, ~33 min). No bugs found, so nothing needed fixing. See Task 04's Implementation Record for full detail.**
- [x] `SPEC.md` §11 Milestone 4 checklist and the roadmap's "Demonstrable" line can be checked off honestly. **Confirmed honest: the concurrency test suite passes 100 iterations green, as measured above.**

---

## Demo script (run at milestone review)

```sh
# 0. Ledger-service concurrency suite, verbose, once
cd services/ledger-service
go test ./tests/... -run 'TestMoneyCannotBeCreated|TestMoneyCannotBeDestroyed|TestDisjointPairsDontBlock' -v

# 1. Same suite, 100 times, to prove it's not flaky (Task 04's stability bar)
go test ./tests/... -run 'TestMoneyCannotBeCreated|TestMoneyCannotBeDestroyed|TestDisjointPairsDontBlock' -count=100
cd ../..

# 2. Wallet-service idempotency-under-concurrency test, once
cd services/wallet-service
./mvnw verify -Dit.test=TransferIdempotencyConcurrencyIT -DfailIfNoTests=true
cd ../..

# 3. Full project test suite still green end to end
# `make test` is currently broken at the repo root (services/wallet-service/Makefile is
# missing — a pre-existing gap, out of scope for this milestone; see Task 04's Implementation
# Record). These are the direct commands Task 04 verified work in its place:
cd services/ledger-service && go test ./...
cd ../projection-service && go test ./...
cd ../wallet-service && ./mvnw verify
```
