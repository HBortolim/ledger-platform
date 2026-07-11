# Task 07 — Tests (integration + unit)

**Status:** Not started
**Owner:** Ledger Service
**Depends on:** 02 (TST-INT-6), 04 (posting/happy-path), 05 (TST-INT-2); TST-INT-5 needs only the schema
**Blocks:** milestone sign-off
**Spec reference:** [`SPEC.md` §10.1 (unit)](../../SPEC.md), §10.2 (integration: TST-INT-2/5/6), §10.4 (concurrency), §3.2 (invariant)

---

## Goal

Prove the milestone with real infrastructure: a `testcontainers-go` harness (real Postgres + real Kafka) plus domain unit tests. These tests are the artifact that shows a reviewer the ledger is correct and immutable.

## Test framework (decision #3)

- **`testcontainers-go`** for integration tests (consistency with the wallet-service's Testcontainers), under `services/ledger-service/tests/`.
- The harness must apply `services/ledger-service/migrations/` (via golang-migrate, per ADR-004) so the `ledger_app` role and grants are present — this is exactly what `services/ledger-service/tests/migrations_test.go` already does.
- Record the divergence from `SPEC.md` §10.2's literal "dockertest" as **ADR-0004**.

## Integration scenarios

| ID | Scenario | Asserts | Needs |
|---|---|---|---|
| **TST-INT-5** | Direct DB insert of an unbalanced transaction | the deferred `trg_check_balance` trigger aborts the commit; nothing persists | schema only |
| **TST-INT-6** | Connected as `ledger_app`, attempt `UPDATE`/`DELETE` on `ledger_entries` | both raise `permission denied`; `INSERT`/`SELECT` still work | Task 02 |
| **TST-INT-2** | After a worker tick, a committed posting's outbox row | has `published_at` set; the message is present on `ledger.posted.v1` | Tasks 04, 05 |
| **Posting happy-path** (M2 analog of TST-INT-1) | `POST /ledger/postings` with a balanced body | `201`; exactly 1 `ledger_transactions` + 2 `ledger_entries` + 1 `outbox` row | Task 04 |
| **Concurrency smoke** | Two concurrent same-source postings that jointly overdraw | exactly one `201`, one `422 INSUFFICIENT_FUNDS`; never both succeed (foreshadows TST-CONCURRENCY-1) | Tasks 03, 04 |

> The full 50-iteration concurrency suite (TST-CONCURRENCY-1..4) is **M4**. The smoke test here just guards the FOR-UPDATE path pulled forward by decision #4.

## Unit tests (`SPEC.md` §10.1)

- `domain` package: invariant calc (`ValidateBalance` balanced vs. unbalanced), sign logic (DEBIT/CREDIT → signed amount), `Money` parsing (rejects `"100"`, `"100.5"`, non-positive; accepts `"100.00"`).
- Service-layer mapping: repository errors → HTTP outcomes (table in Task 04), using a faked repository.
- Target: **≥ 80% line coverage on domain packages** (infrastructure/handler coverage not gated).

## Acceptance criteria

| Check | Expected |
|---|---|
| `make test` (ledger-service portion) | all integration + unit tests green |
| TST-INT-5 | unbalanced insert rejected by trigger |
| TST-INT-6 | `UPDATE`/`DELETE` on ledger tables denied as `ledger_app` |
| TST-INT-2 | outbox row published + Kafka message observed |
| Posting happy-path | exact row counts as specified |
| Concurrency smoke | one success, one `INSUFFICIENT_FUNDS`, repeatable |
| Domain coverage | ≥ 80% |

## Done when

The integration suite (TST-INT-2/5/6 + posting + concurrency smoke) and domain unit tests pass against real Postgres + Kafka via `make test`, and coverage meets the §10.1 bar.

## Notes

- Reuse a single container set per test package where possible (start Postgres/Kafka once) to keep the suite fast; apply migrations on startup.
- TST-INT-6 specifically requires connecting as `ledger_app` — assert grants, not application logic. This is the test that makes the append-only guarantee real.
- Keep test data accounts distinct from the V4-seeded external account to avoid cross-test interference.
