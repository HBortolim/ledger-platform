package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ledger-platform/projection-service/internal/metrics"
)

// maxStuckWalletsLogged bounds the error lines one report can emit, so a mass loss does not
// flood the log; the gauge carries the true count.
const maxStuckWalletsLogged = 20

// refreshPendingGauge sets projection_pending_entries from the table. Best-effort observability:
// a failure must never affect the apply path.
func (c *LedgerPostedConsumer) refreshPendingGauge(ctx context.Context) {
	var n int64
	if err := c.pool.QueryRow(ctx, `SELECT count(*) FROM projection_db.pending_entries`).Scan(&n); err != nil {
		slog.WarnContext(ctx, "projection consumer: cannot read pending entry count", slog.Any("error", err))
		return
	}
	metrics.PendingEntries.Set(float64(n))
}

// reportPendingLoop runs ReportPending on its own ticker. It cannot ride on Tick: PollFetches
// blocks while the topic is idle, and an idle topic is exactly when a gap that never fills must
// still be reported.
func (c *LedgerPostedConsumer) reportPendingLoop(ctx context.Context) {
	ticker := time.NewTicker(min(10*time.Second, c.alertAfter))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.ReportPending(ctx)
		}
	}
}

// ReportPending refreshes the gauge and raises SEQ-9: an error log, naming the wallet and the
// missing seq, for every wallet whose oldest parked entry is older than the alert threshold. It
// never blocks, retries or guesses across the gap -- the recovery is a rebuild. Exported so
// integration tests can drive it deterministically.
func (c *LedgerPostedConsumer) ReportPending(ctx context.Context) {
	c.refreshPendingGauge(ctx)

	rows, err := c.pool.Query(ctx, `
		SELECT p.wallet_id, w.last_seq + 1 AS missing_seq, count(*) AS parked, min(p.parked_at) AS oldest
		  FROM projection_db.pending_entries p
		  JOIN projection_db.wallet_balances w ON w.wallet_id = p.wallet_id
		 WHERE p.parked_at < now() - make_interval(secs => $1)
		 GROUP BY p.wallet_id, w.last_seq
		 ORDER BY min(p.parked_at)
		 LIMIT $2`, c.alertAfter.Seconds(), maxStuckWalletsLogged)
	if err != nil {
		slog.WarnContext(ctx, "projection consumer: cannot check for stuck pending entries", slog.Any("error", err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var (
			wallet      uuid.UUID
			missingSeq  int64
			parked      int
			oldestSince time.Time
		)
		if err := rows.Scan(&wallet, &missingSeq, &parked, &oldestSince); err != nil {
			slog.WarnContext(ctx, "projection consumer: cannot read stuck pending entries", slog.Any("error", err))
			return
		}
		slog.ErrorContext(ctx, "projection gap did not fill: an entry is missing; rebuild the wallet",
			slog.String("wallet_id", wallet.String()),
			slog.Int64("missing_seq", missingSeq),
			slog.Int("parked_entries", parked),
			slog.Time("oldest_parked_at", oldestSince),
			slog.Duration("alert_after", c.alertAfter))
	}
}
