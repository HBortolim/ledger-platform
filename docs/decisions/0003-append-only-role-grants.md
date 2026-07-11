# ADR-003: Append-Only DB Role & Grants for Ledger Tables

**Status:** Accepted  
**Date:** 2026-07-06

## Context

The Ledger Service connects to Postgres as the owner role `ledger`, which has unrestricted `UPDATE` and `DELETE` on all tables. `ledger_transactions` and `ledger_entries` are the immutable financial audit record — any accidental or malicious mutation would silently violate auditability requirements (NFR-AUDIT-1, NFR-SEC-6). Relying on application convention alone is insufficient; the database should enforce the invariant.

## Decision

Introduce a restricted `ledger_app` login role and run the Ledger Service as it. The role receives only the privileges it needs per table:

| Table | SELECT | INSERT | UPDATE | DELETE |
|---|:--:|:--:|:--:|:--:|
| `ledger_db.ledger_transactions` | ✅ | ✅ | ❌ | ❌ |
| `ledger_db.ledger_entries` | ✅ | ✅ | ❌ | ❌ |
| `ledger_db.account_balances_locked` | ✅ | ✅ | ✅ | ❌ |
| `ledger_db.outbox` | ✅ | ✅ | ✅ | ✅ |

The owner role `ledger` is retained for migrations and manual operations only.

## Rationale

- A Postgres permission error is a hard guarantee; an application-level convention can be bypassed by any future code change or misconfiguration.
- `account_balances_locked` and `outbox` are intentionally mutable operational state, not the audit record — they need `UPDATE` (and `DELETE` for outbox retention cleanup in Task 05).
- Table-level grants are sufficient because the schema is fixed for v1; `ALTER DEFAULT PRIVILEGES` is not needed.

## Rejected alternative

Application-layer guard — rejected because it provides no protection against direct SQL access, future regressions, or operator error.

## Consequences

- The Ledger Service `DATABASE_URL` in `docker-compose.yml` must point to `ledger_app:ledger_app`.
- Applying the grants (now `services/ledger-service/migrations/0003_ledger_grants.up.sql`, originally `V5__ledger_grants.sql`) no longer requires a volume wipe — migrations are applied per-service via golang-migrate rather than `docker-entrypoint-initdb.d`. See ADR-004.
- Any future table added to `ledger_db` must have its grants explicitly assigned to `ledger_app`.
