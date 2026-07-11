# ADR-004: Per-Service Migration Ownership (Splitting V1–V5)

**Status:** Accepted
**Date:** 2026-07-11

## Context

All schema migrations currently live in one shared directory, `infrastructure/postgres/migrations/`, applied as a single ordered set (`V1`–`V5`) via Postgres's `docker-entrypoint-initdb.d` mount (`docker-compose.yml:14`). As ADR-003 and `docs/milestones/milestone-2/00-overview.md` already note, this mount only runs once per fresh volume — a schema change requires `make down -v && make up`, and there is no version tracking for these five files.

`wallet-service` separately already ships `flyway-core` / `flyway-database-postgresql` with Spring Boot autoconfig pointed at `classpath:db/migration` (`schemas: wallet_db`), but that classpath location is currently empty, so it's a dormant no-op running alongside the shared `initdb.d` mechanism rather than replacing it.

As the project moves past "apply once on a fresh volume," each service should own and apply its own schema migrations independently, using a tool native to its runtime, instead of one shared file-drop.

**Note on SPEC.md:** `SPEC.md` §5.4 (Database topology) explains why there is one Postgres instance with three schemas, but it did not address migration *mechanism* or ownership at all — §6 and §12 only described where DDL lives, not how it's applied per service. This ADR established that reasoning for the first time, rather than superseding an existing spec decision. Now that this ADR is Accepted and implemented, `SPEC.md` §5.4 and §12 cross-reference it, matching how §5.5 cites ADR-001 and §8 cites ADR-002.

## Current file → owner mapping

Reviewed each file's actual contents — no file touches more than one schema:

| File | Creates | Natural owner |
| --- | --- | --- |
| `V1__wallet_schema.sql` | `wallet_db` schema: `wallets`, `idempotency_records`, `wallet_audit_log` | wallet-service |
| `V2__ledger_schema.sql` | `ledger_db` schema: `ledger_transactions`, `ledger_entries`, `account_balances_locked`, `outbox`, balance-check trigger | ledger-service |
| `V3__projection_schema.sql` | `projection_db` schema: `wallet_balances`, `projection_offsets` | projection-service |
| `V4__seed_system_accounts.sql` | seed row in `ledger_db.account_balances_locked` | ledger-service (depends on V2) |
| `V5__ledger_grants.sql` | `ledger_app` role + grants on `ledger_db` | ledger-service (depends on V2) |

`V4` and `V5` both fold into ledger-service's own set, in order, after `V2`. The split is clean — three independent per-service migration sets, still targeting the one shared `ledger` Postgres database (a different schema each).

## Decision

Split into per-service migration directories, each with its own tool and its own version history:

```text
services/wallet-service/src/main/resources/db/migration/
  V1__wallet_schema.sql              (= current V1, unchanged)

services/ledger-service/migrations/
  0001_ledger_schema.up.sql          (= current V2)
  0001_ledger_schema.down.sql        (new)
  0002_seed_system_accounts.up.sql   (= current V4)
  0002_seed_system_accounts.down.sql (new)
  0003_ledger_grants.up.sql          (= current V5)
  0003_ledger_grants.down.sql        (new)

services/projection-service/migrations/
  0001_projection_schema.up.sql      (= current V3)
  0001_projection_schema.down.sql    (new)
```

Tooling:

- **wallet-service** — Flyway, already wired via Spring Boot autoconfig. Just move the file into the classpath location above; no further config change needed.
- **ledger-service** / **projection-service** — `golang-migrate`, run as one-shot `migrate/migrate` CLI containers in `docker-compose.yml` (`ledger-migrate`, `projection-migrate`), gated with `depends_on: condition: service_completed_successfully` on each app service. Chosen over embedding golang-migrate as a library call in each service's own startup path: `projection-service` has no DB/config plumbing at all yet, so embedding would mean inventing that scaffolding purely to run a one-time boot action, and `ledger-service`'s `main.go` was left untouched to avoid friction with in-flight feature work.

Each service's migration tool gets its own schema-history table scoped to its own schema (`wallet_db.flyway_schema_history`, a `ledger_db`-scoped and a `projection_db`-scoped `schema_migrations`), so the three histories never collide despite sharing one physical database.

### Credentials: migration runner vs. app runtime

ledger-service's app already runs as the restricted `ledger_app` role (ADR-003), which has no `CREATE SCHEMA` / `CREATE ROLE` privilege — and `V5` (now `0003_ledger_grants.up.sql`) literally creates that role, so `ledger_app` can never be the one applying its own migrations. The migration runner must keep using the `ledger` owner credentials, exactly as ADR-003 already specifies ("owner role `ledger` is retained for migrations and manual operations only"), while `DATABASE_URL` (app runtime) stays on `ledger_app`. Implemented as two separate connection strings scoped to two separate compose services rather than two env vars on one binary: the `ledger-migrate` one-shot container connects as `ledger` (owner), while `ledger-service` itself keeps `DATABASE_URL` on `ledger_app`.

wallet-service and projection-service don't yet have a restricted app role — both currently connect as the `ledger` owner directly — so no credential split is needed for them yet (see Open questions).

## Rationale

- Matches the runtime each service already has: Flyway is free for Spring, golang-migrate is native to Go. No cross-language tooling forced onto either side.
- Fixes the original gotcha: each service tracks its own applied-version history inside its own schema, so schema changes reapply correctly against an existing volume instead of requiring `make down -v`.
- Nothing in `V1`–`V5` needs cross-schema coordination, so per-service ownership doesn't fight the existing schema design.

## Rejected alternative

Keep one shared migration set applied by a single tool/owner before any service starts (a `migrate` init service in compose running Flyway or golang-migrate against all five files in order). Simpler to operate — one history table, one ordering — but forces one tool onto both runtimes and gives the ledger-service credential split (owner vs. `ledger_app`) no natural home. Rejected in favor of matching migration ownership to service ownership.

## Consequences

- `V4` / `V5` are renumbered as ledger-service's own `0002` / `0003`; content is unchanged, only file naming/location moves.
- `down` migrations must be authored for golang-migrate's up/down convention; none exist today since Flyway (used so far) doesn't require them. These can be minimal (`DROP SCHEMA ... CASCADE`) since rollback-in-production isn't a project goal, but the tool requires them to exist.
- Two Postgres connection strings now exist for ledger-service's data path (the `ledger-migrate` container on owner `ledger`, the app itself on `ledger_app`) instead of one, though neither lives in the app's own config — the split is expressed entirely in `docker-compose.yml`.
- `docker-compose.yml:14`'s `docker-entrypoint-initdb.d` mount is removed entirely once all three services own their migrations; the `postgres` service becomes schema-empty on first boot until each service's migration step runs.
- Test setup (dockertest/testcontainers) can spin an ephemeral Postgres and run the three per-service migration sets against it directly, instead of relying on a pre-seeded volume — this was the original gap that prompted this ADR.

## Open questions

- Should wallet-service and projection-service eventually get their own restricted app roles (mirroring `ledger_app`), splitting migration/app credentials the same way ledger-service does here? Not required for this split, but the same pattern would apply.
- ~~Where does the migration step run for ledger-service/projection-service in compose?~~ Resolved: dedicated one-shot `migrate/migrate` CLI services (`ledger-migrate`, `projection-migrate`) in `docker-compose.yml`, gated via `depends_on: condition: service_completed_successfully`.
