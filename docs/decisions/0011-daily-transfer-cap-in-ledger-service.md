# ADR-0011: Daily transfer cap enforced in Ledger Service, inside the locked transaction

**Status:** Accepted
**Date:** 2026-08-01

## Context

`docs/SPEC.md` §8 — the transfer flow, explicitly called "the single most important flow" —
specifies the daily per-wallet transfer cap check as step `[8]`, inside the Ledger Service's
locked transaction, immediately after the `SELECT ... FOR UPDATE` account lock (step `[7]`)
and the available-balance check, before any writes:

```
[6]  Ledger Service: BEGIN
[7]  Ledger Service: SELECT ... FOR UPDATE on source wallet balance
[8]  Ledger Service: check available balance + daily cap
[9]  Ledger Service: INSERT ledger_transactions, INSERT ledger_entries
```

The actual implementation instead followed §5.2's ownership table and the M2/M3 milestone
decision docs, which placed the cap check in the Wallet Service: `CreateTransferUseCase.validate()`
read `ledgerEntryRepository.sumTodaysDebits(sourceWalletId)` — an unlocked cross-schema `SELECT`
against `ledger_db.ledger_entries` — *before* calling the Ledger Service at all. §5.2's stated
rationale was that daily caps are a "business rule that doesn't require ledger state," which
turned out to be false once built: the check has to read `ledger_entries` to sum today's debits.

This is a TOCTOU race, independent of where the code lives: two concurrent transfers from the
same wallet can each read a pre-transfer sum, both individually pass, and jointly exceed the
cap, since nothing serializes the read against the eventual ledger write. A `//TODO: assess
move this validation to ledger-service` comment in `CreateTransferUseCase` already flagged this
gap. The available-balance check faces the identical race and is solved by running inside the
Ledger Service's locked transaction (ADR-002); the daily cap check had no equivalent protection.

## Decision

Enforce the daily cap exclusively in the Ledger Service, inside `PostingRepository.Post`'s
existing locked transaction — immediately after `checkAvailableBalance` and before any `INSERT`,
under the same `FOR UPDATE` lock on `account_balances_locked` that already protects the balance
check. The Wallet Service's pre-check is removed entirely; there is no duplicated,
non-authoritative check left there. Single source of truth for the limit.

## Alternatives considered

1. **Keep the check in the Wallet Service** — rejected: the TOCTOU race above. An unlocked
   read outside the transaction that eventually writes the entries cannot be made race-free
   without moving into that transaction.
2. **A dedicated Limits/Risk service** — rejected: `docs/SPEC.md` §2.2 explicitly excludes
   "transaction limits beyond a hardcoded daily cap" from v1 scope. Building a new service for
   this would be scope creep against the project's own stated boundaries.
3. **Keep a non-authoritative, fast-fail pre-check in the Wallet Service alongside the
   authoritative Ledger Service check** — rejected for this portfolio project: it would
   duplicate the cap's config value across two services (`application.yml` and a new
   ledger-service env var) with no correctness benefit, since the Ledger Service check is now
   the required backstop regardless. The UX savings of an early rejection don't outweigh the
   two-sources-of-truth risk at this project's scale.

## Consequences

- The cap's config value moves from Wallet Service's `application.yml` (`ledger.daily-transfer-cap`)
  to Ledger Service's `DAILY_TRANSFER_CAP` environment variable (default `"100000.00"` in code,
  matching the old default).
- `AC-5.10`'s `422 DAILY_LIMIT_EXCEEDED` client contract is unchanged — `LedgerClient.postPosting`
  → `PostPostingResult.Rejected(code, message)` → `CreateTransferUseCase.rejectedOutcome` already
  passes through whatever `{code, message}` the Ledger Service returns, so no wallet-service
  response-handling code needed to change.
- `LedgerEntryRepository`, its JDBC implementation, and the `ledger_db.ledger_entries` read path
  they existed solely to serve are deleted from the Wallet Service. The `wallet_app` → `ledger_db`
  cross-schema read grant (`services/ledger-service/migrations/0004_wallet_read_grants.sql`) is
  also revoked: with the daily-cap query gone, nothing exercises it — `GET /wallets/{id}/balance`
  (Task 06) is still not built, so keeping an unused grant on `wallet_app` violates least
  privilege for no present benefit. Task 06 will introduce its own scoped grant when the
  balance-endpoint freshness join actually needs it, per `docs/milestones/milestone-3/06-balance-endpoint.md`'s
  original plan (that doc always attributed this migration to Task 06 in the first place).
- `docs/milestones/milestone-2/00-overview.md`, `milestone-2/03-posting-repository.md`,
  `milestone-3/00-overview.md`, and `milestone-3/05-transfer-endpoint.md` describe the
  now-superseded Wallet Service placement as a build-time decision. Each is annotated with a
  pointer to this ADR rather than rewritten, since they are point-in-time records of what was
  actually built at each milestone.
- `TestPostingConcurrency/ConcurrentTransfersNearDailyCap` (ledger-service) is the direct proof
  the race is closed: two concurrent same-source transfers that each individually fit under a
  tight cap but jointly exceed it — exactly one succeeds.

### Amendment (2026-09-19, milestone-6 task 03): system account exempt from the cap

`checkDailyTransferCap` now skips any entry whose account is `r.systemAccountID`, the same
exemption `checkAvailableBalance` already had (ADR-0008: the system funding account may overdraw,
because its debits are the sum of all money entering the platform, not a wallet's outflow). Without
this, `POST /deposits` (FR-6) would drive the system account's daily debit total past `dailyCap`
under normal platform volume and start rejecting every subsequent deposit that day with
`DAILY_LIMIT_EXCEEDED` — a platform-wide outage triggered by legitimate inflow. The cap therefore
bounds per-wallet outflow only; it never bounds platform inflow, and a wallet's own debit total
(withdrawals, outgoing transfers) is still capped exactly as before.

### Amendment (2026-11-14, milestone-7 task 05): the read grant came back

The Consequences above say the `wallet_app` → `ledger_db` read grant was revoked and that "Task 06 will introduce
its own scoped grant". That happened: Ledger migration `0004_wallet_read_grants` grants `wallet_app` `SELECT`
on `ledger_db.ledger_entries`, used by `GET /balance`'s freshness join and, since M6, by the close-wallet check
(`BalanceRepository.getLedgerBalance`, a SUM over the wallet's entries). The cap itself is still enforced only
in the Ledger Service (`DAILY_TRANSFER_CAP`, default `100000.00`), and the Wallet Service has no cap logic.
