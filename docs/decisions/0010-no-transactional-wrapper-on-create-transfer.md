# ADR-0010: No `@Transactional` wrapper around `CreateTransferUseCase.execute`

**Status:** Accepted
**Date:** 2026-08-01

## Context

`CreateTransferUseCase.execute` performs, in order: local reads (`WalletRepository`, `LedgerEntryRepository`), a local idempotency-record insert (`IdempotencyService.begin`), a remote HTTP call to the Ledger Service (`LedgerClient.postPosting`), and a local idempotency-record update (`IdempotencyService.complete`/`markFailed`). It would be natural to expect a single `@Transactional` around `execute` so the whole operation is all-or-nothing, the same way `GetWalletUseCase` and `CreateWalletUseCase` already use `@Transactional` for their local, single-database operations.

That expectation doesn't hold here, for reasons specific to this use case's shape.

## Decision

Do not annotate `CreateTransferUseCase.execute` (or any method it calls) with `@Transactional`. Leave the local DB writes (`IdempotencyRepository.insertPending`, `.complete`, `.markFailed`) as independently-committing statements, not part of one enclosing transaction.

## Rationale

1. **A local transaction cannot make the whole operation atomic.** `LedgerClient.postPosting`/`getTransaction` are HTTP calls (`RestClient`) to the Ledger Service, which owns a separate database (ADR-0001). Spring's `@Transactional` wraps a single JDBC connection/`DataSource` transaction; it has no effect on a remote HTTP call and cannot roll back a posting the Ledger Service has already committed. There is no XA/2PC spanning `wallet-service`'s DB and `ledger-service`'s DB, and `@Transactional` doesn't create one.

2. **It would break the idempotency in-flight coordination.** `IdempotencyService.begin` inserts a `PENDING` row via `IdempotencyRepository.insertPending` specifically so a *concurrent* request carrying the same `Idempotency-Key` can observe it through `repository.find(...)` and enter the `InProgress` / `awaitCompletion` polling path (see the `CreateTransferUseCase` sequence flow) while the first request is still waiting on the Ledger Service. If `execute` ran inside one enclosing transaction, that insert would not be durably visible to another connection until the entire method — including the network round-trip — commits at the end. A concurrent duplicate request would never observe the `PENDING` row, defeating the coordination mechanism entirely.

3. **Holding a DB transaction open across a network call is an anti-pattern regardless of correctness.** It pins a connection (and any row locks) for the duration of an HTTP round-trip to another service; under load or Ledger Service latency this drains the connection pool for no compensating benefit.

## Alternatives considered

1. **`@Transactional` around all of `execute`** — rejected for reasons 1–3 above.
2. **`@Transactional` around only the local segments** (`validate`'s reads, `begin`, `complete`/`markFailed`) as separate, narrower transactions — effectively already the current behavior, since each `IdempotencyRepository` method commits independently; an explicit annotation would add ceremony without changing behavior, and risks a future edit accidentally widening the scope back to spanning the Ledger Service call.
3. **Distributed transaction (2PC/XA) across `wallet-service` and `ledger-service`** — rejected as out of scope and contrary to ADR-0001's HTTP-based, loosely-coupled service boundary; would also reintroduce the coordinator-availability and cross-service locking problems HTTP was chosen to avoid.

## Consequences

- Atomicity across the process boundary is provided by the deterministic `transactionId` derivation (ADR-0006) plus the idempotency state machine (`New` / `Replay` / `Mismatch` / `InProgress`) plus crash recovery via `ledgerClient.getTransaction(...)` — an idempotent-retry-with-stable-identity pattern, not a database transaction. The Ledger Service's own commit is the real atomicity boundary for the ledger entries; `wallet-service` only needs to reliably converge on "did this already happen, and what was the result," regardless of when the process died.
- A brief window exists where the local `PENDING` record exists but the Ledger Service call hasn't resolved yet (or has resolved but `complete`/`markFailed` hasn't run). This is intentional, not a bug: it's exactly the window `InProgress`/`recoverFromInProgress` is designed to handle.
- Any future local-only use case in this service should still use `@Transactional` where appropriate (as `GetWalletUseCase`/`CreateWalletUseCase` do) — this ADR applies specifically to use cases that call out to another service mid-flow, not as a blanket rule against `@Transactional` in `wallet-service`.

### Amendment (2026-11-14, milestone-7 task 05): scope, and one use case that does not follow it

- The decision applies equally to `ExternalMovementUseCase` (deposits and withdrawals), which has the same
  shape (validate, idempotency `begin`, remote ledger call, `complete`/`markFailed`) and carries no
  `@Transactional`.
- **`RebuildProjectionUseCase.execute` is `@Transactional` and makes an HTTP call inside it**
  (`ProjectionClient.rebuild`, 2 s connect / 5 s read timeouts, between a wallet read and the audit insert).
  That is the pattern rationale 3 above warns about: the transaction pins a Hikari connection (pool of 20) for
  the duration of the call, up to ~7 s in the worst case. It was not a recorded decision (ADR-0016 does not
  discuss it), and nothing in it needs atomicity across the call (the audit row is written *after* a
  successful rebuild by design). It is admin-only and rare, so the impact is small; it is noted here as an
  inconsistency to resolve, most simply by dropping the annotation, not as an accepted exception.
