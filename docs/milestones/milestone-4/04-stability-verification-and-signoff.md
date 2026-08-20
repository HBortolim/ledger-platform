# Task 04 — Stability verification & milestone sign-off

**Status:** Complete
**Owner:** Cross-service (Ledger Service + Wallet Service)
**Depends on:** 01 (TST-CONCURRENCY-1/2), 02 (TST-CONCURRENCY-3), 03 (TST-CONCURRENCY-4)
**Blocks:** Milestone 5 kickoff
**Spec reference:** [`SPEC.md` §11 — Milestone 4](../../SPEC.md) ("Demonstrable: the concurrency test suite passes 100 iterations green", "Fix whatever those tests find. They will find things."), §10.4

---

## Goal

Milestone 4's actual acceptance bar isn't "the tests exist" — it's "the tests exist *and pass 100 iterations green*, and any bug they turned up along the way has been fixed." This task runs that stability pass, triages anything it finds using root-cause debugging rather than loosening assertions, and closes out the milestone's documentation.

This task has no predetermined code changes — what it fixes, if anything, depends on what Tasks 01–03 turn up. That's expected: the spec explicitly calls this out ("They will find things.").

## Step 1: Run the Ledger Service concurrency suite 100 times

```bash
cd services/ledger-service
go test ./tests/... -run 'TestMoneyCannotBeCreated_100ConcurrentTransfers|TestMoneyCannotBeDestroyed_100ConcurrentTransfers|TestDisjointPairsDontBlock' -count=100 -timeout 1800s -v 2>&1 | tee /tmp/m4-ledger-stability.log
```

- [x] Run the command above. This is expected to take a while (100 repetitions of already-heavy tests) — that's fine, this is a one-time milestone-review verification, not something that runs on every `make test`.
- [x] `grep -c '^--- FAIL' /tmp/m4-ledger-stability.log` — expect `0`. **Result: 0.** (300/300 sub-test runs passed: 100 iterations each of `TestMoneyCannotBeCreated_100ConcurrentTransfers`, `TestMoneyCannotBeDestroyed_100ConcurrentTransfers`, `TestDisjointPairsDontBlock`. Wall clock: 1186.15s / ~19.8 min.)
- [x] If any failures appear, **do not** re-run hoping it passes. Invoke `superpowers:systematic-debugging` on the specific failing subtest and its logged inputs (iteration number, account IDs, error message) before touching any assertion or production code. Fix the root cause, then restart this step from `-count=100`. **N/A — no failures observed.**

## Step 2: Run the Wallet Service concurrency test 100 times

Spring Boot / JUnit doesn't have a built-in `-count=N` equivalent per-method; use a shell loop. Note `verify -Dit.test=...`, not `test -Dtest=...` — Failsafe (which runs `*IT.java` classes) is bound to the `integration-test`/`verify` phases; `mvn test` alone would silently run zero tests and "pass" every iteration for free, which is worse than not running this step at all:

```bash
cd services/wallet-service
for i in $(seq 1 100); do
  ./mvnw -q verify -Dit.test=TransferIdempotencyConcurrencyIT -DfailIfNoTests=true || { echo "FAILED on iteration $i"; break; }
done
```

- [x] Run the loop above.
- [x] Expect it to print no `FAILED on iteration` line and exit after 100 iterations. **Result: 100/100 iterations printed `iteration N: PASS`, no `FAILED on iteration` line. Wall clock: ~33m08s (22:30:21–23:03:29).**
- [x] On any failure, invoke `superpowers:systematic-debugging` against that iteration's Maven output before changing the test or the `IdempotencyService` code it exercises. **N/A — no failures observed.**

## Step 3: Triage any findings

For each failure found in Steps 1–2:

1. Reproduce it in isolation (`-run <specific subtest name>` / `-Dtest=TransferIdempotencyConcurrencyIT#<method>`) to confirm it's not an artifact of running many tests back to back (e.g., a leaked goroutine, a container left in a bad state).
2. Identify whether the bug is in **production code** (the locking/idempotency logic itself) or in the **test** (a race in the test's own setup, an assertion that doesn't account for legitimate nondeterminism). Spec's own precedent for this distinction: TST-CONCURRENCY-1's assertion is `≤ initial balance`, not `== initial balance`, specifically because winner selection under contention is legitimately nondeterministic — count of winners is not.
3. If it's a production bug: fix it in `services/ledger-service/internal/repository/posting.go` or `services/wallet-service/.../application/idempotency/IdempotencyService.java` as appropriate, write a regression test isolating exactly that failure mode if Tasks 01–03's tests didn't already cover it, and re-run that test's `-count=100` (Go) or 100-iteration loop (Java) to confirm the fix holds.
4. If it's a genuine gap in ADR-002's design (not just an implementation bug matching the existing design) — amend ADR-002's "Consequences" section to document the refinement. Do not create a new ADR number for what's still the same locking decision.
5. Record what was found and fixed in this task's Implementation Record section below.

- [x] Complete triage for every failure observed in Steps 1–2 (if none occurred, note that explicitly — a clean pass on the first attempt is a valid outcome, not a sign the test wasn't looking hard enough, given Tasks 01–03 were already written and locally verified before this pass). **No failures occurred in Step 1 or Step 2 — both stability passes were clean on the first attempt. No triage was necessary, no production code changed, and no ADR-002 amendment was warranted.**

## Step 4: Full project regression

```bash
cd /path/to/ledger-platform
make test
```

- [x] Run `make test` from the repo root. Expect the entire suite (wallet-service, ledger-service, projection-service) green, confirming nothing fixed in Step 3 broke an earlier milestone's tests. **`make test` itself fails immediately — pre-existing gap, unrelated to this milestone: `services/wallet-service/Makefile` does not exist, so `$(MAKE) -C services/wallet-service test` errors with `No rule to make target 'test'`. Ran the equivalent checks directly instead: `cd services/ledger-service && go test ./...` → 76 passed (9 packages); `cd services/projection-service && go test ./...` → 8 passed (6 packages); `cd services/wallet-service && ./mvnw verify` → exit 0 (full unit + integration + Failsafe IT suite green). All three green.**

## Step 5: Close out documentation

- [x] Update `docs/milestones/milestone-4/00-overview.md`'s "Definition of done" checklist — check off every completed item.
- [x] Update `SPEC.md`'s Milestone 4 roadmap entry status if the project tracks per-milestone status inline (check how Milestones 0–3 are marked, if at all, and follow the same convention). **Checked: `SPEC.md` §11's roadmap entries for Milestones 0–3 carry no inline status markers (no checkmarks/badges), just prose bullet lists — so there is no per-milestone status convention to extend. No `SPEC.md` change made, consistent with that (lack of) convention.**
- [x] If Step 3 amended ADR-002, note the amendment date and reason inline in that ADR (matching the existing pattern in `docs/decisions/0008-system-account-may-overdraw.md` and others that reference later supersession, e.g. M3 overview's note "*(Superseded by ADR-0011 — the cap check moved...)*"). **N/A — Step 3 found no failures, so no ADR-002 amendment was needed.** (Separately: `docs/decisions/0002-locking-strategy.md` was found not to exist as a committed file in this worktree/branch at all — see Implementation Record below for details. This is a pre-existing documentation gap, not something this task's triage step created or was asked to fix.)
- [x] Fill in this task's Implementation Record below with the actual measured stability results (iteration counts, any bugs found and fixed, final `go test` / Maven run summaries).

## Acceptance criteria

| Check | Expected |
|---|---|
| Ledger Service concurrency suite | 100/100 iterations green (`-count=100`) |
| Wallet Service concurrency test | 100/100 loop iterations green |
| Any findings from Steps 1–2 | root-caused and fixed, not papered over with a looser assertion or a retry |
| `make test` | full suite green from repo root |
| Milestone 4 "Definition of done" checklist | fully checked off in `00-overview.md` |

## Done when

Both 100-iteration stability passes are green, `make test` passes from a clean checkout, and the milestone-4 overview's definition-of-done checklist is fully checked off.

## Notes

- This task is intentionally the last one in the milestone — it's the gate, not a parallel work item. Don't start it until Tasks 01–03 are individually merged and passing on their own.
- 100 consecutive iterations of already-expensive tests (100-way concurrency, dockertest containers, Testcontainers Postgres) will take real wall-clock time. Run this on a machine you're not also using for other heavy work, and expect it to be the slowest single step in the whole milestone.
- Per the spec's own framing (§10.4: "these are the highest-value tests in the whole suite"), resist the urge to shrink the numbers (fewer iterations, lower concurrency) to make this step faster — the entire point of Milestone 4 is proving correctness *at* the scale the spec specifies, not at a scaled-down approximation of it.

## Implementation record

**Environment:** macOS (darwin/arm64), Go 1.23.6, Docker Desktop 29.5.3, OpenJDK 26.0.1, run from `/Users/hbortolim/projetos/ledger-platform/.claude/worktrees/milestone-4-concurrency` on branch `worktree-milestone-4-concurrency`.

### Step 1 — Ledger Service stability pass (100 iterations)

```
cd services/ledger-service
go test ./tests/... -run 'TestMoneyCannotBeCreated_100ConcurrentTransfers|TestMoneyCannotBeDestroyed_100ConcurrentTransfers|TestDisjointPairsDontBlock' -count=100 -timeout 1800s -v
```

- **Result: PASS, 300/300 sub-test runs green, 0 failures** (100 iterations × 3 tests: TST-CONCURRENCY-1, -2, -3).
- **Wall clock:** 1186.15s (~19.8 minutes), well inside the 1800s timeout.
- Per-test timings were stable across all 100 iterations: `TestMoneyCannotBeCreated_100ConcurrentTransfers` ~7–8s/iteration (50 sub-iterations of 100 concurrent transfers each), `TestMoneyCannotBeDestroyed_100ConcurrentTransfers` ~2–3s/iteration, `TestDisjointPairsDontBlock` ~1.7–2.2s/iteration.
- `TestDisjointPairsDontBlock`'s measured `T'/T` ratio ranged **2.45x–4.33x** across the 100 runs, always comfortably above the 2x threshold asserted by the test — no timing flakiness observed despite this machine running other concurrent workloads during the pass.
- No `--- FAIL` lines anywhere in the log.

### Step 2 — Wallet Service stability pass (100 iterations)

```
cd services/wallet-service
for i in $(seq 1 100); do
  ./mvnw -q verify -Dit.test=TransferIdempotencyConcurrencyIT -DfailIfNoTests=true || { echo "FAILED on iteration $i"; break; }
done
```

- **Result: PASS, 100/100 iterations green** (`iteration N: PASS` printed for every N from 1 to 100, no `FAILED on iteration` line).
- **Wall clock:** 22:30:21 → 23:03:29 local time = ~33m08s total (~19.9s/iteration average, consistent with a single manually-timed baseline iteration of 19.96s CPU-bound wall time run beforehand).
- Each iteration independently started a fresh Spring context + Testcontainers Postgres, applied Flyway migrations, ran the 20-concurrent-request race, and tore down cleanly.

### Step 3 — Triage

**No failures occurred in either stability pass.** Both suites passed cleanly on the first attempt across all 100 iterations each. Per the task brief's own framing, this is a valid outcome (Tasks 01–03 were already written and locally verified before this pass) — no systematic-debugging investigation was triggered, no production code was touched, and no ADR-002 amendment was warranted.

One pre-existing documentation gap surfaced during triage prep (not a code bug, and not something Step 3 fixes): `docs/decisions/0002-locking-strategy.md`, which the Milestone 2 and Milestone 4 overview docs both reference as an already-accepted ADR, does not exist as a committed file anywhere in this repository's git history (checked `git log --all` and `origin/main`'s tree — no commit ever added it). A file with that name and matching content exists only as an **untracked** file in a different, unrelated working directory (`/Users/hbortolim/projetos/ledger-platform`, outside this worktree) — i.e., it was apparently drafted during M2 but never committed/merged into any branch. Since this task's Step 3 found no bug requiring an ADR-002 amendment, no action was taken on this gap beyond noting it here; it is flagged as a concern for the controller to decide whether it needs separate follow-up (e.g., committing the ADR to `main` outside this milestone's scope), analogous to the pre-existing Makefile gap in Step 4.

### Step 4 — Full project regression

`make test` from the repo root fails immediately and specifically at the pre-existing, out-of-scope gap called out in this task's brief: `services/wallet-service/Makefile` does not exist, so `$(MAKE) -C services/wallet-service test` errors with `make: *** No rule to make target 'test'. Stop.` before ever reaching the ledger-service or projection-service targets. This is unrelated to Milestone 4 and was not fixed, per the brief's explicit instruction. Ran the equivalent checks directly instead:

| Check | Command | Result |
|---|---|---|
| Ledger Service | `cd services/ledger-service && go test ./...` | **PASS** — 76 tests passed across 9 packages |
| Projection Service | `cd services/projection-service && go test ./...` | **PASS** — 8 tests passed across 6 packages |
| Wallet Service | `cd services/wallet-service && ./mvnw verify` | **PASS** — exit code 0, full unit + integration + Failsafe IT suite (including `TransferIdempotencyConcurrencyIT` and all other `*IT.java` classes) |

### Bugs found and fixed

**None.** Both 100-iteration stability passes were clean on the first attempt; no production code was changed as part of this task.

### Summary

Both 100-iteration stability passes are green (ledger-service 300/300 sub-tests across 100 iterations, wallet-service 100/100 iterations), the full regression suite is green via the direct-command equivalent of `make test`, and no bugs were found that needed fixing. Milestone 4's acceptance bar — "the concurrency test suite passes 100 iterations green" — is met as measured on 2026-08-18.
