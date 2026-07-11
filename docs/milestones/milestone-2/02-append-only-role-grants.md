# Task 02 — Append-Only DB Role & Grants

**Status:** Done
**Owner:** Ledger Service / Infrastructure
**Depends on:** 01
**Blocks:** 07 (TST-INT-6)
**Spec reference:** [`SPEC.md` §6.2](../../SPEC.md) ("App user has `INSERT, SELECT` ... No `UPDATE`, no `DELETE` ... enforced via Postgres role grants"), NFR-AUDIT-1, NFR-SEC-6

---

## Goal

Make the immutability of `ledger_entries` and `ledger_transactions` a **database-enforced** guarantee, not a convention. Introduce a restricted `ledger_app` login role and run the Ledger Service as it.

## Current state

- Every service connects as the owner superuser-equivalent role `ledger:ledger`.
- `V2__ledger_schema.sql` ends with a comment: `-- Restrict app user ... (Run after creating the app role, e.g. in V5__grants.sql)`. **`V5` does not exist.**

## Steps

1. Create `infrastructure/postgres/migrations/V5__ledger_grants.sql`:
   - `CREATE ROLE ledger_app LOGIN PASSWORD 'ledger_app';` (idempotent guard — skip if exists).
   - `GRANT USAGE ON SCHEMA ledger_db TO ledger_app;`
   - Grant the matrix below.
   - `GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA ledger_db TO ledger_app;` (for `outbox.id` BIGSERIAL).
2. Point the `ledger-service` `DATABASE_URL` in `docker-compose.yml` at `ledger_app` (keep the owner `ledger` role for migrations/manual ops only).
3. Confirm the worker's needs are covered (outbox `UPDATE` to set `published_at`, `DELETE` for retention — Task 05).

### Grant matrix

| Table | SELECT | INSERT | UPDATE | DELETE | Why |
|---|:--:|:--:|:--:|:--:|---|
| `ledger_db.ledger_transactions` | ✅ | ✅ | ❌ | ❌ | Append-only — auditability (NFR-AUDIT-1) |
| `ledger_db.ledger_entries` | ✅ | ✅ | ❌ | ❌ | Append-only — immutable facts |
| `ledger_db.account_balances_locked` | ✅ | ✅ | ✅ | ❌ | Lock target + mutable cached balance (Task 03) |
| `ledger_db.outbox` | ✅ | ✅ | ✅ | ✅ | Mark `published_at`; retention cleanup (Task 05) |

> The `UPDATE`/`DELETE` denial applies **only** to the two ledger tables. `account_balances_locked` and `outbox` are intentionally mutable — they are operational state, not the audit record.

## Acceptance criteria

| Check (connected as `ledger_app`) | Expected |
|---|---|
| `INSERT` into `ledger_transactions` / `ledger_entries` | succeeds |
| `SELECT` from any ledger table | succeeds |
| `UPDATE ledger_db.ledger_entries ...` | `ERROR: permission denied for table ledger_entries` |
| `DELETE FROM ledger_db.ledger_transactions ...` | `ERROR: permission denied` |
| `UPDATE ledger_db.account_balances_locked ...` | succeeds |
| `UPDATE`/`DELETE` on `outbox` | succeeds |
| Ledger-service boots with the new `DATABASE_URL` and serves a posting | succeeds |

## Done when

The ledger-service runs as `ledger_app`, all posting/outbox flows work, and direct `UPDATE`/`DELETE` on the ledger tables is rejected by Postgres.

## Notes & caveats

- **Ordering:** the role must exist before grants — keep both in the same migration, role first.
- **Migration application (updated, see ADR-004):** this grants migration now lives at `services/ledger-service/migrations/0003_ledger_grants.up.sql` (originally `V5__ledger_grants.sql`), applied via golang-migrate rather than `docker-entrypoint-initdb.d`. It reapplies correctly against an existing volume; `make down -v && make up` is no longer required.
- **Default privileges:** these grants cover existing tables. Since the schema is fixed for v1, table-level grants are sufficient; no `ALTER DEFAULT PRIVILEGES` needed.
- Record the rationale as **ADR-0003** in `docs/decisions/`.
