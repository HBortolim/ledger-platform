# ADR-002: Row-level locking for concurrent transfer serialization

**Status:** Accepted  
**Date:** 2026-05-20

## Context

Two concurrent transfers from the same source wallet must not both succeed if the combined amount exceeds the balance (AC-5.17). We need a serialization mechanism.

## Decision

Maintain an `account_balances_locked` table in `ledger_db`. Each transfer does `SELECT balance FROM account_balances_locked WHERE account_id = $1 FOR UPDATE` before inserting entries. This row is updated within the same DB transaction that posts the entries.

## Alternatives considered

1. **SERIALIZABLE isolation** — would work but causes spurious serialization failures on unrelated wallets, degrading throughput for AC-5.18.
2. **Advisory locks keyed on wallet UUID** — avoids the extra table but advisory locks do not participate in transaction rollback cleanly.
3. **Optimistic locking with version column** — requires retry loops in application code; adds latency under contention.

## Consequences

- Concurrent transfers on **different** wallets do not block each other (satisfies AC-5.18).
- The `account_balances_locked` row is a cached, non-authoritative balance. The authoritative balance is always derived from `ledger_entries`. The reconciliation job (§7.5) detects any divergence.
- Under high contention on a single wallet, throughput is bounded by sequential DB transactions for that wallet only.

### Amendment (2026-11-14, milestone-7 task 05): lock scope, the reconciliation reference, and what it can detect

- **Every account in a posting is locked, not only the source.** `PostingRepository.Post` upserts a lock row
  for each distinct account and takes `SELECT … FOR UPDATE` on all of them (debit *and* credit) in sorted
  UUID order (`lockAccounts`, `sortedAccountIDs`), so transactions touching overlapping account sets cannot
  deadlock. The same locked read now also returns `entry_seq`, the per-account entry counter of ADR-0017:
  the lock that serializes balance checks also serializes entry numbering.
- **The reconciliation job is SPEC §7.6, not §7.5** (§7.5 is the outbox worker), and it exists since M6
  Task 06. It does *not* "detect any divergence" of the cached balance: check 3 proves the sum of all cached
  balances and the sum of all ledger entries are each zero, and check 2 compares each *projection* to the
  ledger. It never compares one account's `account_balances_locked.balance` to that account's own entry sum,
  so two compensating cache errors (one account +5, another −5) would pass. M6 Task 06's Notes record this as
  a known blind spot and the natural fourth check if the cached-balance path ever changes.
- **Advisory locks** were rejected above for transfers. The reconciliation job uses a session-level advisory
  lock for a different job (cross-process overlap guard for a long read-only pass, where rollback semantics
  do not matter); that is not a reversal of this decision.
