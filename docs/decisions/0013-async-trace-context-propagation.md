# ADR-0013: Async trace context propagation across outbox and Kafka

**Status:** Accepted
**Date:** 2026-08-22

## Context

NFR-OBS-5 requires one end-to-end trace "from `POST /transfers` through to the projection write." Everything up through Task 04 produces two disconnected halves: a wallet→ledger trace that stops at the HTTP response, and a projection service that writes balances with no idea which request caused them. The only path between those two points is asynchronous — through the ledger-service's outbox table and Kafka — and the outbox worker publishes on its own goroutine, on a 100 ms tick, in a batch, potentially seconds after the posting committed (and after a Kafka outage, potentially much later). There is no ambient request context to read at publish time; the trace context has to be captured while the posting request is still in flight and carried with the row, which is exactly what the `headers` JSONB column (added in M2) was for. The schema left a placeholder for this: `Traceparent` was always written as `""`.

## Decision

Propagate trace context parent–child via the W3C `traceparent` format, not span links:

- At posting time, `insertOutboxRow` injects the active span's context into a `propagation.MapCarrier{}` via `otel.GetTextMapPropagator().Inject`, and persists it into the outbox row's `headers` JSONB column (authoritative) and mirrors it into the `LEDGER_POSTED` payload's `traceparent` field (for human debuggability and §7.3 schema conformance).
- The outbox worker's `Poll` decodes the row's headers into Kafka record headers, and additionally extracts that same context to start an `outbox publish` producer span — making the row's captured context, not a fresh context, the parent of the publish span. This span is what makes outbox lag visible in the Jaeger waterfall.
- The projection consumer extracts the `traceparent` from the consumed Kafka record's headers (never the payload) and starts a `projection apply` consumer span as a child of it.
- The header is authoritative for propagation; the payload's `traceparent` field is a mirror only. A consumer that needs the payload's copy instead of the header is a bug in that consumer, not a reason to change the producer.

## Alternatives considered

1. **Span links** — the usual OpenTelemetry messaging-semantic-conventions choice for message-broker instrumentation, and a better fit for high-fanout batch consumers. Rejected because links produce *separate* traces joined only by reference, which would fail NFR-OBS-5's requirement of *one* trace "through to the projection write" outright. One `LEDGER_POSTED` event maps to one logical transfer here, so the batch-fanout advantage of links doesn't apply.
2. **Payload field as the carrier** — using the `traceparent` field already in the `LEDGER_POSTED` schema as the sole propagation mechanism. Works, but puts a cross-cutting propagation concern inside the business event schema, and breaks any consumer that only reads Kafka headers (the idiomatic place to look for trace context). Kept as a mirror for debuggability, but not authoritative.
3. **Re-reading ambient context at publish time** — impossible in this architecture: the outbox worker publishes on its own goroutine, on a ticker, long after the original HTTP request's context has ended. There is nothing ambient left to read.

## Consequences

- Traces for a busy wallet can be long-lived: the trace doesn't close until the projection write commits, which may be a full poll tick (up to 100 ms) plus Kafka round-trip after the HTTP response was already returned to the caller.
- A consumer that fans one event into many downstream calls will nest all of them under the original transfer's trace. That's desirable today (one event, one logical transfer) and would need revisiting if a consumer's fan-out grows large enough that link semantics become the better fit.
- Empty context — tracing disabled, i.e. no `OTEL_EXPORTER_OTLP_ENDPOINT` configured — degrades gracefully: `Inject` writes nothing, the carrier stays empty, and the row gets `{}` headers and `""` traceparent, which is behaviorally equivalent to pre-M5 behavior (both mean "no trace context"), though not byte-identical: pre-M5 the code wrote `json.Marshal(map[string]string{"traceparent": ""})` → `{"traceparent":""}`, one empty-valued header; post-M5 an empty `propagation.MapCarrier{}` marshals to `{}`, zero headers. No consumer in this repo distinguishes the two. Every existing outbox and E2E test keeps passing unchanged, confirmed by running `make test-e2e` against a core-only stack (no observability containers).
- The two carrier types (`headerCarrier` in ledger-service, `recordCarrier` in projection-service) are near-identical (~20 lines) but live in different Go modules; this duplication is accepted rather than building a shared module for it, consistent with the existing duplicated `logging` and `metrics` packages between the two services.
