# Milestone 5 — Observability

**Status:** Ready to start
**Owner:** All three services + infrastructure
**Spec reference:** [`SPEC.md` §11 — Milestone 5](../../SPEC.md), NFR-OBS-1..5, §7.3 (event schemas / `traceparent`), §7.4 (required metrics), §12 (repo layout)
**Estimated effort:** 3 days (per roadmap)

---

## Goal

Make one transfer tell its whole story. After this milestone a reviewer can run a transfer, open Jaeger, and follow a single unbroken trace from `POST /transfers` at the Wallet Service edge, through the Ledger Service's posting transaction, across the outbox → Kafka boundary, and into the Projection Service's balance write — with every log line along the way carrying that trace's ID, and a Grafana dashboard showing the system's health while it happens.

**Demonstrable (the M5 acceptance bar):**

1. `make up-obs` brings up the core stack plus Jaeger, Prometheus, Grafana, and the OTel Collector.
2. Run a transfer. Open Jaeger at `localhost:16686`, search `wallet-service`, and see one trace spanning all three services, ending at the projection write (NFR-OBS-5).
3. `docker compose logs ledger-service` shows structured JSON with a populated `trace_id` matching that trace (NFR-OBS-1, -2).
4. Grafana's `Transfers Overview` dashboard shows transfer RPS, latency percentiles, error rate, projection lag, consumer lag, and outbox depth (NFR-OBS-4).

---

## What already exists vs. what M5 builds

**The metrics half of this milestone is already done.** All ten §7.4 metrics were built incrementally during M2 Task 06 and M3 Task 07, and `prometheus.yml` already scrapes all three services. M5's real work is tracing, structured logging in the Go services, and making the observability stack actually run.

| Area | State | Where |
|---|---|---|
| All 10 required §7.4 metrics (wallet ×3, ledger ×4, projection ×3) | ✅ Done (M2/M3) | `MetricsFilter.java`, `IdempotencyService.java`, `internal/metrics/metrics.go` ×2 |
| Prometheus scrape config for all three services | ✅ Done | `infrastructure/observability/prometheus.yml` |
| Wallet-service JSON log pattern with `trace_id`/`span_id` MDC slots | ✅ Done (renders empty today — nothing populates MDC) | `application.yml:44` |
| Wallet-service OTel starter dependency | ✅ Done (present, unconfigured) | `pom.xml` (`spring-boot-starter-opentelemetry`) |
| Outbox → Kafka header machinery (JSONB `headers` → `kgo.RecordHeader`) | ✅ Done (M2 Task 05) | `internal/outbox/headers.go` |
| `traceparent` field in the `LEDGER_POSTED` payload + consumer struct | ✅ Done (schema only, always `""`) | `posting.go:362`, `consumer/event.go:23` |
| `docker-compose.observability.yml` (jaeger, prometheus, grafana, collector) | ⚠️ Exists, never run — see decision #1 | repo root |
| `otel-collector.yml` | ⚠️ Exists, uses a **removed** exporter — see decision #1 | `infrastructure/observability/` |
| **Observability stack actually starting + services wired to it** | ❌ To build | Task 01 |
| **Structured JSON logging in both Go services** | ❌ To build (stdlib `log`, plain text) | Task 02 |
| **OTel SDK + HTTP instrumentation in both Go services** | ❌ To build (zero OTel deps in either `go.mod`) | Task 03 |
| **Wallet-service tracing config + outbound propagation** | ❌ To build | Task 04 |
| **Trace context across outbox → Kafka → projection** | ❌ To build (hardcoded `""` placeholder) | Task 05 |
| **Grafana `Transfers Overview` dashboard** | ❌ To build (`grafana/` dir exists but is empty) | Task 06 |
| **End-to-end trace verification + sign-off** | ❌ To build | Task 07 |

---

## Global constraints

Every task's requirements implicitly include these.

- **Log field names are fixed by NFR-OBS-1:** `trace_id`, `span_id`, `service`, `level`, `msg`. The Go services must match the Wallet Service's existing shape (`application.yml:44`), which also emits `timestamp`. Structured JSON to stdout, nothing else.
- **Trace propagation is W3C `traceparent`** (NFR-OBS-2) — over HTTP headers and as a Kafka record header. No B3, no vendor formats.
- **Observability is optional at runtime.** When `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, every service must start and behave normally with tracing disabled. `make up`, `make test`, and `make test-e2e` must all keep working with no observability stack running — §1.1's "all services healthy within 60 seconds" bar applies to the core stack alone.
- **No changes to the ten existing §7.4 metrics.** Their names and labels are already correct and asserted by existing tests.
- **Sampling is always-on** (ratio 1.0) in this environment. A portfolio demo that drops the reviewer's one transfer is worthless.
- **Service names in traces:** `wallet-service`, `ledger-service`, `projection-service` — exactly matching the `service` log field and the `prometheus.yml` job names.

---

## Decisions for this milestone

| # | Decision | Rationale | Reference |
|---|---|---|---|
| 1 | **Services export to the OTel Collector, which forwards to Jaeger** — not directly to Jaeger. This requires fixing `otel-collector.yml`, whose `jaeger` exporter was deprecated and then **removed** from the Collector distribution well before the pinned `0.101.0` image; it must become an `otlp` exporter targeting `jaeger:4317`. Jaeger 1.57 accepts OTLP natively. | The collector config is already a first-class artifact in `SPEC.md` §12's layout, and it's the production-shaped topology worth demonstrating. The current config almost certainly fails to start — strong evidence this stack has never been run. Alternative (direct-to-Jaeger) is simpler but discards a file the spec asks for. | ADR-0012; Task 01 |
| 2 | **Async trace context is parent–child via `traceparent`, not span links.** The Kafka record header is the authoritative carrier for context extraction; the payload's `traceparent` field is populated for human debuggability and §7.3 schema conformance, and consumers MUST prefer the header. | NFR-OBS-5 demands *one* end-to-end trace "from `POST /transfers` through to the projection write". Span links — the usual messaging-conventions choice — would produce two separate traces and fail that requirement outright. The tradeoff (links scale better for high-fanout batch consumers) doesn't bite here: one event maps to one logical transfer. | ADR-0013; Task 05 |
| 3 | **Tracing is disabled when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset**, and the W3C propagator is installed unconditionally. `make up` stays core-only; a new `make up-obs` adds the observability stack. | Keeps the §1.1 60-second bar and the entire test suite independent of a 4-container observability stack. Also means a reviewer who only wants the ledger demo never has to run Jaeger. | Tasks 01, 03 |
| 4 | **Kafka trace context is extracted manually via a small `TextMapCarrier` over `kgo.Record.Headers`**, rather than adopting a Kafka-instrumentation library. | ~25 lines, zero new dependencies, and completely explicit about what crosses the boundary — which is exactly the part of this milestone a reviewer will want to read. franz-go's `kotel` plugin is a reasonable alternative; evaluate it only if manual carriers start multiplying. | Task 05 |
| 5 | **DB-level spans (`otelpgx`) are out of scope.** Traces cover HTTP ingress, the posting service call, the outbox publish, and the projection apply — not individual SQL statements. | Keeps the milestone at its 3-day budget. The end-to-end story NFR-OBS-5 asks for is complete without per-query spans; they can be added later without reworking anything built here. | Task 03 notes |
| 6 | **"Error rate by code" (NFR-OBS-4) is read as HTTP status code**, using `wallet_requests_total{status=~"4..\|5.."}`. | That's the label that actually exists on the metric §7.4 mandates. Domain codes (`INSUFFICIENT_FUNDS`, …) are not currently a metric dimension, and adding one would change a metric this milestone's Global Constraints freeze. Flagged as an open question in Task 06. | Task 06 |

### New ADRs to write during M5

- **ADR-0012 — OTel Collector as the trace pipeline.** The collector-vs-direct-to-Jaeger choice, the removed `jaeger` exporter discovery, and the port topology (Go → gRPC 4317, Spring → http/protobuf 4318).
- **ADR-0013 — Async trace context is parent–child, carried in the Kafka header.** Why not span links, why the header outranks the payload field, and what that costs.

Place both in `docs/decisions/` following the existing format. Next free number is **0012** (0001–0011 are taken).

---

## Task order & dependencies

```
01 observability-stack ──┬──► 02 go-structured-logging ──► 03 go-otel-tracing ──┬──► 05 async-trace-propagation ──► 07 verification-and-signoff
                         │                                                      │                                    ▲
                         ├──────────────────────────────► 04 wallet-tracing ────┘                                    │
                         └──────────────────────────────► 06 grafana-dashboard ───────────────────────────────────────┘
```

- **01 first, always.** Nothing downstream is verifiable until you can see a span land in Jaeger. Its own acceptance bar is deliberately trivial (stack starts, Prometheus targets green) so it can't hide a broken collector.
- **02 → 03** is the Go vertical slice: logging first (fields present, empty), then tracing (fields populate). Both touch the same files; splitting them keeps each independently reviewable.
- **04** only needs 01, and is pure Java — it can run in parallel with 02/03.
- **05** is the milestone's centrepiece and needs both 03 (a real span to capture on the ledger side) and 04 (a trace that started at the wallet edge, so the captured context isn't a root span).
- **06** needs only 01 (Prometheus scraping) — the metrics it graphs already exist. Parallelizable throughout.
- **07** is the gate: it needs everything.

---

## Definition of done (milestone)

- [ ] `make up-obs` starts the core stack plus Jaeger, Prometheus, Grafana, and the OTel Collector; all Prometheus targets report `UP` (Task 01).
- [ ] `make up` still starts the core stack alone, with no observability containers and no startup errors (Tasks 01, 03).
- [ ] Both Go services emit structured JSON logs to stdout with `timestamp`, `level`, `service`, `trace_id`, `span_id`, `msg` (NFR-OBS-1) (Task 02).
- [ ] `POST /transfers` produces a single Jaeger trace spanning wallet-service → ledger-service → projection-service, ending at the projection write (NFR-OBS-5) (Tasks 03–05, verified in 07).
- [ ] The `traceparent` in the `LEDGER_POSTED` Kafka header and payload is a real, non-empty W3C value matching the originating trace (NFR-OBS-2) (Task 05).
- [ ] Log lines emitted during a traced request carry that trace's `trace_id` in all three services (Tasks 02–04).
- [ ] Grafana's `Transfers Overview` shows all six NFR-OBS-4 panels with live data (Task 06).
- [ ] `make test` and `make test-e2e` still pass with no observability stack running (Task 07).
- [ ] ADR-0012 and ADR-0013 written (Tasks 01, 05).

---

## Demo script (run at milestone review)

```sh
# 0. Full stack incl. observability
make down && make up-obs

# 1. Two wallets + funding (same as the M3 demo)
TOKEN=$(./scripts/generate-jwt.sh 00000000-0000-0000-0000-000000000001)
SRC=$(curl -s -X POST http://localhost:8080/wallets -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"currency":"BRL"}' | jq -r .walletId)
DST=$(curl -s -X POST http://localhost:8080/wallets -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"currency":"BRL"}' | jq -r .walletId)

curl -s -X POST http://localhost:8081/ledger/postings -H "Content-Type: application/json" \
  -d "{\"transactionId\":\"$(uuidgen)\",\"type\":\"DEPOSIT\",
       \"entries\":[
         {\"accountId\":\"00000000-0000-0000-0000-000000000001\",\"entryType\":\"DEBIT\",\"amount\":\"500.00\"},
         {\"accountId\":\"$SRC\",\"entryType\":\"CREDIT\",\"amount\":\"500.00\"}]}"

# 2. The traced transfer
curl -s -X POST http://localhost:8080/transfers \
  -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d "{\"sourceWalletId\":\"$SRC\",\"destinationWalletId\":\"$DST\",\"amount\":\"100.00\"}"

# 3. The money shot: one trace, three services, ending at the projection write
open http://localhost:16686        # Jaeger UI -> service: wallet-service -> newest trace

# 4. Same trace_id visible in every service's logs
docker compose logs ledger-service | tail -20 | jq -r 'select(.trace_id != "") | "\(.service) \(.trace_id) \(.msg)"'
docker compose logs projection-service | tail -20 | jq -r 'select(.trace_id != "") | "\(.service) \(.trace_id) \(.msg)"'

# 5. Non-empty traceparent on the wire (proves the Kafka hop carries context)
docker compose exec kafka kafka-console-consumer \
  --bootstrap-server localhost:9092 --topic ledger.posted.v1 \
  --from-beginning --max-messages 1 --property print.headers=true

# 6. Dashboards
open http://localhost:3000         # Grafana (admin/admin) -> Transfers Overview
open http://localhost:9090/targets # Prometheus -> all three targets UP
```
