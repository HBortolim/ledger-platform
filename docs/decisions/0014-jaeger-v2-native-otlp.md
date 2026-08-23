# ADR-0014: Services export directly to Jaeger v2, no standalone OTel Collector

**Status:** Accepted
**Date:** 2026-08-23

## Context

ADR-0012 built the trace pipeline as services → OTel Collector (`otel/opentelemetry-collector-contrib:0.101.0`) → Jaeger v1 (`jaegertracing/all-in-one:1.57`). Two things have changed since that decision was made:

- **Jaeger v1 reached end-of-life on 2025-12-31.** It no longer receives updates or security fixes.
- **Jaeger v2** is a ground-up rewrite built directly on the OpenTelemetry Collector framework — it is, architecturally, a specialized OTel Collector distribution with Jaeger's storage backend and query/UI service added as plugins. It terminates OTLP natively (gRPC 4317, HTTP 4318) with no translation layer needed, and no separate collector process in front of it.

That makes the standalone `otel-collector` container genuinely redundant, not just simplifiable. It's worth being precise about what the collector was actually doing in this stack, since its removal costs nothing today: its trace pipeline forwarded OTLP to Jaeger, and its metrics pipeline exported to a Prometheus-scrape endpoint on port 8889 — but neither Go service's tracing package ever constructed an OTLP metrics exporter (only `otlptracegrpc`, for traces), and `infrastructure/observability/prometheus.yml` never scraped port 8889. The collector's only *other* job besides trace forwarding was already provably inert, at both the infrastructure and application-code level.

## Decision

Services export OTLP directly to Jaeger v2 — no standalone OTel Collector in the pipeline:

- **Go services** (ledger-service, projection-service): `OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317` (gRPC), unchanged from before except for the hostname.
- **Spring service** (wallet-service): `MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_ENDPOINT=http://jaeger:4318/v1/traces` (HTTP/protobuf), same change.
- `docker-compose.observability.yml`'s `jaeger` service is bumped to `jaegertracing/jaeger:2.20.0`, runs with no mounted config file (default all-in-one mode: in-memory storage, OTLP receivers on 4317/4318, UI on 16686), and drops the v1-era `COLLECTOR_OTLP_ENABLED` env var — Jaeger v2 is config-file-driven, not env-var-driven, and that flag has no effect on it.

This explicitly **supersedes ADR-0012's Decision and its Alternative #1** (which rejected exporting directly to Jaeger, reasoning that `SPEC.md` §12 names the collector as a first-class artifact and that it's the intended central point for future policy — sampling, filtering, enrichment). That reasoning isn't wrong, it's just moot for now: there is no such policy today, and Jaeger v2 already absorbs the collector's role.

## Alternatives considered

1. **Keep the collector, only bump Jaeger's version** — rejected. With Jaeger v2 natively terminating OTLP, the collector's trace-forwarding hop adds a network round-trip and a container with no remaining job to do; it would be pure redundancy, not defense-in-depth.
2. **Stay on Jaeger v1** — rejected. It's end-of-life as of 2026-12-31: no further updates, no security fixes. Not a defensible position to hold going forward.

## Consequences

- One fewer container in the observability overlay (`jaeger`, `prometheus`, `grafana` — no `otel-collector`).
- `SPEC.md` §12's repo-layout tree no longer lists `otel-collector.yml`, since `infrastructure/observability/otel-collector.yml` is deleted.
- The collector's explicit `batch` processor is no longer separately configured on this hop. Considered and explicitly deemed irrelevant at this project's scale: each transfer produces single-digit spans across three services, and there's no rate limit or per-request cost against a self-hosted OTLP receiver the way there might be against a paid SaaS backend. Not worth re-litigating without a concrete throughput number that would make it matter.
- This is not a one-way door. If a genuine need for multi-backend fan-out or a real processing pipeline (sampling, filtering, PII scrubbing) arises later, a collector can be reintroduced in front of Jaeger without touching application code — the services already speak generic OTLP and know nothing about Jaeger specifically.
- The original discovery documented in ADR-0012 (the OTel Collector's built-in `jaeger` exporter component was deprecated in 2023 and removed from the Collector distribution) is unaffected by this change and remains accurate history — see ADR-0012 for that troubleshooting record.
- Fail-soft behavior is unchanged: with no `OTEL_EXPORTER_OTLP_ENDPOINT`/`MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_ENDPOINT` configured (e.g. plain `make up`), tracing stays disabled and every service runs normally — this was already proven under the collector-based pipeline and generalizes unchanged to Jaeger's absence.
