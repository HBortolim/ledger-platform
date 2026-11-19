# Architecture Decision Records

One file per decision, numbered in the order they were made. Each records the context, the decision, what was
rejected, and the consequences. A decision that no longer holds is **not rewritten**: it gets a dated
`### Amendment` section at the bottom (see ADR-0011, or the 2026-11-14 amendments added in Milestone 7), or its
status becomes *Superseded by* a newer ADR.

Four-digit numbers are used here; the headings of the first four ADRs predate that and say `ADR-001`…`ADR-004`.

| # | Title | Status | Milestone | Decision in one line |
|---|---|---|---|---|
| [0001](0001-http-not-grpc.md) | HTTP/JSON over gRPC for Wallet→Ledger | Accepted | Design | Plain HTTP/JSON between services; revisit gRPC only if the latency budget outgrows it. |
| [0002](0002-locking-strategy.md) | Row-level locking for concurrent transfers | Accepted | Design | Serialize per account with `SELECT … FOR UPDATE` on an `account_balances_locked` row in the posting transaction. |
| [0003](0003-append-only-role-grants.md) | Append-only DB role and grants for ledger tables | Accepted | M2 | The ledger service runs as `ledger_app`, which can `INSERT`/`SELECT` entries but never `UPDATE`/`DELETE` them. |
| [0004](0004-per-service-migration-ownership.md) | Per-service migration ownership | Accepted | M2 | Each service owns and applies its own migrations (Flyway for Java, golang-migrate for Go), one history table each. |
| [0005](0005-dockertest-for-integration-tests.md) | Keep dockertest for Go integration tests | Accepted | M2 | Go services use `dockertest`, matching SPEC §10.2, rather than `testcontainers-go`. |
| [0006](0006-deterministic-transaction-ids.md) | Deterministic transaction IDs from idempotency keys | Accepted | M3 | `transactionId` = UUIDv5 of `userId:idempotencyKey`, so a crashed transfer can be recovered by recomputing it. |
| [0007](0007-wallet-service-reads-projections-via-sql.md) | The Wallet Service reads projections via SQL, not HTTP | Accepted | M3 | `GET /balance` joins the projection and the ledger in one read-only SQL statement; no HTTP hop. |
| [0008](0008-system-account-may-overdraw.md) | System funding account may overdraw | Accepted | M3 | Exactly one configured account is exempt from the available-balance check, so money can enter the platform. |
| [0009](0009-private-object-mapper-for-fingerprint-and-response.md) | Private `ObjectMapper` for fingerprints and stored responses | Accepted | M3 | The durable idempotency hash and stored bodies use a private, unconfigured mapper that can't drift with global config. |
| [0010](0010-no-transactional-wrapper-on-create-transfer.md) | No `@Transactional` around `CreateTransferUseCase` | Accepted | M3 | A local transaction cannot span the remote ledger call and would hide the `PENDING` idempotency row. |
| [0011](0011-daily-transfer-cap-in-ledger-service.md) | Daily transfer cap enforced in the Ledger Service | Accepted | M3 | The cap is checked inside the locked posting transaction, closing a check-then-act race. |
| [0012](0012-otel-collector-trace-pipeline.md) | OpenTelemetry Collector trace pipeline | Superseded by [0014](0014-jaeger-v2-native-otlp.md) | M5 | Route traces through an OTel Collector in front of Jaeger. |
| [0013](0013-async-trace-context-propagation.md) | Async trace context propagation across outbox and Kafka | Accepted | M5 | Carry W3C `traceparent` in the outbox `headers` column and Kafka headers; parent–child spans, not links. |
| [0014](0014-jaeger-v2-native-otlp.md) | Services export directly to Jaeger v2 | Accepted | M5 | Drop the standalone Collector; every service speaks OTLP straight to Jaeger v2. |
| [0015](0015-deposits-and-withdrawals-are-admin-only.md) | Deposits and withdrawals are admin-only | Accepted | M6 | Both external movements require an admin JWT; there is no owner-authorized withdrawal in v1. |
| [0016](0016-projection-rebuild-through-the-wallet-service.md) | Projection rebuild goes through the Wallet Service | Accepted | M6 | The admin gate and audit row live in the Wallet Service, which proxies to an auth-free Projection Service route. |
| [0017](0017-per-wallet-entry-sequence.md) | Per-wallet entry sequence for exactly-once projection | Accepted | M6 | The ledger numbers each account's entries; the projection applies `last_seq + 1`, skips old, parks ahead. |

ADR-0007 was planned in Milestone 3 and not written until Milestone 7; the numbering is otherwise continuous.

Milestone planning documents (`docs/milestones/`) are kept out of the repository; the decisions above are the
durable record.
