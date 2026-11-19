# ADR-001: HTTP/JSON over gRPC for Wallet→Ledger transport

**Status:** Accepted  
**Date:** 2026-05-20

## Context

The Wallet Service calls the Ledger Service synchronously for every transfer. We need to decide on the transport protocol.

## Decision

Use HTTP/JSON (`RestClient` on Java, `net/http` on Go).

## Rationale

- Proto compilation and dual-language tooling adds complexity with no throughput benefit at this scale.
- HTTP/JSON is trivially debuggable with `curl`.
- The p95 latency target (< 200 ms end-to-end) is achievable over local HTTP.

## Rejected alternative

gRPC — considered and rejected because the added tooling cost is not justified until the latency budget tightens beyond what HTTP can deliver.

## Consequences

- If throughput requirements exceed ~500 TPS, revisit gRPC or async messaging.
- Service contracts are OpenAPI specs in `docs/api/`.

### Amendment (2026-11-14, milestone-7 task 05): what the transport looks like now, and the contracts claim

- The decision stands. Wallet → Ledger is HTTP/JSON: `LedgerClient` (Spring `RestClient`, 2 s connect / 5 s
  read timeouts in `LedgerClientConfig`) against the Ledger Service's Gin handlers. Load numbers for the
  "revisit above ~500 TPS" trigger are measured in Milestone 7 (`docs/results/`).
- "Service contracts are OpenAPI specs in `docs/api/`" was not true when written and is only partly true
  now: `docs/api/` held no specs until Milestone 7 Task 06, which adds OpenAPI for the **Wallet Service's public
  API only**. The internal contracts (`POST /ledger/postings`, `GET /ledger/transactions/{id}`, the admin
  routes) are specified in SPEC.md §7 and described in `docs/architecture/data-flow.md`.
- The Wallet Service also calls the Projection Service over HTTP for exactly one thing, the admin rebuild
  (ADR-0016); every other cross-service read is SQL (ADR-0007).
