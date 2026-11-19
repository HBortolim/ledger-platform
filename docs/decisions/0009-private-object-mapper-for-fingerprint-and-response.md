# ADR-0009: Private, unconfigured `ObjectMapper` for fingerprinting and stored responses

**Status:** Accepted
**Date:** 2026-08-01

## Context

Two places in `wallet-service` build JSON with a locally-declared `private static final ObjectMapper MAPPER = new ObjectMapper();` instead of injecting Spring's autoconfigured mapper:

- `RequestFingerprint.of(...)` — hashes a canonical re-serialization of `CreateTransferRequest` to produce the SHA-256 used for idempotency replay/mismatch detection (AC-5.12, AC-5.13).
- `CreateTransferUseCase.successOutcome(...)` / `rejectedOutcome(...)` — builds the response body that gets both returned to the client and persisted verbatim by `IdempotencyService.complete(...)` for later replay.

Both avoid serializing domain types (`Instant`, the `CreateTransferRequest`/`TransferResponse` records) directly, instead building a `LinkedHashMap<String, Object>` of pre-stringified values (`BigDecimal.toPlainString()`, `Instant.toString()`).

Two independent reasons make Spring's shared mapper the wrong dependency here:

1. **Stability of a wire contract.** `RequestFingerprint`'s hash and `IdempotencyService`'s stored response body are durable — a stored fingerprint must keep matching an identical retry, and a stored response body must keep deserializing/replaying correctly, for as long as the idempotency record's TTL lasts. If either depended on the app's shared, globally-configured mapper, an unrelated change to that global config (naming strategy, date format, a newly added module) would silently change the format and break already-computed fingerprints or previously-stored bodies. A private, deliberately minimal mapper freezes the format.
2. **Type mismatch in this Spring Boot version.** `wallet-service` is on `spring-boot-starter-parent:4.0.6`, whose default JSON stack (`spring-boot-starter-jackson`) is backed by `tools.jackson.databind.ObjectMapper` ("Jackson 3"), not `com.fasterxml.jackson.databind.ObjectMapper` ("Jackson 2"). These two files import the classic `com.fasterxml.jackson.databind.ObjectMapper`, which Spring does not autoconfigure a bean for in this app — `com.fasterxml.jackson.*` only reaches the classpath transitively via `jjwt-jackson` (JWT handling), not as part of the app's primary JSON stack. There is nothing to `@Autowire` here even if reuse were otherwise desirable.

## Decision

Keep a private, unconfigured `com.fasterxml.jackson.databind.ObjectMapper` in each of `RequestFingerprint` and `CreateTransferUseCase`, and never hand it a type (`Instant`, `BigDecimal`, a record) whose default serialization isn't already guaranteed stable. Convert such values to `String` (`.toString()`, `.toPlainString()`) before putting them into a `LinkedHashMap<String, Object>`, so the mapper only ever serializes plain strings and never needs a registered module (e.g. `JavaTimeModule`) to avoid throwing on `Instant`.

## Alternatives considered

1. **Inject Spring's shared `ObjectMapper`** — rejected: wrong type in this Boot version (see above), and even if it were the right type, it would couple a durable fingerprint/replay format to a mutable global config.
2. **Register `JavaTimeModule` on a local mapper and serialize the DTOs/records directly** — rejected: adds a dependency (`jackson-datatype-jsr310`, itself the "wrong" Jackson 2 line for this Boot version) for no benefit over stringifying the one or two fields that need it; also still leaves `BigDecimal`'s default numeric formatting unpinned for the fingerprint use case.
3. **Add a `tools.jackson.databind.ObjectMapper`-based shared bean and migrate these two files to it** — rejected for now: still leaves the durable-format coupling problem in (1), and is a larger change (migrating off `com.fasterxml.jackson` entirely) than this decision's scope.

## Consequences

- Both mappers must stay "dumb": any new field of a type Jackson can't serialize by default (e.g. another `Instant`) needs to be stringified explicitly before reaching the mapper, or the mapper needs a module — there is no test currently enforcing this, so a future change adding a raw `Instant`/`BigDecimal` field would fail at runtime, not compile time.
- `RequestFingerprint`'s hash format is effectively part of the durable data model, same as ADR-0006's `transactionId` derivation: changing field order, key names, or the `BigDecimal` string format is a breaking change for any idempotency record whose TTL hasn't yet expired.
- If the app later needs a shared, module-registered mapper for other purposes, it should be built against `tools.jackson.databind.ObjectMapper` (matching Spring Boot 4.0.6's default stack), not `com.fasterxml.jackson.databind.ObjectMapper` — and should still not be reused for `RequestFingerprint` or stored idempotency response bodies per the stability reasoning above.

### Amendment (2026-11-14, milestone-7 task 05): three call sites, not two

The decision and its reasoning are unchanged and still hold (Spring Boot 4.0.6, `new ObjectMapper()` from
`com.fasterxml.jackson.databind`). The set of places it applies grew: `RequestFingerprint` now has three
`of(…)` overloads (transfer, deposit, withdrawal), and `ExternalMovementUseCase` builds its stored response
bodies with its own private mapper exactly as `CreateTransferUseCase` does. Movement responses use
`transactionId` where transfers use `transferId`; both are stored and replayed verbatim. The same stability rule applies
to all three: the canonical field order and string formats are part of the durable data model.
