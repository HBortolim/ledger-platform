# Task 01 — Stand up the observability stack

**Status:** Complete
**Owner:** Infrastructure
**Depends on:** nothing
**Blocks:** every other task in this milestone (nothing is verifiable until a span can land in Jaeger)
**Spec reference:** [`SPEC.md` §11 — Milestone 5](../../SPEC.md), §12 (repo layout), NFR-OBS-3/4/5, overview decisions #1 and #3

---

## Goal

Make `make up-obs` bring up a working Jaeger + Prometheus + Grafana + OTel Collector stack alongside the core services, and give every service the environment variable it needs to find the collector. This task ships no application code — it makes the next six tasks verifiable.

## Why this is first, and what's actually broken

`docker-compose.observability.yml` and `otel-collector.yml` already exist but there is no Makefile target that runs them and no `OTEL_*` variable anywhere in `docker-compose.yml`, so nothing has ever pointed at them. The collector config exports via:

```yaml
exporters:
  jaeger:
    endpoint: jaeger:14250
```

The Collector's `jaeger` exporter was deprecated in mid-2023 and subsequently **removed** from the distribution — well before the pinned `otel/opentelemetry-collector-contrib:0.101.0`. A collector started with this config will fail on startup with an unknown-exporter error. Jaeger 1.57 accepts OTLP natively on 4317, so the fix is an `otlp` exporter.

**Verify this before assuming it:** start the collector as-is once (Step 1) and read the error. If it starts cleanly, the exporter still exists in this version — record that in your report and keep the config, but still complete Steps 2–5.

## Step 1: Confirm the collector is actually broken

- [x] Run: `docker compose -f docker-compose.observability.yml up otel-collector`
- [x] Expected: the container exits with an error naming the `jaeger` exporter as unknown/invalid. Capture the exact message for your report — it justifies the change in Step 2 and belongs in ADR-0012.
- [x] Stop it: `docker compose -f docker-compose.observability.yml down`

## Step 2: Fix the collector config

**Files:**
- Modify: `infrastructure/observability/otel-collector.yml`

Replace the whole file with:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch:

exporters:
  # Jaeger 1.35+ ingests OTLP natively. The Collector's dedicated `jaeger`
  # exporter was deprecated in 2023 and removed well before the 0.101.0 image
  # pinned in docker-compose.observability.yml — see ADR-0012.
  otlp/jaeger:
    endpoint: jaeger:4317
    tls:
      insecure: true
  prometheus:
    endpoint: "0.0.0.0:8889"

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp/jaeger]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [prometheus]
```

- [x] Make the edit above.

## Step 3: Fix the observability compose file

**Files:**
- Modify: `docker-compose.observability.yml`

Two problems with the current file: Jaeger publishes host ports `4317`/`4318`, which will collide once anything else wants them, and nothing declares that services reach the collector over the compose network (they do — no host publishing needed for service-to-service traffic). Only Jaeger's UI and the Prometheus/Grafana UIs need host ports.

Replace the whole file with:

```yaml
services:
  jaeger:
    image: jaegertracing/all-in-one:1.57
    environment:
      COLLECTOR_OTLP_ENABLED: "true"
    ports:
      # UI only. OTLP ingest happens over the compose network from the
      # collector, so 4317/4318 deliberately are not published to the host.
      - "16686:16686"

  prometheus:
    image: prom/prometheus:v2.52.0
    volumes:
      - ./infrastructure/observability/prometheus.yml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"

  grafana:
    image: grafana/grafana:10.4.3
    environment:
      GF_SECURITY_ADMIN_PASSWORD: admin
    volumes:
      - ./infrastructure/observability/grafana:/etc/grafana/provisioning:ro
    ports:
      - "3000:3000"
    depends_on:
      - prometheus

  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.101.0
    command: ["--config=/etc/otelcol-contrib/config.yaml"]
    volumes:
      - ./infrastructure/observability/otel-collector.yml:/etc/otelcol-contrib/config.yaml:ro
    ports:
      - "8889:8889"   # prometheus exporter scrape target
    depends_on:
      - jaeger
```

Note the deliberate omissions: no `version:` key (obsolete in modern Compose and emits a warning), and no `depends_on` from the application services to the collector — per overview decision #3, a missing collector must never block or break the core stack.

- [x] Make the edit above.

## Step 4: Point the services at the collector

**Files:**
- Modify: `docker-compose.yml`

Add to the `environment:` block of **`ledger-service`** and **`projection-service`** (Go, gRPC exporter → port 4317):

```yaml
      OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector:4317
```

Add to the `environment:` block of **`wallet-service`** (Spring's OTLP exporter defaults to http/protobuf → port 4318):

```yaml
      MANAGEMENT_OTLP_TRACING_ENDPOINT: http://otel-collector:4318/v1/traces
      MANAGEMENT_TRACING_SAMPLING_PROBABILITY: "1.0"
```

These are set unconditionally in `docker-compose.yml` even though the collector only exists in the observability overlay. That is intentional and safe: with no collector running, the exporters fail to connect, log at debug, and drop spans — the services themselves keep working (overview decision #3). Task 03 and Task 04 implement the code that reads these.

- [x] Make both edits.

## Step 5: Add the Makefile targets

**Files:**
- Modify: `Makefile`

Add `up-obs` and `down-obs` targets, and add both to `.PHONY`:

```makefile
COMPOSE_OBS := -f docker-compose.yml -f docker-compose.observability.yml

up-obs:
	docker compose $(COMPOSE_OBS) up --build -d

down-obs:
	docker compose $(COMPOSE_OBS) down -v
```

Leave the existing `up` and `down` targets exactly as they are — `make up` must stay core-only.

- [x] Make the edit, adding `up-obs down-obs` to the `.PHONY` line.

## Step 6: Verify the whole stack

- [x] Run: `make down && make up-obs`
- [x] Wait for startup, then check every container is running: `docker compose -f docker-compose.yml -f docker-compose.observability.yml ps` — expect no container in `Exit`/`Restarting` state, especially `otel-collector`.
- [x] Collector started clean: `docker compose -f docker-compose.yml -f docker-compose.observability.yml logs otel-collector` — expect `Everything is ready. Begin running and processing data.` and **no** exporter errors.
- [x] Prometheus targets: open `http://localhost:9090/targets` — all three of `wallet-service`, `ledger-service`, `projection-service` must show `UP`. (Their metrics already exist from M2/M3; this proves the scrape config is correct.)
- [x] Jaeger UI loads: `http://localhost:16686`. The service dropdown will be **empty** — correct at this stage, since no service emits spans until Tasks 03/04.
- [x] Grafana loads: `http://localhost:3000` (admin/admin). No dashboards yet — that's Task 06.

## Step 7: Verify the core stack is still independent

This is the guard for overview decision #3 — do not skip it.

- [x] Run: `make down && make up`
- [x] Expect only the core containers (postgres, kafka, the two migrate jobs, topics-init, and the three services) — no jaeger/prometheus/grafana/collector.
- [x] All three services healthy: `docker compose ps` shows the health-checked services as `healthy`.
- [x] Run: `make test-e2e` — expect TST-E2E-1..4 green against the core-only stack.

## Step 8: Write ADR-0012 and commit

**Files:**
- Create: `docs/decisions/0012-otel-collector-trace-pipeline.md`

Follow the existing ADR format (see `docs/decisions/0008-system-account-may-overdraw.md` for the house style: Status, Date, Context, Decision, Alternatives considered, Consequences). It must record:

- **Context:** M5 needs a trace pipeline; a collector config already existed but had never been run.
- **Decision:** services → OTel Collector → Jaeger, over OTLP. Go via gRPC/4317, Spring via http-protobuf/4318.
- **The discovery:** the `jaeger` exporter is removed from Collector 0.101.0 (quote the actual startup error you captured in Step 1); replaced with `otlp/jaeger` targeting Jaeger's native OTLP ingest.
- **Alternatives considered:** (a) services export directly to Jaeger — simpler, one fewer container, but discards a file `SPEC.md` §12 lists as a first-class artifact and gives up the central place to add sampling/filtering later; (b) keep the collector but pin an older image that still has the `jaeger` exporter — rejected, pinning to a removed-feature version to avoid a two-line config change is backwards.
- **Consequences:** one more container in the observability overlay; services never learn about Jaeger directly; observability stays fully optional (decision #3).

- [x] Write the ADR.
- [x] Commit:

```bash
git add infrastructure/observability/otel-collector.yml docker-compose.observability.yml \
        docker-compose.yml Makefile docs/decisions/0012-otel-collector-trace-pipeline.md
git commit -m "build(observability): fix collector exporter and wire the observability stack

The collector's jaeger exporter was removed upstream well before the pinned
0.101.0 image, so this stack could never have started. Replaced with an otlp
exporter against Jaeger's native OTLP ingest, added make up-obs/down-obs, and
pointed all three services at the collector. make up stays core-only."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| `make up-obs` | every container running; `otel-collector` logs no exporter error |
| `http://localhost:9090/targets` | all three service targets `UP` |
| `http://localhost:16686` | Jaeger UI loads (empty service list is correct at this stage) |
| `http://localhost:3000` | Grafana loads |
| `make up` (core only) | no observability containers; all services healthy |
| `make test-e2e` against core-only stack | TST-E2E-1..4 green |
| ADR-0012 | written, quoting the real collector startup error |

## Done when

`make up-obs` brings up a clean stack with green Prometheus targets, `make up` still works standalone, and ADR-0012 records why the collector config had to change.

## Notes

- If the collector *does* start with the original `jaeger` exporter, don't force the change to fit the plan — record the actual version behavior in your report, keep whichever config works, and adjust ADR-0012's framing to match reality. The rest of the task is unaffected either way.
- Grafana's provisioning volume is mounted read-only here. Task 06 writes files into `infrastructure/observability/grafana/` on the host; a restart picks them up.
- Don't add a healthcheck to the collector unless you find you need one — its readiness isn't a gate for anything, by design.
