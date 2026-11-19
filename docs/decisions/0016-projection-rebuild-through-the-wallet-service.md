# ADR-0016: Projection rebuild is exposed through the Wallet Service, not authenticated directly

**Status:** Accepted
**Date:** 2026-10-03

## Context

`docs/SPEC.md` §9.7 describes the projection-recovery endpoint and says it is "admin-only and
emits a clear audit event." §5.2 says the opposite about who may reach the Projection Service at
all: "clients only ever talk to the Wallet Service" — the Projection Service is an internal
component with no client-facing contract, no auth concern, and (per §7.2's existing balance-read
split) no awareness of what its caller is allowed to do.

Both statements are taken as binding, which means neither can be read as overriding the other:

- If the Projection Service enforces "admin-only" itself, it needs to verify a JWT, which means it
  needs the public key, a `role` claim parser, and its own authorization policy — the first auth
  code to exist outside the Wallet Service, and a second place that policy (who counts as "admin")
  can drift out of sync with the first.
- If the Wallet Service is the only thing clients reach, but the mechanism that actually
  recomputes a balance lives in `projection-service/internal/repository/rebuild.go` (Task 04) —
  built there because that is where the ledger-replay logic and `wallet_balances` write access
  already are — then something has to bridge the two without either service taking on the other's
  job.

There is also no cross-service transaction available: a rebuild happens in the Projection
Service's database, the audit row §9.7 requires belongs in `wallet_db.wallet_audit_log`, and those
are two different Postgres roles with no shared transaction boundary (ADR-0004's per-service
migration ownership is the same reasoning applied to schemas; this is its runtime consequence).

## Decision

**Two endpoints, each owning one half of §9.7:**

1. **`POST /admin/projections/{walletId}/rebuild` on the Projection Service** (Task 04) —
   internal, unauthenticated by design, reachable only on the compose network. It does the actual
   work: lock the wallet's row, replay `ledger_db.ledger_entries`, write the recomputed balance.
   It knows nothing about who is allowed to call it, exactly as it knows nothing about who is
   allowed to read a balance (§7.2).
2. **`POST /admin/projections/{walletId}/rebuild` on the Wallet Service** (Task 05, this task) —
   the client-facing one. `SecurityConfig` gates `/admin/**` behind `hasRole("ADMIN")`.
   `RebuildProjectionUseCase` proxies to endpoint 1 via `ProjectionClient`, then — only after that
   call succeeds — writes the `PROJECTION_REBUILD` audit row with the calling admin as actor
   (Task 01 BR-A16's sanctioned exemption from "audit in the same transaction": there isn't one
   here, so audit-after-success is the best available ordering).

The two paths are given **the identical URL path** on purpose — `/admin/projections/{walletId}/rebuild`
— so that §9.7's own text is, verbatim, the path an operator or a future reader actually calls;
only the host and the presence of an `Authorization` header differ. This is the same
policy/mechanism split §7.2 already established for balance reads (Projection Service serves the
raw fact; Wallet Service applies policy and owns the client contract), reapplied to a write.

### Why not enforce admin-only at the Projection Service

Rejected: giving the Projection Service a JWT verifier means two independent places decide "is
this caller an admin," which can only ever be kept in sync by discipline, not by the type system —
exactly the failure mode ADR-0004 and the wallet-service-owns-policy pattern elsewhere in this
codebase exist to avoid. It would also mean the one Go service in this platform that has never
needed to know about JWTs (its balance-read endpoint doesn't check one either) suddenly needs the
RSA public key, a claims parser, and a role-matching policy — for one admin action, duplicating
machinery the Wallet Service already has.

### Why not a shared internal secret

Rejected: a static bearer token or shared key between the two services solves "is this call
internal" but not "is this caller an admin" — it would need to ride alongside the real JWT anyway,
making this the *second* auth mechanism in the platform rather than a substitute for one. §9.7
never describes such a secret, and inventing one here means every future internal call either
needs it too (sprawl) or the inconsistency of some internal calls being secured and others not.

## Consequences

- **The Projection Service's rebuild endpoint is reachable by anything on the compose network, not
  just the Wallet Service.** In this deployment model — a single Docker Compose network with no
  other tenants — that is accepted as the cost of keeping the Projection Service auth-free. A rogue
  or compromised container on the same network could trigger a rebuild directly, bypassing the
  audit row entirely. Rebuild is a read of the ledger followed by an idempotent overwrite of one
  wallet's cached balance (Task 04's pre-lock design) — not a money-moving operation — so the
  blast radius of that gap is "a stale audit trail for a legitimate-looking recomputation," not
  "funds at risk."
- **What would change in a real multi-tenant deployment:** network policy or mTLS restricting which
  callers can reach the Projection Service's internal port at all — not a second JWT verifier.
  That keeps the fix at the infrastructure layer, where "who is on this network" belongs, rather
  than teaching a second service the Wallet Service's authorization model.
- **No `depends_on` between the two services in `docker-compose.yml`.** The only call the Wallet
  Service ever makes to the Projection Service process is this one admin path (the balance-read
  endpoint reads `projection_db.wallet_balances` by direct cross-schema SQL, never over HTTP), and
  that path is built to degrade to `503 Retry-After` (NFR-AVAIL-4) rather than require the
  Projection Service to be up. Declaring a startup dependency would assert a coupling the design
  deliberately doesn't have, for no benefit `service_started` could actually deliver (it orders
  container creation, not readiness).
- **A rebuild that succeeds upstream but whose audit insert then fails produces a `500` and no
  audit row.** Documented and accepted (Task 05, Step 3): there is no atomic way to cover both
  writes, and Task 06's reconciliation job observes the system is consistent regardless of whether
  the audit row exists.

### Amendment (2026-11-14, milestone-7 task 05): what changed underneath, and two imprecise claims

- **The rebuild is exact under consumer lag since Task 08 (ADR-0017).** It now creates and locks the wallet's
  projection row first (`sequence.LockWallet`), replays the ledger in per-account `seq` order, writes
  `last_seq`, discards parked entries it covers and drains the contiguous ones. The "Task 04's pre-lock design"
  wording above predates this; the earlier residual window for a wallet with no projection row is closed.
- **There is no `GET /projections/wallets/{id}` endpoint.** The "§7.2 balance-read split" this ADR leans on
  (SPEC.md §7.2) was never built as HTTP: the Wallet Service reads `wallet_balances` by SQL (ADR-0007), so the
  rebuild is the *only* HTTP call from the Wallet Service to the Projection Service, which is what the
  `depends_on` bullet already says. The policy-versus-mechanism split still holds; only the transport differs.
- "Task 06's reconciliation job observes the system is consistent regardless of whether the audit row exists"
  is loose. Reconciliation compares ledger, projection and money supply; it does not look at
  `wallet_audit_log`. A rebuild without an audit row is not detected by it.
- `RebuildProjectionUseCase` is annotated `@Transactional` around the HTTP call; see the ADR-0010 amendment.
