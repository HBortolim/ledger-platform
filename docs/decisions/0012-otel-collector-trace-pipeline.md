# ADR-0012: OpenTelemetry Collector trace pipeline

**Status:** Accepted
**Date:** 2026-08-20

## Context

Milestone 5 requires a distributed trace pipeline so that transactions can be traced across the three application services (wallet-service, ledger-service, projection-service) through Kafka and back. `docs/SPEC.md` §12 lists the OTel Collector as a first-class infrastructure artifact, but the existing `infrastructure/observability/otel-collector.yml` and `docker-compose.observability.yml` files have never been successfully run — no Makefile target connected them to `docker-compose.yml`, and no environment variables on the services pointed them at a collector.

The collector configuration exported traces via a `jaeger` exporter, which was a built-in part of the OpenTelemetry Collector up until mid-2023, when it was deprecated and subsequently removed from the distribution well before the pinned image version `otel/opentelemetry-collector-contrib:0.101.0`.

## Decision

Implement a trace pipeline: application services → OpenTelemetry Collector → Jaeger. The path is OTLP (OpenTelemetry Protocol):

- **Go services** (ledger-service, projection-service): gRPC/4317. Configured via `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317` in `docker-compose.yml`.
- **Spring service** (wallet-service): HTTP/protobuf/4318. Configured via `MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_ENDPOINT=http://otel-collector:4318/v1/traces` and `MANAGEMENT_TRACING_SAMPLING_PROBABILITY=1.0`. (Task 04 discovered that the originally-planned property, `management.otlp.tracing.endpoint` / `MANAGEMENT_OTLP_TRACING_ENDPOINT`, is deprecated in Spring Boot 4.0.6 in favor of `management.opentelemetry.tracing.export.otlp.endpoint` and silently fails to bind — tests passed with it, but no exporter was ever actually configured. `application.yml` documents the full discovery, including why an empty-string default doesn't work either (`OtlpTracingConfigurations.ConnectionDetails`'s `@ConditionalOnProperty` only checks presence, not non-blankness, so a blank value still activates the exporter and throws). The property key is omitted entirely from `application.yml`; Spring's relaxed environment-variable binding lets the compose-set env var bind directly, so the property is genuinely absent — not empty — when the collector isn't configured.)

The collector accepts OTLP on both 4317 (gRPC) and 4318 (HTTP), processes traces via a batch processor, and exports them to Jaeger (1.57) via OTLP on port 4317. Jaeger 1.35+ natively ingests OTLP; no translation is needed.

## The discovery

When the stack was first brought up to test, the collector exited immediately with:

```
error decoding 'exporters': unknown type: "jaeger" for id: "jaeger" (valid values: [clickhouse coralogix file logging awsxray mezmo opensearch prometheusremotewrite pulsar otlphttp influxdb instana kafka signalfx syslog skywalking zipkin debug datadog loadbalancing sapm azuremonitor carbon alibabacloud_logservice awscloudwatchlogs awsemf awskinesis honeycombmarker logicmonitor logzio opencensus azuredataexplorer elasticsearch googlecloudpubsub googlemanagedprometheus prometheus sentry sumologic dataset googlecloud loki splunk_hec nop otlp awss3 cassandra tencentcloud_logservice])
```

The `jaeger` exporter is not in the list of valid exporters. The solution: replace `exporters.jaeger` with `exporters.otlp/jaeger` targeting Jaeger's native OTLP ingest on port 4317 (not the old Jaeger collector protocol port 14250).

## Alternatives considered

1. **Skip the collector and have services export directly to Jaeger** — rejected: simpler (one fewer container), but `docs/SPEC.md` §12 designates the collector as a first-class artifact; the collector is the central place to add sampling, filtering, trace ID rewriting, or other policies later, and removing it now would undo that architectural choice.

2. **Keep the collector but pin an older image that still has the `jaeger` exporter** — rejected: the collector 0.101.0 is already pinned, and pinning to an ancient version that has a since-removed exporter to avoid a two-line config change is backwards and creates technical debt.

## Consequences

- The observability stack now brings up cleanly: collector, Jaeger, Prometheus, and Grafana start without errors.
- Application services need unconditional `OTEL_*` environment variables in `docker-compose.yml` to point at the collector. With the collector absent (e.g., `make up` without `-obs`), the exporters silently fail to connect and drop spans; the services keep running (overview decision #3: observability is optional).
- The collector is an additional container in the observability overlay, but not in the core `make up` stack. This maintains the invariant that the core services (Postgres, Kafka, the three application services) run standalone.
- Services never learn about Jaeger directly; the collector is the single integration point, allowing tracing policy to be centralized and updated without restarting services.
