# Milestone 2 — The Ledger Works

**Status:** Ready to start
**Owner:** Ledger Service
**Spec reference:** [`SPEC.md` §11 — Milestone 2](../../SPEC.md), §3.2 (invariant), §6.2 (ledger schema), §7.1 (Ledger API), §7.4 (outbox worker), §8 (transfer flow), §10.2 (integration tests)
**Estimated effort:** 5 days (per roadmap)

---

## Goal

Make the Ledger Service authoritative and operational: a balanced posting can be written through `POST /ledger/postings`, the immutable double-entry invariant is enforced, the entries are physically append-only, and every committed posting becomes a `LEDGER_POSTED` event on Kafka via the outbox worker.

**Demonstrable (the M2 acceptance bar):**

1. Post a balanced ledger transaction via `curl` → see two rows in `ledger_entries`, one row in `ledger_transactions`, one row in `outbox`, and a message on the `ledger.posted.v1` Kafka topic.
2. Post an unbalanced transaction → it is rejected (`422`), no entries persist.
3. Connected as the application DB role, attempt `UPDATE`/`DELETE` on `ledger_entries` → permission denied.

---

## What already exists vs. what M2 builds

A large part of the "schema" bullet in `SPEC.md` §11 is **already done**. Do not redo it.

| Area | State | Where |
|---|---|---|
| `ledger_transactions`, `ledger_entries` tables | ✅ Done | `services/ledger-service/migrations/0001_ledger_schema.up.sql` (originally `V2__ledger_schema.sql`) |
| Invariant trigger (`check_transaction_balance` + `trg_check_balance`, deferred) | ✅ Done | `0001_ledger_schema.up.sql` |
| `account_balances_locked` table (lock target / cached balance) | ✅ Done | `0001_ledger_schema.up.sql` |
| `outbox` table + `idx_outbox_unpublished` | ✅ Done | `0001_ledger_schema.up.sql` |
| External funding account seed | ✅ Done | `services/ledger-service/migrations/0002_seed_system_accounts.up.sql` (originally `V4__seed_system_accounts.sql`) |
| Domain model + invariant calc (`ValidateBalance`) and `Money` type | ✅ Done | `services/ledger-service/internal/domain/{ledger,money}.go` |
| Health/metrics routes, posting route registration, Dockerfile | ✅ Done | `services/ledger-service/internal/handler/routes.go`, `Dockerfile` |
| **Append-only grants / restricted DB role** | ❌ To build | Task 02 |
| **DB connectivity (pgx pool, config, `/health/ready`)** | ❌ To build (stub) | Task 01 |
| **Posting repository (atomic write, FOR UPDATE, outbox row)** | ❌ To build (empty `internal/repository/`) | Task 03 |
| **Posting service + `POST /ledger/postings` handler** | ❌ To build (returns 501) | Task 04 |
| **`GET /ledger/transactions/{id}` handler** | ❌ To build (returns 501) | Task 04 |
| **Outbox worker (Kafka publish loop)** | ❌ To build (no-op poll) | Task 05 |
| **Required Prometheus metrics** | ❌ To build | Task 06 |
| **Integration tests (TST-INT-2/5/6 + posting + concurrency smoke)** | ❌ To build (empty `tests/`) | Task 07 |

> ℹ️ **Migration ownership (updated, see ADR-004).** The shared `infrastructure/postgres/migrations/*.sql` set described above no longer exists. Each service now owns and applies its own migration set — Flyway for wallet-service (`services/wallet-service/src/main/resources/db/migration/`), golang-migrate for ledger-service and projection-service (`services/<service>/migrations/`) — each tracked with its own version history, so schema changes reapply correctly against an existing volume without requiring `make down -v`.

---

## Decisions for this milestone

| # | Decision | Rationale | Reference |
|---|---|---|---|
| 1 | Add a restricted **`ledger_app`** login role (no `UPDATE`/`DELETE` on the immutable ledger tables) and connect the ledger-service as it. | Makes append-only a real DB-enforced guarantee, not a convention; TST-INT-6 becomes meaningful end-to-end. | `SPEC.md` NFR-AUDIT-1, §6.2; Task 02 |
| 2 | Kafka client: **`twmb/franz-go`**. | Pure Go, CGO-free — preserves the `CGO_ENABLED=0` Dockerfile build. | Task 05 |
| 3 | Go integration tests use **`testcontainers-go`**. | Consistency with the wallet-service's Testcontainers; active maintenance. Intentionally diverges from `SPEC.md` §10.2's literal "dockertest". | Task 07 |
| 4 | Include the **`SELECT ... FOR UPDATE` available-balance check** + `account_balances_locked` update inside the M2 posting. | Pulls part of M4 forward so the posting is correct under contention from day one. Daily-cap stays in the Wallet Service per the §5.2 ownership map and is **out of scope** here. *(Superseded by ADR-0011 — the daily cap later moved into the Ledger Service's locked transaction.)* | `SPEC.md` §8 (steps 7–8), ADR-002; Task 03/04 |

### New ADRs to write during M2

- **ADR-0003 — Append-only enforcement via a restricted DB role.** Document the `ledger_app` grant matrix and why DB-level enforcement (not application convention) is used.
- **ADR-0004 — `testcontainers-go` over `dockertest` for Go integration tests.** Record the divergence from `SPEC.md` §10.2 and the consistency rationale.

Place both in `docs/decisions/` following the existing ADR format (`docs/decisions/0001-http-not-grpc.md`, `0002-locking-strategy.md`).

---

## Task order & dependencies

```
01 db-connectivity ──► 02 append-only-role-grants ──► 03 posting-repository ──► 04 posting-service-and-handler ──► 05 outbox-worker-kafka
                                                              │                          │                                   │
                                                              └──────────────► 06 metrics (folds into 04 + 05)               │
                                                                                                                             ▼
                                                                              07 integration-tests (layered on as 02–05 land)
```

- **01** unblocks everything (no posting without a DB pool).
- **02** must land before **07**'s TST-INT-6 can assert grant enforcement, and before the ledger-service runs as `ledger_app`.
- **03 → 04** is the posting vertical slice; **05** drains what **04** writes.
- **06** is small and is satisfied incrementally inside **04** and **05**.
- **07** is written progressively: TST-INT-5 needs only the schema (available immediately), TST-INT-6 needs **02**, the posting/happy-path needs **04**, TST-INT-2 needs **05**.

---

## Definition of done (milestone)

- [ ] `ledger_app` role exists with the documented grant matrix; ledger-service connects as it (Task 02).
- [ ] `POST /ledger/postings` returns the `SPEC.md` §7.1 shapes for 201 / 409 / 422 / 503 (Task 04).
- [ ] `GET /ledger/transactions/{id}` returns a committed transaction (recovery path §9.2) (Task 04).
- [ ] A committed posting produces an outbox row that the worker drains to `ledger.posted.v1` (Task 05).
- [ ] Required ledger metrics are exposed on `/metrics` (Task 06).
- [ ] TST-INT-2, TST-INT-5, TST-INT-6, posting happy-path, and the concurrency smoke pass (Task 07).
- [ ] Domain unit-test coverage ≥ 80% per `SPEC.md` §10.1.
- [ ] ADR-0003 and ADR-0004 written.

---

## Demo script (run at milestone review)

```sh
# 0. Fresh stack
make down && make up        # rebuilds; each service applies its own migrations (ADR-004)

# 1. Post a balanced transfer directly to the Ledger Service
TX=$(uuidgen)
curl -s -X POST http://localhost:8081/ledger/postings \
  -H "Content-Type: application/json" \
  -d "{\"transactionId\":\"$TX\",\"type\":\"TRANSFER\",\"description\":\"demo\",
       \"entries\":[
         {\"accountId\":\"aaaaaaaa-0000-0000-0000-000000000001\",\"entryType\":\"DEBIT\",\"amount\":\"100.00\"},
         {\"accountId\":\"aaaaaaaa-0000-0000-0000-000000000002\",\"entryType\":\"CREDIT\",\"amount\":\"100.00\"}]}"

# 2. Inspect the ledger, outbox, and Kafka
psql "postgres://ledger:ledger@localhost:5432/ledger" \
  -c "SELECT entry_type, amount FROM ledger_db.ledger_entries WHERE transaction_id='$TX';" \
  -c "SELECT topic, published_at FROM ledger_db.outbox ORDER BY id DESC LIMIT 1;"
# message appears on ledger.posted.v1 (use kafka-console-consumer or the franz-go consumer)

# 3. Post an unbalanced transaction -> 422, nothing persists
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:8081/ledger/postings \
  -H "Content-Type: application/json" \
  -d "{\"transactionId\":\"$(uuidgen)\",\"type\":\"TRANSFER\",
       \"entries\":[
         {\"accountId\":\"aaaaaaaa-0000-0000-0000-000000000001\",\"entryType\":\"DEBIT\",\"amount\":\"100.00\"},
         {\"accountId\":\"aaaaaaaa-0000-0000-0000-000000000002\",\"entryType\":\"CREDIT\",\"amount\":\"90.00\"}]}"
# 422

# 4. Prove append-only as the app role
psql "postgres://ledger_app:ledger_app@localhost:5432/ledger" \
  -c "UPDATE ledger_db.ledger_entries SET amount = 1 WHERE transaction_id='$TX';"
# ERROR: permission denied for table ledger_entries
```
