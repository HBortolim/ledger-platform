package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ledger-platform/ledger-service/internal/metrics"
)

const (
	pollInterval      = 100 * time.Millisecond
	retentionInterval = 24 * time.Hour
	batchSize         = 100
	produceTimeout    = 5 * time.Second
)

// Worker drains ledger_db.outbox rows to Kafka: exactly-once from the
// database's perspective, at-least-once from the consumer's.
type Worker struct {
	pool     *pgxpool.Pool
	producer *kgo.Client
}

func NewWorker(pool *pgxpool.Pool, producer *kgo.Client) *Worker {
	return &Worker{pool: pool, producer: producer}
}

// Run blocks until ctx is cancelled, then returns nil. It does not close the
// producer — the caller owns the kgo.Client and flushes/closes it after Run
// returns.
func (w *Worker) Run(ctx context.Context) error {
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	retentionTicker := time.NewTicker(retentionInterval)
	defer retentionTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollTicker.C:
			if err := w.Poll(ctx); err != nil {
				slog.ErrorContext(ctx, "outbox poll failed", slog.Any("error", err))
			}
			w.reportDepth(ctx)
		case <-retentionTicker.C:
			if err := w.Sweep(ctx); err != nil {
				slog.ErrorContext(ctx, "outbox retention sweep failed", slog.Any("error", err))
			}
		}
	}
}

type outboxRow struct {
	id      int64
	topic   string
	key     string
	payload []byte
	headers []byte
}

// Poll drains up to batchSize unpublished outbox rows to Kafka in one pass.
// Exported so tests can invoke a single tick deterministically.
func (w *Worker) Poll(ctx context.Context) error {
	dbtx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	rows, err := dbtx.Query(ctx, `
		SELECT id, topic, key, payload, headers
		  FROM ledger_db.outbox
		 WHERE published_at IS NULL
		 ORDER BY id
		 LIMIT $1
		   FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		return fmt.Errorf("select unpublished outbox rows: %w", err)
	}

	var batch []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.id, &r.topic, &r.key, &r.payload, &r.headers); err != nil {
			rows.Close()
			return fmt.Errorf("scan outbox row: %w", err)
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate outbox rows: %w", err)
	}

	if len(batch) == 0 {
		return dbtx.Commit(ctx)
	}

	records := make([]*kgo.Record, 0, len(batch))
	produced := make([]outboxRow, 0, len(batch))
	for _, r := range batch {
		hdrs, err := decodeHeaders(r.headers)
		if err != nil {
			metrics.OutboxPublishFailures.WithLabelValues("serialization").Inc()
			slog.ErrorContext(ctx, "outbox row: decode headers failed",
				slog.Int64("outbox_id", r.id), slog.Any("error", err))
			continue
		}
		records = append(records, &kgo.Record{
			Topic:   r.topic,
			Key:     []byte(r.key),
			Value:   r.payload,
			Headers: hdrs,
		})
		produced = append(produced, r)
	}

	if len(records) == 0 {
		return dbtx.Commit(ctx)
	}

	// Bound the produce call: franz-go's default idempotent producer retries
	// indefinitely, and without a timeout here a Kafka outage would block
	// this tick (and the row lock held by dbtx) forever instead of failing
	// fast so the next tick can retry
	produceCtx, produceCancel := context.WithTimeout(ctx, produceTimeout)
	defer produceCancel()
	results := w.producer.ProduceSync(produceCtx, records...)

	published := make([]int64, 0, len(records))
	for i, res := range results {
		if res.Err != nil {
			metrics.OutboxPublishFailures.WithLabelValues(classifyProduceErr(res.Err)).Inc()
			slog.ErrorContext(ctx, "outbox row: produce failed",
				slog.Int64("outbox_id", produced[i].id), slog.Any("error", res.Err))
			continue
		}
		published = append(published, produced[i].id)
	}

	if len(published) > 0 {
		if _, err := dbtx.Exec(ctx,
			`UPDATE ledger_db.outbox SET published_at = now() WHERE id = ANY($1)`,
			published,
		); err != nil {
			return fmt.Errorf("mark outbox rows published: %w", err)
		}
	}

	return dbtx.Commit(ctx)
}

// Sweep deletes published outbox rows older than the 7-day retention window.
// Exported so tests can invoke it directly rather than waiting on a ticker.
func (w *Worker) Sweep(ctx context.Context) error {
	tag, err := w.pool.Exec(ctx, `
		DELETE FROM ledger_db.outbox
		 WHERE published_at IS NOT NULL
		   AND published_at < now() - interval '7 days'`)
	if err != nil {
		return fmt.Errorf("outbox retention sweep: %w", err)
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.InfoContext(ctx, "outbox retention sweep completed", slog.Int64("deleted_rows", n))
	}
	return nil
}

func (w *Worker) reportDepth(ctx context.Context) {
	var depth float64
	err := w.pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_db.outbox WHERE published_at IS NULL`,
	).Scan(&depth)
	if err != nil {
		slog.ErrorContext(ctx, "outbox depth query failed", slog.Any("error", err))
		return
	}
	metrics.OutboxDepth.Set(depth)
}

// classifyProduceErr maps franz-go produce errors to a small, low-cardinality
// reason label — never raw error text, per SPEC.md §7.3's cardinality note.
func classifyProduceErr(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, kgo.ErrRecordTimeout):
		return "timeout"
	default:
		return "kafka_unavailable"
	}
}
