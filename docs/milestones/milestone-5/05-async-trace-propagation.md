# Task 05 — Trace context across outbox → Kafka → projection

**Status:** Complete
**Owner:** Ledger Service + Projection Service
**Depends on:** 03 (a live ledger span to capture), 04 (so that span has a wallet-side parent)
**Blocks:** 07
**Spec reference:** [`SPEC.md` NFR-OBS-2, NFR-OBS-5](../../SPEC.md), §7.3 (`LEDGER_POSTED` schema), §7.5 (outbox worker), overview decisions #2 and #4

---

## Goal

This is the milestone's centrepiece. Everything before it produces two disconnected halves: a wallet→ledger trace that stops at the HTTP response, and a projection service that writes balances with no idea which request caused them. NFR-OBS-5 requires one trace "from `POST /transfers` through to the projection write" — and the only path between those two points is asynchronous, through the outbox table and Kafka.

The schema for this was designed in M2 and left deliberately unfinished:

```go
// services/ledger-service/internal/repository/posting.go:362
Traceparent     string         `json:"traceparent"` // wired in M5; placeholder for now
```
```go
// posting.go:390 and :396
Traceparent:     "",
headersJSON, err := json.Marshal(map[string]string{"traceparent": ""})
```

This task fills in those placeholders and picks the context back up on the far side.

## Why the context must be persisted, not read at publish time

The outbox worker publishes on its own goroutine, on a 100 ms tick, in a batch, potentially seconds after the posting committed — and after a Kafka outage, potentially much later. There is no ambient request context to read at that moment. The trace context has to be captured while the posting request is still in flight and stored in the row, which is exactly what the `headers` JSONB column was added for in M2.

## Interfaces

- **Consumes:** `otel.GetTextMapPropagator()`, installed unconditionally by Task 03's `observability.SetupTracing`.
- **Produces:** a non-empty W3C `traceparent` on every `ledger.posted.v1` record header and in every `LEDGER_POSTED` payload, consumed by the projection service (and, later, by any other consumer).

## Step 1: Inject trace context when writing the outbox row

**Files:**
- Modify: `services/ledger-service/internal/repository/posting.go` (`insertOutboxRow`, around lines 372–409)

```go
func insertOutboxRow(ctx context.Context, tx pgx.Tx, t domain.LedgerTransaction) error {
	// Capture the active trace context into the row. The worker publishes
	// this asynchronously -- a tick later at best, after a Kafka outage at
	// worst -- so there is no ambient context to read at publish time; it has
	// to travel with the row. Empty when tracing is disabled, which is the
	// pre-M5 behaviour and remains valid (ADR-0013).
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	eps := make([]entryPayload, len(t.Entries))
	for i, e := range t.Entries {
		eps[i] = entryPayload{
			EntryID:   e.ID,
			AccountID: e.AccountID,
			EntryType: string(e.EntryType),
			Amount:    e.Amount.String(),
		}
	}
	payload := ledgerPostedPayload{
		SchemaVersion:   1,
		EventID:         uuid.New(),
		EventType:       "LEDGER_POSTED",
		OccurredAt:      time.Now().UTC(),
		TransactionID:   t.ID,
		TransactionType: string(t.Type),
		Entries:         eps,
		// Mirrors the header for human debuggability and §7.3 schema
		// conformance. The header is authoritative for propagation --
		// consumers MUST prefer it (ADR-0013).
		Traceparent: carrier["traceparent"],
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	headersJSON, err := json.Marshal(map[string]string(carrier))
	if err != nil {
		return fmt.Errorf("marshal outbox headers: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO ledger_db.outbox(topic, key, payload, headers)
		 VALUES ($1, $2, $3, $4)`,
		"ledger.posted.v1", t.ID.String(), payloadJSON, headersJSON,
	)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}
```

Also update the struct comment at line 362, which now describes something that has happened:

```go
	// Mirror of the Kafka traceparent header; the header is authoritative
	// for propagation (ADR-0013). Empty when tracing is disabled.
	Traceparent     string         `json:"traceparent"`
```

Add the imports `go.opentelemetry.io/otel` and `go.opentelemetry.io/otel/propagation`.

Note the graceful degradation: with no active span, `Inject` writes nothing, the carrier stays empty, and the row gets `{}` headers and `""` traceparent — behaviorally equivalent to today's behavior (both mean "no trace context"), though not byte-identical: today's code writes `json.Marshal(map[string]string{"traceparent": ""})` → `{"traceparent":""}` (one empty-valued header), while the empty `propagation.MapCarrier{}` marshals to `{}` (zero headers). No consumer distinguishes the two, so every existing outbox and E2E test keeps passing unchanged.

- [x] Make the edits.
- [x] Run: `cd services/ledger-service && go build ./...`

## Step 2: Write the failing test for injection

**Files:**
- Modify: `services/ledger-service/tests/outbox_test.go`

```go
// TestOutboxRowCarriesTraceparent proves the M2 placeholder is gone: a posting
// made inside an active span must persist that span's W3C trace context into
// the outbox row, in both the headers column (authoritative) and the payload
// field (mirror). Without this, the ledger->projection half of NFR-OBS-5's
// end-to-end trace cannot exist -- Kafka is the only path between them.
func TestOutboxRowCarriesTraceparent(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	// A provider with no exporter still produces real, sampled span contexts,
	// which is all injection needs.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	spanCtx, span := tp.Tracer("test").Start(ctx, "posting request")
	wantTraceID := span.SpanContext().TraceID().String()

	source, dest := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, source, "500.00")
	tx := newBalancedTransfer(source, dest, "100.00")
	if err := repo.Post(spanCtx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}
	span.End()

	var headersJSON, payloadJSON []byte
	err = pool.QueryRow(ctx,
		`SELECT headers, payload FROM ledger_db.outbox WHERE key = $1`, tx.ID.String(),
	).Scan(&headersJSON, &payloadJSON)
	if err != nil {
		t.Fatalf("query outbox row: %v", err)
	}

	var headers map[string]string
	if err := json.Unmarshal(headersJSON, &headers); err != nil {
		t.Fatalf("unmarshal headers: %v", err)
	}
	traceparent := headers["traceparent"]
	if traceparent == "" {
		t.Fatal("outbox headers carry no traceparent; the M2 placeholder is still in place")
	}
	// W3C format: 00-<32 hex trace>-<16 hex span>-<2 hex flags>
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`).MatchString(traceparent) {
		t.Errorf("traceparent = %q, want a well-formed W3C traceparent", traceparent)
	}
	if !strings.Contains(traceparent, wantTraceID) {
		t.Errorf("traceparent = %q, want it to carry the originating trace ID %s", traceparent, wantTraceID)
	}

	var payload struct {
		Traceparent string `json:"traceparent"`
	}
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Traceparent != traceparent {
		t.Errorf("payload traceparent = %q, want it to mirror the header %q", payload.Traceparent, traceparent)
	}
}
```

Add imports as needed: `encoding/json`, `regexp`, `strings`, `go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/propagation`, `sdktrace "go.opentelemetry.io/otel/sdk/trace"`.

- [x] Add the test.
- [x] Run: `go test ./tests/... -run TestOutboxRowCarriesTraceparent -v`
- [x] Expected: **PASS** with Step 1 applied. Confirm it has teeth by temporarily reverting Step 1's injection to the old `map[string]string{"traceparent": ""}` — it must fail on "the M2 placeholder is still in place". Restore afterwards.

## Step 3: Add a publish span in the outbox worker

**Files:**
- Modify: `services/ledger-service/internal/outbox/headers.go` (add the carrier)
- Modify: `services/ledger-service/internal/outbox/worker.go` (`Poll`)

The publish span is what makes outbox lag visible in the Jaeger waterfall — the gap between the posting span ending and the publish span starting *is* the lag, which is a headline property of this architecture.

In `headers.go`:

```go
// headerCarrier adapts a franz-go record header slice to OTel's
// TextMapCarrier so W3C trace context can be read back out of a row's stored
// headers. Extract-only: Set is intentionally a no-op, since nothing here
// ever writes context back into an already-persisted row.
type headerCarrier []kgo.RecordHeader

func (c headerCarrier) Get(key string) string {
	for _, h := range c {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c headerCarrier) Set(string, string) {}

func (c headerCarrier) Keys() []string {
	keys := make([]string, len(c))
	for i, h := range c {
		keys[i] = h.Key
	}
	return keys
}
```

In `worker.go`, add a package-level tracer and extend `Poll`'s record loop. Only the changed regions are shown; keep everything else as-is:

```go
var tracer = otel.Tracer("github.com/ledger-platform/ledger-service/internal/outbox")
```

```go
	records := make([]*kgo.Record, 0, len(batch))
	produced := make([]outboxRow, 0, len(batch))
	spans := make([]trace.Span, 0, len(batch))
	for _, r := range batch {
		hdrs, err := decodeHeaders(r.headers)
		if err != nil {
			metrics.OutboxPublishFailures.WithLabelValues("serialization").Inc()
			slog.ErrorContext(ctx, "outbox row: decode headers failed",
				slog.Int64("outbox_id", r.id), slog.Any("error", err))
			continue
		}

		// Continue the trace that produced this row rather than starting a
		// new one: the publish is logically part of that posting, even though
		// it runs on this worker's goroutine much later.
		rowCtx := otel.GetTextMapPropagator().Extract(ctx, headerCarrier(hdrs))
		_, span := tracer.Start(rowCtx, "outbox publish",
			trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithAttributes(
				attribute.String("messaging.system", "kafka"),
				attribute.String("messaging.destination.name", r.topic),
				attribute.Int64("outbox.row_id", r.id),
			))

		records = append(records, &kgo.Record{
			Topic:   r.topic,
			Key:     []byte(r.key),
			Value:   r.payload,
			Headers: hdrs,
		})
		produced = append(produced, r)
		spans = append(spans, span)
	}
```

and, in the results loop:

```go
	published := make([]int64, 0, len(records))
	for i, res := range results {
		if res.Err != nil {
			metrics.OutboxPublishFailures.WithLabelValues(classifyProduceErr(res.Err)).Inc()
			slog.ErrorContext(ctx, "outbox row: produce failed",
				slog.Int64("outbox_id", produced[i].id), slog.Any("error", res.Err))
			spans[i].RecordError(res.Err)
			spans[i].SetStatus(codes.Error, "produce failed")
			spans[i].End()
			continue
		}
		spans[i].End()
		published = append(published, produced[i].id)
	}
```

`records`, `produced`, and `spans` stay index-aligned because all three are appended to in the same iteration and skipped together on the decode-failure path — the same invariant the existing `produced[i]` indexing already relies on.

Imports to add: `go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/attribute`, `go.opentelemetry.io/otel/codes`, `go.opentelemetry.io/otel/trace`.

- [x] Make the edits.
- [x] Run: `go build ./... && go test ./...` — expect the full ledger suite green, including the existing `TestOutboxWorker_PublishesToKafka` (TST-INT-2).

## Step 4: Extract the context in the projection consumer

**Files:**
- Modify: `services/projection-service/internal/consumer/ledger_posted.go`

Add the same carrier type (this file's package has no equivalent yet) and a tracer, then wrap `applyRecord`:

```go
var tracer = otel.Tracer("github.com/ledger-platform/projection-service/internal/consumer")

// recordCarrier adapts a consumed record's headers to OTel's TextMapCarrier.
// Extract-only; Set is a no-op.
type recordCarrier []kgo.RecordHeader

func (c recordCarrier) Get(key string) string {
	for _, h := range c {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c recordCarrier) Set(string, string) {}

func (c recordCarrier) Keys() []string {
	keys := make([]string, len(c))
	for i, h := range c {
		keys[i] = h.Key
	}
	return keys
}
```

At the top of `applyRecord`:

```go
func (c *LedgerPostedConsumer) applyRecord(ctx context.Context, r *kgo.Record) {
	// Continue the ledger's trace. The Kafka header is authoritative; the
	// payload's traceparent field is a mirror for debugging only (ADR-0013).
	// This is the hop that makes NFR-OBS-5's single end-to-end trace possible.
	ctx = otel.GetTextMapPropagator().Extract(ctx, recordCarrier(r.Headers))
	ctx, span := tracer.Start(ctx, "projection apply",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.source.name", r.Topic),
			attribute.Int64("messaging.kafka.message.offset", r.Offset),
		))
	defer span.End()

	event, err := decodeLedgerPostedEvent(r.Value)
	// ... rest unchanged
```

Because `ctx` is reassigned, every existing `slog.ErrorContext(ctx, ...)` / `slog.InfoContext(ctx, ...)` call further down the function now emits the correct `trace_id` with no further changes — that is the payoff from Task 02's handler.

Add error recording on the two failure paths already in the function:

```go
	if err != nil {
		slog.ErrorContext(ctx, "projection consumer: poison message", /* ...existing attrs... */)
		span.RecordError(err)
		span.SetStatus(codes.Error, "poison message")
		metrics.EventsProcessedTotal.WithLabelValues("error").Inc()
		// ...
	}
```

```go
	result, err := applyEvent(ctx, c.pool, event, c.groupID, r.Topic, r.Partition, r.Offset)
	if err != nil {
		slog.ErrorContext(ctx, "projection consumer: apply failed, will retry", /* ...existing attrs... */)
		span.RecordError(err)
		span.SetStatus(codes.Error, "apply failed")
		return
	}
```

- [x] Make the edits.
- [x] Run: `cd services/projection-service && go build ./... && go test ./...` — expect green.

## Step 5: Write the failing test for extraction

**Files:**
- Modify: `services/projection-service/tests/testhelpers_test.go` (teach the producer helper to set a traceparent)
- Modify: `services/projection-service/tests/consumer_test.go` (the new test, beside `TestConsumer_AppliesLedgerPosted_ToWalletBalances`)

First, the helper. `produceLedgerPosted` currently produces a record with no headers at all (`testhelpers_test.go:178`), so it can't exercise extraction. Add a `traceparent string` parameter as its **last** argument:

```go
func produceLedgerPosted(t *testing.T, bootstrap string, transactionID uuid.UUID, occurredAt time.Time, entries []entryFixture, traceparent string) {
```

Set it on both the payload and the record header, mirroring what the ledger-service now writes:

```go
	body, err := json.Marshal(payload{
		SchemaVersion:   1,
		EventID:         uuid.New(),
		EventType:       "LEDGER_POSTED",
		OccurredAt:      occurredAt,
		TransactionID:   transactionID,
		TransactionType: "TRANSFER",
		Entries:         eps,
		Traceparent:     traceparent,
	})
```

```go
	var hdrs []kgo.RecordHeader
	if traceparent != "" {
		hdrs = []kgo.RecordHeader{{Key: "traceparent", Value: []byte(traceparent)}}
	}
	res := producer.ProduceSync(produceCtx, &kgo.Record{
		Topic:   "ledger.posted.v1",
		Key:     []byte(transactionID.String()),
		Value:   body,
		Headers: hdrs,
	})
```

Then update the three existing call sites in `consumer_test.go` (in `TestConsumer_AppliesLedgerPosted_ToWalletBalances`, `TestConsumer_DuplicateDelivery_IsNoOp`, and `TestConsumer_PoisonMessage_CountsAndContinues` — check each; the poison-message test may build its record inline instead) to pass `""` as the new final argument. An empty traceparent produces a header-less record, exactly as before, so those tests keep asserting the same behavior.

Now the new test. It produces a record carrying a known `traceparent`, runs one consumer tick, and asserts the span created during apply joined that trace. An in-memory span recorder means no collector is needed:

```go
// TestConsumerJoinsProducerTrace proves the Kafka hop preserves trace context
// (NFR-OBS-2): a record whose header carries a traceparent must be applied
// inside a span belonging to that same trace, not a fresh root trace. This is
// what makes NFR-OBS-5's end-to-end view possible.
func TestConsumerJoinsProducerTrace(t *testing.T) {
	// Reuse this package's existing harness exactly as
	// TestConsumer_AppliesLedgerPosted_ToWalletBalances does (Postgres +
	// Kafka containers, migrations, a connected consumer). Do not stand up a
	// second set of containers.

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const wantTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	// Produce via the helper extended above, passing the traceparent as the
	// final argument.
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), entries, traceparent)

	// Drive exactly one Tick, as the sibling tests in this file do, rather
	// than racing the async Run loop.
	if err := c.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() = error %v, want nil", err)
	}

	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatal("no spans recorded; the consumer created none for the applied record")
	}
	var applySpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "projection apply" {
			applySpan = s
			break
		}
	}
	if applySpan == nil {
		t.Fatalf("no %q span recorded; got %d spans", "projection apply", len(spans))
	}
	if got := applySpan.SpanContext().TraceID().String(); got != wantTraceID {
		t.Errorf("apply span trace ID = %s, want %s (the consumer started a new trace "+
			"instead of continuing the producer's)", got, wantTraceID)
	}
}
```

Import `"go.opentelemetry.io/otel/sdk/trace/tracetest"` for the recorder.

- [x] Write the test against this package's existing harness — reuse its container/consumer setup rather than standing up a second one.
- [x] Run it. Expected: **PASS**. Confirm it has teeth by temporarily removing the `Extract` line in Step 4 — the trace ID must then differ from `wantTraceID`. Restore afterwards.

## Step 6: See the whole trace end to end

- [x] Run: `make down && make up-obs`
- [x] Run the full transfer flow (steps 1–2 of the milestone overview's demo script).
- [x] Open `http://localhost:16686`, service `wallet-service`, Find Traces, open the newest.
- [x] Expect **one** trace containing, in order: a wallet-service `POST /transfers` server span, a wallet-service client span, a ledger-service `POST /ledger/postings` server span, a ledger-service `outbox publish` producer span, and a projection-service `projection apply` consumer span.
- [x] Confirm the visible time gap between the posting span and the publish span — that gap is the outbox lag, and being able to see it is the point of Step 3.
- [x] Confirm the header is non-empty on the wire:

```sh
docker compose exec kafka kafka-console-consumer \
  --bootstrap-server localhost:9092 --topic ledger.posted.v1 \
  --from-beginning --max-messages 1 --property print.headers=true
```

Expect a `traceparent:00-...` header, not an empty value.

## Step 7: Write ADR-0013 and commit

**Files:**
- Create: `docs/decisions/0013-async-trace-context-propagation.md`

Following the house ADR format, record:

- **Context:** NFR-OBS-5 wants one end-to-end trace; the only wallet→projection path is async via outbox + Kafka.
- **Decision:** parent–child via W3C `traceparent`, captured at posting time into the outbox `headers` column, published as a Kafka record header, extracted by consumers. The header is authoritative; the payload's `traceparent` field mirrors it for debugging and §7.3 conformance.
- **Alternatives considered:** (a) **span links** — the usual messaging-semantic-conventions choice, and better for high-fanout batch consumers, but it produces *separate* traces and would fail NFR-OBS-5's "traced end-to-end … through to the projection write" outright; (b) **payload field as the carrier** — works, but puts propagation concerns inside the business schema and breaks any consumer that only reads headers; (c) **re-reading ambient context at publish time** — impossible, the worker publishes on its own goroutine long after the request ended.
- **Consequences:** traces for a busy wallet can be long-lived; a consumer that fans one event into many downstream calls will nest under the original transfer's trace, which is desirable here and would need revisiting if fan-out grows. Empty context (tracing disabled) degrades to today's exact behavior.

- [x] Write the ADR.
- [x] Commit:

```bash
git add services/ledger-service/internal/repository/posting.go \
        services/ledger-service/internal/outbox services/ledger-service/tests \
        services/projection-service/internal/consumer services/projection-service/tests \
        docs/decisions/0013-async-trace-context-propagation.md
git commit -m "feat(observability): propagate trace context across outbox, Kafka, and projection

Replaces M2's hardcoded empty traceparent placeholder. The posting request's
trace context is captured into the outbox row, published as a Kafka header,
and extracted by the projection consumer, so a transfer is one trace from the
wallet edge through to the balance write (NFR-OBS-5). Adds an outbox publish
span, which makes outbox lag visible as a gap in the Jaeger waterfall."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| `TestOutboxRowCarriesTraceparent` | passes; fails when Step 1's injection is reverted |
| `TestConsumerJoinsProducerTrace` | passes; fails when Step 4's `Extract` is removed |
| `go test ./...` (both Go services) | full suites green, incl. TST-INT-2/3/4 |
| Jaeger, one transfer | a single trace spanning wallet → ledger → outbox publish → projection apply |
| `kafka-console-consumer --property print.headers=true` | non-empty `traceparent` header |
| Projection log lines during apply | carry the originating transfer's `trace_id` |
| ADR-0013 | written |

## Done when

One transfer produces one Jaeger trace that ends at the projection write, and both round-trip tests pass while provably failing against the un-instrumented code.

## Notes

- **Tracing-disabled must stay a no-op.** With no active span, `Inject` writes nothing and the row gets `{}` headers — identical to pre-M5 behavior. Run `make test-e2e` against a core-only stack before committing to confirm no existing test noticed a difference.
- The two carrier types (`headerCarrier`, `recordCarrier`) are near-identical but live in different Go modules, same as the duplicated `logging` and `metrics` packages. Don't build a shared module to deduplicate ~20 lines.
- If a consumer ever needs the payload's `traceparent` instead of the header, that's a bug in the consumer, not a reason to change the producer — ADR-0013 makes the header authoritative precisely so this doesn't get relitigated per-consumer.
- The `projection apply` span deliberately covers decode + apply + offset commit, so a poison message or a retry is visible as a failed span rather than a missing one.
