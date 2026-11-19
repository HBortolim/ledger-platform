# ADR-0007: The Wallet Service reads projections and ledger entries via SQL, not HTTP

**Status:** Accepted
**Date:** 2026-11-14 (decided in Milestone 3, overview decision #2 — planned there and never written; recorded now in Milestone 7 Task 05)

## Context

`GET /wallets/{id}/balance` (FR-2) must return the projected balance and say whether that projection is
behind the ledger (`"stale"`, AC-2.4). Answering needs two facts the Wallet Service does not own:

1. the projection row, `projection_db.wallet_balances` (`balance`, `last_applied_at`), owned by the
   Projection Service;
2. the newest ledger entry for the wallet, `max(ledger_db.ledger_entries.created_at)`, owned by the Ledger
   Service.

SPEC.md §7.2 sketches an HTTP route for the first (`GET /projections/wallets/{walletId}` on the Projection
Service, called by the Wallet Service) and §6.3 describes the second as a join against `ledger_entries`. The
two sources live in the same Postgres instance, one schema per service (SPEC §5.4, ADR-0004). The Wallet Service
needs to decide how to reach them.

A second need appeared later: closing a wallet (FR-4) requires the wallet's *ledger-derived* balance to be zero
(AC-4.1), which also reads `ledger_entries`.

## Decision

The Wallet Service reads both tables **directly with SQL**, over its own datasource, as the restricted role
`wallet_app`, which has read-only grants on exactly those two tables:

| Grant | Owner of the object | Migration |
|---|---|---|
| `USAGE` on `projection_db`, `SELECT` on `projection_db.wallet_balances` | Projection Service | `projection-service/migrations/0003_wallet_read_grants` |
| `USAGE` on `ledger_db`, `SELECT` on `ledger_db.ledger_entries` | Ledger Service | `ledger-service/migrations/0004_wallet_read_grants` |

Each service grants its **own** objects to `wallet_app` (the ownership rule in ADR-0004's amendment). Nothing
here grants a write.

`BalanceJdbcRepository.getByWalletId` does the freshness join in one statement: the projection row (`LEFT JOIN`
from a one-row `VALUES` so a wallet with no projection row yields a row of nulls), a scalar subquery for the
newest entry's `created_at`, and the database's own `now()` (so the staleness comparison never depends on a
service clock, SPEC §9.10). `GetBalanceUseCase` then applies the policy:

- no projection row, or null balance → balance `0.00`, `asOf` = the wallet's creation time (SPEC §7.2's "a
  wallet with no transactions has a known balance of zero", with no 404 to translate);
- `stale` is true when the newest ledger entry is **later than the projection's `last_applied_at` and older than
  5 seconds** (`STALE_THRESHOLD`), measured against the database clock. An entry newer than 5 s is "in flight",
  not stale; a projection that has caught up is never stale.

The close-wallet check uses `BalanceRepository.getLedgerBalance`, a `SUM` over the wallet's ledger entries,
deliberately **not** the projection: a stale projection must not let a funded wallet be closed.

## Rejected alternative

**An HTTP read path.** The Wallet Service would call the Projection Service for the balance (SPEC §7.2) and the
Ledger Service for the newest entry's timestamp. Rejected for v1 because it adds two network hops, two
timeouts and two new failure modes to a read path, and buys nothing: the data is already in one Postgres
instance, the join in §6.3 is a single statement, and the freshness comparison is *more* correct when both
sides are read by one statement under one database clock than when assembled from two HTTP responses with
different timestamps. It would also have needed a new unauthenticated Projection Service route that nothing
else uses.

## Consequences

- **The Wallet Service has read access to two other services' tables.** That is a deliberate, narrow
  exception to schema ownership (SPEC §5.2): reads of *derived or append-only* data, never writes, so the
  write-ownership boundaries are untouched. The grants are the whole of the coupling. `BalanceJdbcRepositoryIT` proves the queries run
  as `wallet_app` with read-only access, but it emulates the two grants itself; nothing asserts the real migrations
  `0003`/`0004` (the Go services' migration tests cover other grants), so a dropped grant would be caught first
  by the end-to-end suite, not by a unit-level test.
- **`GET /balance` does not depend on the Projection or Ledger Service being up**, only on Postgres.
  NFR-AVAIL-4's `503 + Retry-After` is for the write path and the rebuild call.
- **SPEC.md §7.2's `GET /projections/wallets/{walletId}` was therefore never built.** The Projection Service's
  only HTTP API besides health and metrics is the admin rebuild. §7.2 (and a stale comment in the Projection
  Service's `routes.go`) still describe the endpoint.
- **The one exception in the other direction is the rebuild** (ADR-0016): it *writes* (recomputes and
  overwrites a wallet's projection row), so it goes through the Projection Service's HTTP API, which owns that
  write, instead of the Wallet Service gaining a write grant on `projection_db`.
- A schema change to `wallet_balances` or `ledger_entries` (for example Task 08's `last_seq` and `seq`)
  must keep the columns the Wallet Service selects (`balance`, `last_applied_at`; `account_id`, `entry_type`,
  `amount`, `created_at`); the dockertest and Spring integration suites are what catch a break.
