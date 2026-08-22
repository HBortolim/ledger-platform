# Task 02 — Structured JSON logging in the Go services

**Status:** Complete
**Owner:** Ledger Service + Projection Service
**Depends on:** 01 (stack exists, though this task is verifiable from stdout alone)
**Blocks:** 03 (tracing populates the fields this task creates)
**Spec reference:** [`SPEC.md` NFR-OBS-1](../../SPEC.md), overview Global Constraints

---

## Goal

Both Go services currently log plain text through the standard library's `log` package — no JSON, no `service`, no `trace_id`. NFR-OBS-1 requires structured JSON on stdout with `trace_id`, `span_id`, `service`, `level`, `msg`. This task delivers exactly that, with the trace fields present but empty until Task 03 wires up a tracer.

The Wallet Service is already compliant (`application.yml:44`) and is the shape to match:

```json
{"timestamp":"2026-08-19 22:52:39.806","level":"INFO","service":"wallet-service","trace_id":"","span_id":"","msg":"..."}
```

## Interfaces

- **Produces (used by Task 03):** `logging.Setup(service string) *slog.Logger` in each service's `internal/logging` package. Task 03 does not change this API — once a tracer provider exists, `trace_id`/`span_id` populate automatically for any call site that passes a `context.Context`.
- **Consumes:** nothing from earlier tasks.

## A note on duplication

The two Go services are separate Go modules with no shared internal module, so this package is written twice — once per service, differing only in nothing at all. That mirrors the existing duplication between the two `internal/metrics/metrics.go` files and the two `internal/config/config.go` files. Do **not** introduce a shared module to avoid it; that's a repo-structure decision well outside this milestone.

## Step 1: Write the logging package (ledger-service)

**Files:**
- Create: `services/ledger-service/internal/logging/logging.go`

```go
// Package logging provides the structured JSON logger required by
// SPEC.md NFR-OBS-1: stdout, JSON, with timestamp/level/service/trace_id/
// span_id/msg. The field set and shape deliberately match the wallet-service's
// Logback pattern (services/wallet-service/src/main/resources/application.yml)
// so log lines from all three services collate cleanly.
package logging

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler decorates every record with the trace and span IDs of the
// active span in ctx. Until a tracer provider is installed (Task 03), the
// span context is invalid and both fields render as empty strings — the same
// thing the wallet-service emits today for an untraced request.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)

	var traceID, spanID string
	if sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	if sc.HasSpanID() {
		spanID = sc.SpanID().String()
	}

	r.AddAttrs(
		slog.String("trace_id", traceID),
		slog.String("span_id", spanID),
	)
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup must re-wrap: the embedded handler's versions
// return a bare slog.Handler, which would silently drop trace correlation
// for any logger derived via With(...).
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// Setup installs a JSON logger as the slog default and returns it. Call once,
// early in main, before anything logs.
func Setup(service string) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// slog's default key is "time"; NFR-OBS-1's shape (and the
			// wallet-service's pattern) uses "timestamp".
			if a.Key == slog.TimeKey {
				a.Key = "timestamp"
			}
			return a
		},
	})

	logger := slog.New(traceHandler{base}).With(slog.String("service", service))
	slog.SetDefault(logger)
	return logger
}
```

- [x] Create the file.
- [x] Add the dependency: `cd services/ledger-service && go get go.opentelemetry.io/otel/trace` — this is the small trace-API module only, not the SDK (Task 03 adds that). Record the resolved version in your report.
- [x] Run: `go build ./...` — expect success.

## Step 2: Write the failing test (ledger-service)

**Files:**
- Create: `services/ledger-service/internal/logging/logging_test.go`

```go
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// newTestLogger mirrors Setup's handler stack but writes to buf instead of
// stdout, so the emitted JSON can be asserted on directly.
func newTestLogger(buf *bytes.Buffer, service string) *slog.Logger {
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Key = "timestamp"
			}
			return a
		},
	})
	return slog.New(traceHandler{base}).With(slog.String("service", service))
}

func TestLogRecordHasRequiredNFROBS1Fields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, "ledger-service")

	logger.InfoContext(context.Background(), "posting committed")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v (raw: %s)", err, buf.String())
	}

	// NFR-OBS-1: logs include trace_id, span_id, service, level, msg.
	for _, key := range []string{"timestamp", "level", "service", "trace_id", "span_id", "msg"} {
		if _, ok := got[key]; !ok {
			t.Errorf("log record missing required field %q; got keys %v", key, keysOf(got))
		}
	}
	if got["service"] != "ledger-service" {
		t.Errorf("service = %v, want ledger-service", got["service"])
	}
	if got["msg"] != "posting committed" {
		t.Errorf("msg = %v, want %q", got["msg"], "posting committed")
	}
}

func TestUntracedContextYieldsEmptyTraceFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, "ledger-service")

	logger.InfoContext(context.Background(), "no active span")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if got["trace_id"] != "" {
		t.Errorf("trace_id = %v, want empty string for an untraced context", got["trace_id"])
	}
	if got["span_id"] != "" {
		t.Errorf("span_id = %v, want empty string for an untraced context", got["span_id"])
	}
}

// Derived loggers must keep trace correlation: a bare embedded WithAttrs
// would return the undecorated handler and silently drop trace_id/span_id.
func TestDerivedLoggerKeepsTraceFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, "ledger-service").With(slog.String("component", "outbox"))

	logger.InfoContext(context.Background(), "tick")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if _, ok := got["trace_id"]; !ok {
		t.Error("derived logger dropped trace_id; WithAttrs must re-wrap the handler")
	}
	if got["component"] != "outbox" {
		t.Errorf("component = %v, want outbox", got["component"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [x] Run: `cd services/ledger-service && go test ./internal/logging/... -v`
- [x] Expected: **PASS** (the package from Step 1 already satisfies these). If `TestDerivedLoggerKeepsTraceFields` fails, the `WithAttrs`/`WithGroup` re-wrapping in Step 1 is missing or wrong — that's the exact bug those two methods exist to prevent.

## Step 3: Replace `log` with `slog` in the ledger-service

**Files:**
- Modify: `services/ledger-service/cmd/server/main.go`
- Modify: `services/ledger-service/internal/outbox/worker.go`
- Modify: any other ledger-service file that imports `log` — find them with `grep -rln '"log"' --include='*.go' services/ledger-service`

Rules for the rewrite:

- Call `logging.Setup("ledger-service")` as the **first statement** in `main()`, before config load, so even a config failure logs as JSON.
- `log.Fatal(err)` / `log.Fatalf(...)` → `slog.Error(...)` followed by `os.Exit(1)`. `slog` has no Fatal; the two-line form is the idiom.
- `log.Printf(...)` → `slog.ErrorContext(ctx, ...)` for failures, `slog.InfoContext(ctx, ...)` for lifecycle events — **always the `...Context` variant where a `ctx` is in scope**, since that's what carries trace correlation once Task 03 lands.
- Move `fmt`-style interpolation into structured attributes rather than the message. The message becomes a stable, low-cardinality string; the varying parts become fields.

Worked example — `cmd/server/main.go`'s opening:

```go
func main() {
	logging.Setup("ledger-service")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("cannot connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()
	// ...
```

Worked example — `internal/outbox/worker.go`, showing the structured-attribute style:

```go
		case <-pollTicker.C:
			if err := w.Poll(ctx); err != nil {
				slog.ErrorContext(ctx, "outbox poll failed", slog.Any("error", err))
			}
			w.reportDepth(ctx)
```

and, from `Poll`:

```go
		hdrs, err := decodeHeaders(r.headers)
		if err != nil {
			metrics.OutboxPublishFailures.WithLabelValues("serialization").Inc()
			slog.ErrorContext(ctx, "outbox row: decode headers failed",
				slog.Int64("outbox_id", r.id), slog.Any("error", err))
			continue
		}
```

and, from `Sweep`:

```go
	if n := tag.RowsAffected(); n > 0 {
		slog.InfoContext(ctx, "outbox retention sweep completed", slog.Int64("deleted_rows", n))
	}
```

- [x] Rewrite every `log` call site in the ledger-service following those rules.
- [x] Confirm no stragglers: `grep -rn '"log"' --include='*.go' services/ledger-service` returns nothing outside `_test.go` files.
- [x] Run: `cd services/ledger-service && go build ./... && go test ./...` — expect a clean build and the full suite green (76+ tests).

## Step 4: Repeat for the projection-service

**Files:**
- Create: `services/projection-service/internal/logging/logging.go` (identical to Step 1, package doc adjusted)
- Create: `services/projection-service/internal/logging/logging_test.go` (identical to Step 2, with `"projection-service"` substituted for `"ledger-service"` in all three tests)
- Modify: `services/projection-service/cmd/server/main.go`
- Modify: `services/projection-service/internal/consumer/ledger_posted.go`
- Modify: any other file importing `log`

One projection-specific call needs care — `Run`'s connect failure is currently `log.Fatalf` inside a goroutine:

```go
func (c *LedgerPostedConsumer) Run(ctx context.Context) {
	if err := c.Connect(); err != nil {
		slog.ErrorContext(ctx, "projection consumer: cannot create kafka client", slog.Any("error", err))
		os.Exit(1)
	}
	defer c.Close()

	slog.InfoContext(ctx, "projection consumer started")
	// ...
```

`os.Exit(1)` from a goroutine preserves the existing `log.Fatalf` behavior exactly (both terminate the process immediately). Preserving it is correct for this task — changing the consumer's failure semantics is a behavior change that belongs in its own task, not smuggled into a logging refactor. Note it in your report if it bothers you.

Typical rewrites in `applyRecord`:

```go
		slog.ErrorContext(ctx, "projection consumer: poison message",
			slog.String("topic", r.Topic),
			slog.Int("partition", int(r.Partition)),
			slog.Int64("offset", r.Offset),
			slog.Any("error", err))
```

```go
		slog.ErrorContext(ctx, "projection consumer: apply failed, will retry",
			slog.String("transaction_id", event.TransactionID.String()),
			slog.Any("error", err))
```

- [x] Do the projection-service rewrite.
- [x] `cd services/projection-service && go get go.opentelemetry.io/otel/trace`
- [x] Confirm no stragglers: `grep -rn '"log"' --include='*.go' services/projection-service` returns nothing outside `_test.go` files.
- [x] Run: `go build ./... && go test ./...` — expect a clean build and 8+ tests green.

## Step 5: Verify the real output shape

Unit tests prove the handler; this proves the wiring in `main`.

- [x] Run: `make down && make up`
- [x] Run: `docker compose logs ledger-service | tail -20`
- [x] Expect every line to be valid JSON with the six fields. Confirm mechanically rather than by eye:

```sh
docker compose logs --no-log-prefix ledger-service | tail -20 | \
  jq -e 'has("timestamp") and has("level") and has("service") and has("trace_id") and has("span_id") and has("msg")' > /dev/null \
  && echo "ledger-service log shape OK"
```

- [x] Repeat for `projection-service`.
- [x] `trace_id` and `span_id` will be empty strings at this point. That is the correct result for this task — Task 03 fills them.

## Step 6: Commit

```bash
git add services/ledger-service/internal/logging services/projection-service/internal/logging \
        services/ledger-service/cmd services/ledger-service/internal/outbox \
        services/projection-service/cmd services/projection-service/internal/consumer \
        services/ledger-service/go.mod services/ledger-service/go.sum \
        services/projection-service/go.mod services/projection-service/go.sum
git commit -m "feat(observability): structured JSON logging in both Go services

NFR-OBS-1: stdout JSON with timestamp/level/service/trace_id/span_id/msg,
matching the wallet-service's existing Logback shape. Trace fields are wired
through a slog handler that reads the active span from context; they render
empty until the tracer provider lands in Task 03."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| `go test ./internal/logging/...` (both services) | all tests green, including the derived-logger case |
| `grep -rn '"log"' --include='*.go'` (both services, non-test files) | no matches |
| `go test ./...` (both services) | full suites still green — no regressions |
| Container log lines | valid JSON, all six NFR-OBS-1 fields, verified via the `jq -e` check |
| `trace_id` / `span_id` | present and empty (populated in Task 03) |

## Done when

Both Go services emit NFR-OBS-1-shaped JSON to stdout, the logging package is unit-tested in both, and no non-test file imports the stdlib `log` package.

## Notes

- Resist adding a `logger` field to every struct. `slog.SetDefault` plus the package-level `slog.XxxContext` functions is the idiomatic Go 1.21+ approach and keeps this diff small; dependency-injected loggers are a much larger refactor with no NFR-OBS-1 benefit.
- Log **level** stays at Info. Debug-level tracing chatter would drown the demo, and NFR-OBS-1 says nothing about level configurability.
- If you find a call site with no `ctx` in scope that clearly should have one, don't thread a new `ctx` parameter through just for logging — use the non-Context variant and note the site in your report. Plumbing context is a separate change.
