# ADR-0017: Per-wallet entry sequence for exactly-once projection

**Status:** Accepted
**Date:** 2026-11-10

## Context

The projection's only idempotency guard was one column, `wallet_balances.last_entry_id`. It answers
"is this the entry I applied most recently?", not "have I ever applied this entry?". The difference is
reachable: ledger entries E1 (+100), E2 (+50), E3 (+25); the consumer has applied only E1; an
operator rebuilds from the ledger (balance 175, `last_entry_id` = E3); the consumer resumes with E2
(`last_entry_id` ≠ E2, so it applies, 225) and then E3 (`last_entry_id` is now E2, so it applies
again, **250**). A rebuild is most likely exactly when the consumer is behind, so the defect hides in
the one moment an operator is trying to fix things.

Two facts constrain the fix:

- **Events for one wallet are already reordered.** `ledger.posted.v1` has 12 partitions and the outbox
  keys records by transaction id (a transfer touches two wallets; a record has one key). A wallet's
  events land on different partitions and one consumer sees them in arbitrary order. Balance deltas
  commute, so nobody noticed. A sequence number does not commute.
- **A failed apply can be committed past.** A failed apply leaves its offset uncommitted but the loop
  moves on, and the next successful record on the partition commits a higher offset, so the failed
  event is never redelivered. Reproduced in `consumer_offset_test.go`, then fixed (see Consequences).

## Decision

**The ledger numbers each account's entries `1, 2, 3, …` (`ledger_entries.seq`), the event carries the
number (`LEDGER_POSTED` `schemaVersion: 2`), and the projection keeps a watermark `wallet_balances.last_seq`.**

- The counter lives on `account_balances_locked.entry_seq` and is read and advanced inside `Post`'s
  existing `SELECT … FOR UPDATE` transaction. Two postings on the same account never overlap, so the
  number is assigned in per-account commit order with no gaps. `UNIQUE (account_id, seq)` turns any
  double assignment into a failed posting instead of corrupt history.
- The projection applies an entry iff `seq = last_seq + 1`, skips `seq <= last_seq`, and **parks**
  anything ahead of a gap in `pending_entries`, draining contiguous parked entries in the same
  transaction whenever `last_seq` advances. Parking is mandatory, not an edge case: applying an entry
  ahead of a gap would advance the watermark past the missing one, which would then be skipped.
- A rebuild replays in `seq` order, writes `last_seq = MAX(seq)`, discards parked entries at or below
  it and drains the contiguous ones, using the same drain as the consumer. It first ensures the
  wallet's row exists and locks it, so a consumer cannot slip between the ledger read and the write.
- A gap that stays open is made visible (`projection_pending_entries`, `projection_seq_gaps_total`, an
  error log naming the wallet and missing `seq` after `PENDING_ENTRY_ALERT_AFTER`), never waited on
  silently and never guessed across. The recovery is a rebuild.

### Rejected alternatives

- **`created_at` or `occurredAt` as the order.** `created_at` is `now()` at transaction *start*; two
  postings on one wallet can commit in the opposite order to the one they started in, and the Go clock
  in `occurredAt` has the same problem plus skew. A "newer-than" guard on either can *skip* a real
  entry, silently losing money, which is strictly worse than a double count.
- **A global `BIGSERIAL`.** `nextval()` order is not commit order.
- **A projection-local inbox of applied entry ids.** Cheaper to build, but it grows with the ledger and,
  more importantly, **cannot detect a lost event**: it only remembers what arrived. A per-wallet number
  makes a missing entry visible as a gap.
- **Keying the topic by wallet or using one partition.** A transfer has two wallets and one key, and a
  single partition would only make reordering rarer, not impossible (producer retries), so parking would
  still be required, at the cost of the topic's scale-out headroom.

## Consequences

- **Rollout.** Deploy the ledger first (publishes v2), then the projection. The consumer still accepts
  v1 events, resolving their `seq` with `SELECT seq FROM ledger_entries WHERE id = $1`; delete that
  lookup once no v1 event can remain within the topic's retention. Projection migration `0006`
  backfills `last_seq` from `last_entry_id`, which is exact only if the consumer was caught up: drain
  it before migrating, or rebuild every wallet afterwards.
- **Backfill order.** Historic entries are numbered by `(created_at, id)`, which is not guaranteed to be
  commit order for old concurrent postings. The only requirement is a gap-free, unique numbering the
  projection is aligned to.
- **The hot path.** `Post` reads one more column under a lock it already took and writes one more
  column in two statements it already issued.
- **Reconciliation.** `wallet_balances.last_seq` against `account_balances_locked.entry_seq` is now a
  cheap "is this projection behind?" signal that needs no `SUM`. Not built here.
- **Fixed in a follow-up.** The consumer loop no longer commits past a failed apply (the Context's second
  fact): it stops that partition at the failed record, rewinds to it (`SetOffsets`, with
  `BlockRebalanceOnPoll` so no revoke can interleave) and retries it with a 1 s backoff, while other
  partitions keep flowing. The trade-off: an apply that fails *permanently* (not a poison message,
  which is still committed past) now holds its partition until someone intervenes, visible as consumer
  lag and a repeating warning. That is preferred to silently losing the event.
