# ADR-0006: Deterministic transaction IDs from idempotency keys

**Status:** Accepted
**Date:** 2026-07-24

## Context

`POST /transfers` must survive a Wallet Service crash between posting to the Ledger Service and returning a response to the client (§9.2). Recovery requires re-deriving the same `transactionId` that was used for the original (possibly-successful) posting, so the retry can ask the Ledger Service "did this transaction already commit?" via `GET /ledger/transactions/{id}` instead of blindly posting again.

This only works if the `transactionId` is a pure function of information the client already resent: the JWT subject and the `Idempotency-Key` header. A randomly generated ID would be lost forever if the crash happens before it's persisted anywhere durable.

## Decision

`transactionId = UUIDv5(NAMESPACE, "{userId}:{idempotencyKey}")`, where:

- The hash is SHA-1 per RFC 4122 §4.3 (UUID version 5, name-based).
- `NAMESPACE` is a fixed constant, `331b3e69-c5f0-425b-b7c0-0d5ef39ae323`, generated once and hardcoded in `IdempotencyKeys.NAMESPACE` (`services/wallet-service/.../application/idempotency/IdempotencyKeys.java`). It must never change — doing so would make every previously-derived `transactionId` unrecoverable.
- The name string is `"{userId}:{idempotencyKey}"` (both as their canonical string forms), so the same user reusing the same key always derives the same UUID, and different users using the same key literal derive different UUIDs (NFR-SEC-7's per-user scoping extends to this derivation).
- A request with no `Idempotency-Key` header gets `UUID.randomUUID()` instead, plus a logged warning (AC-5.15) — there's no key to derive from, and the client explicitly opted out of retry safety.
- The `transferId` returned to the client **is** this `transactionId` — there is no second ID. This keeps the §9.2 recovery `GET` trivial: re-derive, then look the ID up directly.

Java's `UUID.nameUUIDFromBytes` only implements UUIDv3 (MD5), not UUIDv5 (SHA-1), so the algorithm is hand-rolled in `IdempotencyKeys.uuidV5`, verified against Python's `uuid.uuid5` reference vectors in `IdempotencyKeysTest`.

## Rejected alternative

Random `transactionId` generated at request time and persisted immediately alongside the idempotency record before calling the Ledger Service. Rejected because it adds a second write (and a second failure window — a crash between generating the ID and persisting it reintroduces the exact problem this ADR solves) for no benefit over a pure function of data the client already has.

## Consequences

- `idempotency_records` does not need a `transaction_id` column — it's always recomputed, never stored.
- The namespace constant is effectively part of the durable data model: changing it is a breaking change equivalent to a schema migration with no rollback path for in-flight transactions.
- Keyless requests forfeit crash-recovery idempotency by design; this is the client's tradeoff, not the service's.

### Amendment (2026-11-14, milestone-7 task 05): the derivation now covers deposits and withdrawals

Verified against the code: `IdempotencyKeys.NAMESPACE`, the `"{userId}:{idempotencyKey}"` name and the
keyless `randomTransactionId()` fallback are unchanged, and `idempotency_records` still has no
`transaction_id` column. M6 added two more callers of the same derivation, `ExternalMovementUseCase`
(`POST /deposits`, `POST /withdrawals`), so a user has **one idempotency key space across transfers, deposits
and withdrawals**. Reusing a key across endpoints cannot silently post the wrong thing: the three request
fingerprints (`RequestFingerprint.of`) have different canonical shapes (the deposit and withdrawal ones carry a
`type`), so the second use is `422 IDEMPOTENCY_KEY_MISMATCH` before it reaches the ledger. Note that the
fingerprints cover the wallet ids and amount only, not `description`.
