# Task 03 — OTel SDK and HTTP tracing in the Go services

**Status:** Complete
**Owner:** Ledger Service + Projection Service
**Depends on:** 01 (a collector to export to), 02 (the log fields this task populates)
**Blocks:** 05 (needs a live span on the ledger side to capture into the outbox)
**Spec reference:** [`SPEC.md` NFR-OBS-2, NFR-OBS-5](../../SPEC.md), overview decisions #3 and #5

---

## Goal

Neither Go service has a single OpenTelemetry dependency today. This task installs a tracer provider and OTLP exporter in both, instruments the Ledger Service's business HTTP endpoints, and — as a free consequence of Task 02's handler — makes `trace_id` and `span_id` appear in Go log lines for the first time.

At the end of this task, `POST /ledger/postings` called directly with `curl` produces a visible `ledger-service` trace in Jaeger. Connecting that to the wallet edge is Task 04; carrying it across Kafka is Task 05.

## Interfaces

- **Consumes:** `logging.Setup(service string)` from Task 02 (already called first in each `main`).
- **Produces (used by Tasks 04 and 05):**
  - `observability.SetupTracing(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error)` in each service's `internal/observability` package.
  - A globally-installed W3C `TextMapPropagator`, which Task 05 relies on via `otel.GetTextMapPropagator()`.

## Step 1: Add the dependencies

- [x] Run, in `services/ledger-service`:

```sh
go get go.opentelemetry.io/otel
go get go.opentelemetry.io/otel/sdk
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
go get go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin
```

- [x] Run the same in `services/projection-service`, **omitting `otelgin`** — the projection service exposes only `/health/*` and `/metrics`, neither of which is traced (see Step 3's rationale).
- [x] Record every resolved version in your report. Versions are deliberately unpinned here because the correct current versions can't be verified from the plan; whatever `go get` resolves is the record.

## Step 2: Write the tracing package (ledger-service)

**Files:**
- Create: `services/ledger-service/internal/observability/tracing.go`

```go
// Package observability wires OpenTelemetry tracing per SPEC.md NFR-OBS-2/5.
package observability

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupTracing installs the global W3C propagator and, when an OTLP endpoint
// is configured, a batching tracer provider exporting to it. The returned
// shutdown function flushes pending spans and must be called before the
// process exits — without it, the batcher drops whatever it is holding.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset, tracing is disabled and the
// returned shutdown is a no-op (milestone-5 overview, decision #3): the
// service must run normally with no observability stack present, which is
// what `make up`, `make test`, and `make test-e2e` all rely on. The
// propagator is installed either way, so inbound traceparent headers are
// still parsed and outbound ones still written.
func SetupTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	// otlptracegrpc reads OTEL_EXPORTER_OTLP_ENDPOINT itself, including
	// inferring insecure transport from an http:// scheme — so the endpoint
	// is deliberately not passed explicitly here.
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// Always-on: a portfolio demo that drops the reviewer's one transfer
		// is worthless (milestone-5 overview, Global Constraints).
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
```

- [x] Create the file.
- [x] Run: `go build ./...` — expect success.

## Step 3: Instrument the ledger-service's business routes

**Files:**
- Modify: `services/ledger-service/internal/handler/routes.go`

Wrap only `/ledger` and `/admin`. `/health/live` is hit by Docker's healthcheck every 10 seconds and `/metrics` by Prometheus every 15 — tracing them floods Jaeger with noise that buries the one trace the demo is about.

```go
package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, postingHandler *PostingHandler) {
	hc := r.Group("/health")
	{
		hc.GET("/live", live)
		hc.GET("/ready", ready(pool))
	}

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Tracing is scoped to the business endpoints. Health and metrics are
	// polled continuously by infrastructure and are not worth spans; scoping
	// by route group avoids depending on otelgin's filter-option API, which
	// has moved between versions.
	traced := r.Group("", otelgin.Middleware("ledger-service"))

	v1 := traced.Group("/ledger")
	{
		v1.POST("/postings", postingHandler.PostPosting)
	}

	admin := traced.Group("/admin")
	{
		admin.GET("/ledger/transactions/:id", postingHandler.GetTransaction)
	}
}
```

- [x] Make the edit.
- [x] Run: `go build ./... && go test ./...` — expect the full ledger suite green. The existing `posting_http_test.go` exercises these routes; if it fails, the middleware has changed handler behavior and that's a real regression to investigate, not a test to adjust.

## Step 4: Call SetupTracing from the ledger-service main

**Files:**
- Modify: `services/ledger-service/cmd/server/main.go`

Insert immediately after the signal context is created and before the config load, so a config failure is still traced-adjacent and, more importantly, so shutdown is deferred as early as possible:

```go
	shutdownTracing, err := observability.SetupTracing(ctx, "ledger-service")
	if err != nil {
		slog.Error("cannot initialise tracing", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() {
		// Fresh context: ctx is already cancelled by the time this runs, and
		// a cancelled context would abort the flush this exists to perform.
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if err := shutdownTracing(flushCtx); err != nil {
			slog.Error("tracing shutdown error", slog.Any("error", err))
		}
	}()
```

- [x] Make the edit, adding the `internal/observability` import.
- [x] Run: `go build ./...` — expect success.

## Step 5: Repeat the provider setup for the projection-service

**Files:**
- Create: `services/projection-service/internal/observability/tracing.go` (identical to Step 2, package doc adjusted)
- Modify: `services/projection-service/cmd/server/main.go` (identical wiring to Step 4, with `"projection-service"`)

The projection service gets **no** HTTP instrumentation: its only routes are `/health/*` and `/metrics`. Its spans come from the Kafka consumer, which Task 05 adds. Installing the provider now means Task 05 has a tracer to use and Task 02's log fields start populating the moment a consumer span exists.

- [x] Create the file and wire `main`.
- [x] Run: `cd services/projection-service && go build ./... && go test ./...` — expect green.

## Step 6: Verify a real span reaches Jaeger

This is the acceptance test for the task and does not depend on Tasks 04 or 05.

- [x] Run: `make down && make up-obs`
- [x] Post a balanced transaction straight at the Ledger Service (the M2 demo call):

```sh
TX=$(uuidgen)
curl -s -X POST http://localhost:8081/ledger/postings \
  -H "Content-Type: application/json" \
  -d "{\"transactionId\":\"$TX\",\"type\":\"DEPOSIT\",
       \"entries\":[
         {\"accountId\":\"00000000-0000-0000-0000-000000000001\",\"entryType\":\"DEBIT\",\"amount\":\"100.00\"},
         {\"accountId\":\"$(uuidgen)\",\"entryType\":\"CREDIT\",\"amount\":\"100.00\"}]}"
```

- [x] Open `http://localhost:16686`, select service **`ledger-service`**, Find Traces.
- [x] Expect a trace containing a `POST /ledger/postings` span. Record its trace ID.
- [x] Confirm no `GET /health/live` or `GET /metrics` spans appear — Step 3's scoping is what prevents them, and their presence means the middleware got attached too broadly.

## Step 7: Verify trace correlation reached the logs

This closes the loop with Task 02 and is the first point where NFR-OBS-1's `trace_id` field carries real data.

- [x] Run: `docker compose logs --no-log-prefix ledger-service | jq -r 'select(.trace_id != null and .trace_id != "") | "\(.trace_id) \(.msg)"' | tail`
- [x] Expect at least one line whose `trace_id` matches the trace ID recorded in Step 6.
- [x] If every `trace_id` is still empty: the emitting call site is using `slog.Info(...)` rather than `slog.InfoContext(ctx, ...)`, or is running outside the request's context. Note which, and fix the call site rather than the handler.

## Step 8: Verify tracing stays optional

Guard for overview decision #3 — this is the check that keeps CI and the E2E suite working.

- [x] Run: `make down && make up` (core stack only, no collector, `OTEL_EXPORTER_OTLP_ENDPOINT` still set in compose but pointing at a host that doesn't exist)
- [x] Expect all three services to start and report healthy. The OTLP exporter will fail to connect in the background; that must not surface as a startup failure or a crash.
- [x] Run: `make test-e2e` — expect TST-E2E-1..4 green.
- [x] Additionally confirm the unset-endpoint path: `cd services/ledger-service && OTEL_EXPORTER_OTLP_ENDPOINT= go test ./...` — green, with no exporter goroutines left running.

## Step 9: Commit

```bash
git add services/ledger-service/internal/observability services/ledger-service/internal/handler/routes.go \
        services/ledger-service/cmd services/ledger-service/go.mod services/ledger-service/go.sum \
        services/projection-service/internal/observability services/projection-service/cmd \
        services/projection-service/go.mod services/projection-service/go.sum
git commit -m "feat(observability): OTel tracer provider and HTTP tracing in the Go services

NFR-OBS-2/5: OTLP/gRPC exporter to the collector, W3C propagation installed
unconditionally, and otelgin scoped to the ledger's business routes so health
and metrics polling doesn't flood Jaeger. Tracing no-ops when
OTEL_EXPORTER_OTLP_ENDPOINT is unset, keeping make up and the E2E suite
independent of the observability stack."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| `POST /ledger/postings` via curl | a `ledger-service` trace with a `POST /ledger/postings` span appears in Jaeger |
| Health/metrics spans | absent from Jaeger |
| Ledger log lines during that request | carry the matching non-empty `trace_id` |
| `make up` (no collector) | all services healthy; no crash, no startup failure |
| `make test-e2e` | TST-E2E-1..4 green |
| `go test ./...` (both services) | full suites green |

## Done when

A `curl` to the Ledger Service produces a visible Jaeger trace whose ID appears in that service's JSON logs, and the core stack still runs and passes E2E with no observability stack present.

## Notes

- **Do not instrument pgx.** Per overview decision #5, per-SQL spans (`otelpgx`) are explicitly out of scope; the end-to-end story NFR-OBS-5 asks for is complete without them and they can be added later without reworking anything here.
- `sdktrace.WithBatcher` is deliberate over `WithSyncer`: synchronous export would put the collector on the critical path of every posting, which is exactly the coupling an outbox-based architecture exists to avoid.
- If `otelgin`'s import path or middleware signature differs from what's written above in the version `go get` resolves, follow the resolved API and note the deviation in your report — the route-group scoping approach is the requirement, the exact call is not.
- The `defer shutdownTracing(...)` in `main` runs after `srv.Shutdown`, which is the right order: flush spans once the server has stopped accepting requests.
