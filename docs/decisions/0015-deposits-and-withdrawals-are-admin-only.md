# ADR-0015: Deposits and withdrawals are admin-only, with no owner-authorized withdrawal path

**Status:** Accepted
**Date:** 2026-09-19

## Context

`docs/SPEC.md` §4.1 FR-6 explicitly scopes `POST /deposits` as admin-only. FR-7 says withdrawals
are "symmetric" to deposits without stating what "symmetric" qualifies — the entry-pair direction,
the admin-only gate, or both. Left unresolved, that ambiguity has two plausible readings:

1. **Symmetric shape, same gate:** withdrawals are the mirror entry pair (`DEBIT` wallet, `CREDIT`
   system account) and also admin-only, matching FR-6.
2. **Symmetric shape, owner-authorized:** withdrawals are the mirror entry pair but a wallet's
   *owner* can self-service a cash-out, since — unlike a deposit, which necessarily requires
   someone else's authority to conjure money into the platform — a withdrawal only moves the
   owner's own money out.

Both readings are defensible reads of the spec text alone. The decision needs to be made before
`ExternalMovementUseCase`'s validation step can be written, since it determines whether the
wallet lookup is the ownership-checked `walletRepository.getById(walletId, ownerId)` (like
`CreateTransferUseCase`'s source-wallet check) or the owner-agnostic `getById(walletId)`.

## Decision

Withdrawals are admin-only, identically to deposits: reading #1. `ExternalMovementUseCase` uses
the owner-agnostic `walletRepository.getById(walletId)` for both directions, and
`SecurityConfig` gates both `POST /deposits` and `POST /withdrawals` behind `hasRole("ADMIN")`.

An admin acting on a wallet it does not own is expected, correct behavior here — unlike
`CreateTransferUseCase`'s `WalletAccessDeniedException` (AC-5.11), a non-existent or
not-owned-by-caller wallet on these two endpoints returns `422 WALLET_NOT_ACTIVE`, never `403`:
the no-existence-leak rule exists to protect *users* probing each other's wallets, and an admin is
already trusted with the whole wallet table, so a `403` here would be actively misleading about
what actually went wrong.

## Alternatives considered

1. **Owner-authorized withdrawal (reading #2)** — rejected for v1. It reads as a legitimate future
   feature, not a contradiction of FR-7, but building it now means solving two things this task
   doesn't otherwise need: an ownership check on `POST /withdrawals` (trivial — reuse
   `CreateTransferUseCase`'s pattern) and, more importantly, the AC-5.11 no-existence-leak rule
   applied to a wallet the caller might not own, which the admin-only path deliberately sidesteps.
   Mixing "admin, no leak concern" and "owner, leak concern" into one use case doubles the
   validation branching for a capability nothing in the current spec or milestone plan asks for.
2. **Admin-only deposits, owner-authorized withdrawals (asymmetric gate)** — rejected: this is
   reading #1 for FR-6 and reading #2 for FR-7 simultaneously, which is the literal opposite of
   what FR-7's "symmetric to deposits" is most naturally read as saying. Nothing in the spec
   motivates treating the two operations' *authorization* asymmetrically even though their entry
   pairs are already asymmetric by construction.

## Consequences

- v1 has no self-service cash-out. A wallet owner who wants to withdraw funds must go through an
  admin-operated `POST /withdrawals` call; there is no user-facing withdrawal request flow.
- Adding self-service withdrawal later is a change to `ExternalMovementUseCase.validateWallet`
  (swap in the ownership-checked lookup for that direction, guarded by which principal is calling)
  plus applying the AC-5.11 no-existence-leak rule to that path — not a new endpoint, since the
  entry-pair shape and idempotency/response contract this task builds don't change.
- `SecurityConfig`'s `hasRole("ADMIN")` matcher on `POST /deposits`, `/withdrawals` is the single
  authorization surface for both operations; a future owner-authorized withdrawal would need to
  either split the two endpoints' matchers or add a use-case-level ownership branch, not just widen
  this matcher.
