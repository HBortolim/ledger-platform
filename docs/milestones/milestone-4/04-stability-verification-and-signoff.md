# Task 04 — Stability verification & milestone sign-off

**Status:** Not started
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

- [ ] Run the command above. This is expected to take a while (100 repetitions of already-heavy tests) — that's fine, this is a one-time milestone-review verification, not something that runs on every `make test`.
- [ ] `grep -c '^--- FAIL' /tmp/m4-ledger-stability.log` — expect `0`.
- [ ] If any failures appear, **do not** re-run hoping it passes. Invoke `superpowers:systematic-debugging` on the specific failing subtest and its logged inputs (iteration number, account IDs, error message) before touching any assertion or production code. Fix the root cause, then restart this step from `-count=100`.

## Step 2: Run the Wallet Service concurrency test 100 times

Spring Boot / JUnit doesn't have a built-in `-count=N` equivalent per-method; use a shell loop. Note `verify -Dit.test=...`, not `test -Dtest=...` — Failsafe (which runs `*IT.java` classes) is bound to the `integration-test`/`verify` phases; `mvn test` alone would silently run zero tests and "pass" every iteration for free, which is worse than not running this step at all:

```bash
cd services/wallet-service
for i in $(seq 1 100); do
  ./mvnw -q verify -Dit.test=TransferIdempotencyConcurrencyIT -DfailIfNoTests=true || { echo "FAILED on iteration $i"; break; }
done
```

- [ ] Run the loop above.
- [ ] Expect it to print no `FAILED on iteration` line and exit after 100 iterations.
- [ ] On any failure, invoke `superpowers:systematic-debugging` against that iteration's Maven output before changing the test or the `IdempotencyService` code it exercises.

## Step 3: Triage any findings

For each failure found in Steps 1–2:

1. Reproduce it in isolation (`-run <specific subtest name>` / `-Dtest=TransferIdempotencyConcurrencyIT#<method>`) to confirm it's not an artifact of running many tests back to back (e.g., a leaked goroutine, a container left in a bad state).
2. Identify whether the bug is in **production code** (the locking/idempotency logic itself) or in the **test** (a race in the test's own setup, an assertion that doesn't account for legitimate nondeterminism). Spec's own precedent for this distinction: TST-CONCURRENCY-1's assertion is `≤ initial balance`, not `== initial balance`, specifically because winner selection under contention is legitimately nondeterministic — count of winners is not.
3. If it's a production bug: fix it in `services/ledger-service/internal/repository/posting.go` or `services/wallet-service/.../application/idempotency/IdempotencyService.java` as appropriate, write a regression test isolating exactly that failure mode if Tasks 01–03's tests didn't already cover it, and re-run that test's `-count=100` (Go) or 100-iteration loop (Java) to confirm the fix holds.
4. If it's a genuine gap in ADR-002's design (not just an implementation bug matching the existing design) — amend ADR-002's "Consequences" section to document the refinement. Do not create a new ADR number for what's still the same locking decision.
5. Record what was found and fixed in this task's Implementation Record section below.

- [ ] Complete triage for every failure observed in Steps 1–2 (if none occurred, note that explicitly — a clean pass on the first attempt is a valid outcome, not a sign the test wasn't looking hard enough, given Tasks 01–03 were already written and locally verified before this pass).

## Step 4: Full project regression

```bash
cd /path/to/ledger-platform
make test
```

- [ ] Run `make test` from the repo root. Expect the entire suite (wallet-service, ledger-service, projection-service) green, confirming nothing fixed in Step 3 broke an earlier milestone's tests.

## Step 5: Close out documentation

- [ ] Update `docs/milestones/milestone-4/00-overview.md`'s "Definition of done" checklist — check off every completed item.
- [ ] Update `SPEC.md`'s Milestone 4 roadmap entry status if the project tracks per-milestone status inline (check how Milestones 0–3 are marked, if at all, and follow the same convention).
- [ ] If Step 3 amended ADR-002, note the amendment date and reason inline in that ADR (matching the existing pattern in `docs/decisions/0008-system-account-may-overdraw.md` and others that reference later supersession, e.g. M3 overview's note "*(Superseded by ADR-0011 — the cap check moved...)*").
- [ ] Fill in this task's Implementation Record below with the actual measured stability results (iteration counts, any bugs found and fixed, final `go test` / Maven run summaries).

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

_(Fill in after running Steps 1–4: actual iteration counts, wall-clock time for each stability pass, any bugs found with links to their fixing commits, and confirmation of the final `make test` run.)_
