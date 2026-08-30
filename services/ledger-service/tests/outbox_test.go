package tests

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ledger-platform/ledger-service/internal/metrics"
	"github.com/ledger-platform/ledger-service/internal/outbox"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestOutboxWorker_PublishesToKafka posts a real transaction (giving a
// realistic outbox row via PostingRepository.Post), runs a single worker
// poll tick, and asserts the row is marked published and the message is
// consumable from Kafka with a matching payload.
func TestOutboxWorker_PublishesToKafka(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	bootstrap := setupKafka(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)
	debit, credit := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, debit, "200.00")
	tx := newBalancedTransfer(debit, credit, "100.00")
	if err := repo.Post(ctx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}

	producer, err := kgo.NewClient(kgo.SeedBrokers(bootstrap), kgo.ClientID("outbox-test-producer"))
	if err != nil {
		t.Fatalf("kgo.NewClient() = error %v, want nil", err)
	}
	t.Cleanup(producer.Close)

	worker := outbox.NewWorker(pool, producer)
	if err := worker.Poll(ctx); err != nil {
		t.Fatalf("Poll() = error %v, want nil", err)
	}

	var publishedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT published_at FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&publishedAt); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	if publishedAt == nil {
		t.Fatal("published_at is NULL, want it set after Poll()")
	}

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(bootstrap),
		kgo.ConsumeTopics("ledger.posted.v1"),
		kgo.ConsumeStartOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("kgo.NewClient() (consumer) = error %v, want nil", err)
	}
	t.Cleanup(consumer.Close)

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var envelope struct {
		SchemaVersion int    `json:"schemaVersion"`
		EventType     string `json:"eventType"`
		TransactionID string `json:"transactionId"`
		Entries       []struct {
			EntryID   string `json:"entryId"`
			AccountID string `json:"accountId"`
			EntryType string `json:"entryType"`
			Amount    string `json:"amount"`
		} `json:"entries"`
	}
	found := false
	for !found {
		fetches := consumer.PollFetches(fetchCtx)
		if fetchCtx.Err() != nil {
			t.Fatalf("timed out waiting for message on ledger.posted.v1: %v", fetchCtx.Err())
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if string(r.Key) != tx.ID.String() {
				return
			}
			if err := json.Unmarshal(r.Value, &envelope); err != nil {
				t.Fatalf("unmarshal kafka message: %v", err)
			}
			found = true
		})
	}

	if envelope.SchemaVersion != 1 || envelope.EventType != "LEDGER_POSTED" || envelope.TransactionID != tx.ID.String() {
		t.Errorf("kafka message envelope mismatch: %+v", envelope)
	}
	if len(envelope.Entries) != 2 {
		t.Errorf("kafka message entries = %d, want 2", len(envelope.Entries))
	}
}

// TestOutboxRowCarriesTraceparent proves the M2 placeholder is gone: a posting
// made inside an active span must persist that span's W3C trace context into
// the outbox row's headers column. Without this, the ledger->projection half
// of NFR-OBS-5's end-to-end trace cannot exist -- Kafka is the only path
// between them.
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

	// The header is the sole propagation carrier -- the payload must not
	// carry a traceparent field at all.
	var payload map[string]any
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := payload["traceparent"]; ok {
		t.Error("payload carries a traceparent field; propagation must be header-only")
	}
}

// TestOutboxWorker_KafkaDown_RowsStayPending points the worker at an
// unreachable broker and asserts rows stay unpublished, nothing panics, and
// the publish-failure metric increments.
func TestOutboxWorker_KafkaDown_RowsStayPending(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)
	debit, credit := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, debit, "200.00")
	tx := newBalancedTransfer(debit, credit, "50.00")
	if err := repo.Post(ctx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}

	failureCount := func() float64 {
		return testutil.ToFloat64(metrics.OutboxPublishFailures.WithLabelValues("kafka_unavailable")) +
			testutil.ToFloat64(metrics.OutboxPublishFailures.WithLabelValues("timeout"))
	}
	before := failureCount()

	producer, err := kgo.NewClient(
		kgo.SeedBrokers("127.0.0.1:1"), // deliberately unreachable
		kgo.ClientID("outbox-test-unreachable-producer"),
		kgo.RecordRetries(1),
		kgo.ProduceRequestTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("kgo.NewClient() = error %v, want nil", err)
	}
	t.Cleanup(producer.Close)

	worker := outbox.NewWorker(pool, producer)

	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = worker.Poll(pollCtx) // may or may not return an error depending on how franz-go surfaces the outage; what matters is the row/metric assertions below

	var publishedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT published_at FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&publishedAt); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	if publishedAt != nil {
		t.Errorf("published_at = %v, want NULL (Kafka unreachable)", *publishedAt)
	}

	after := failureCount()
	if after <= before {
		t.Errorf("ledger_outbox_publish_failures_total did not increase: before=%v after=%v", before, after)
	}
}

// TestOutboxWorker_RetentionSweep backdates a published row past the 7-day
// retention window and asserts Sweep() removes it.
func TestOutboxWorker_RetentionSweep(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)
	debit, credit := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, debit, "200.00")
	tx := newBalancedTransfer(debit, credit, "25.00")
	if err := repo.Post(ctx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE ledger_db.outbox SET published_at = now() - interval '8 days' WHERE key = $1`,
		tx.ID.String(),
	); err != nil {
		t.Fatalf("backdate outbox row: %v", err)
	}

	worker := outbox.NewWorker(pool, nil)
	if err := worker.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() = error %v, want nil", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&count); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if count != 0 {
		t.Errorf("outbox row count after sweep = %d, want 0 (row older than retention window should be deleted)", count)
	}
}
