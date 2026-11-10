package consumer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ledger-platform/projection-service/internal/sequence"
	"github.com/ledger-platform/projection-service/internal/utils"
)

// upsertOffset records the last-processed offset for a consumer group. The
// schema's primary key is consumer_group alone (SPEC.md §6.3), so this row
// is a last-processed pointer for observability, not the source of resume
// truth — actual resume-on-restart is Kafka's own committed group offsets,
// written by (*kgo.Client).CommitRecords immediately after this transaction
// commits.
func upsertOffset(ctx context.Context, tx pgx.Tx, group, topic string, partition int32, offset int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO projection_db.projection_offsets (consumer_group, topic, partition, offset_committed, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (consumer_group) DO UPDATE
		   SET topic = EXCLUDED.topic,
		       partition = EXCLUDED.partition,
		       offset_committed = EXCLUDED.offset_committed,
		       updated_at = now()`,
		group, topic, partition, offset,
	)
	if err != nil {
		return fmt.Errorf("upsert projection offset: %w", err)
	}
	return nil
}

// eventResult is what applyEvent did, for the caller's metrics.
type eventResult struct {
	Outcome     string // applied | parked | skipped
	NewlyParked int    // entries newly parked behind a gap (SEQ-6)
	Touched     bool   // anything was parked or drained, so pending_entries may have changed
}

// resolveSeq returns the entry's per-account sequence number. A schemaVersion 2 event carries
// it; a schemaVersion 1 event (published before Task 08, possibly still in Kafka) does not, so
// it is looked up from the ledger -- the entry is committed, so it exists. After this lookup a
// v1 entry takes exactly the same path as a v2 one: there is one dedupe rule, not two.
// TODO: delete the lookup once no v1 event can remain within the topic's retention.
func resolveSeq(ctx context.Context, tx pgx.Tx, e entryEvent) (int64, error) {
	if e.Seq > 0 {
		return e.Seq, nil
	}
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT seq FROM ledger_db.ledger_entries WHERE id = $1`, e.EntryID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("look up seq of v1 entry %s: %w", e.EntryID, err)
	}
	return seq, nil
}

// applyEvent applies every entry in event within one DB transaction, records the consumer's
// last-processed offset in the same transaction, and commits. Each entry is applied, skipped or
// parked by sequence.ApplyEntry (SEQ-4..SEQ-7). The whole event is classified as a single
// outcome, matching the granularity of metrics.EventsProcessedTotal: "applied" if any entry
// applied (including parked ones its arrival unblocked), "parked" if none applied and any was
// parked, otherwise "skipped".
func applyEvent(ctx context.Context, pool *pgxpool.Pool, event ledgerPostedEvent, group, topic string, partition int32, offset int64) (eventResult, error) {
	dbtx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return eventResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	var applied, newlyParked int
	touched := false
	for _, e := range event.Entries {
		seq, err := resolveSeq(ctx, dbtx, e)
		if err != nil {
			return eventResult{}, err
		}
		out, err := sequence.ApplyEntry(ctx, dbtx, e.AccountID, e.EntryID, seq, utils.SignedDelta(e.EntryType, e.Amount))
		if err != nil {
			return eventResult{}, err
		}
		applied += out.Applied
		if out.Parked {
			newlyParked++
		}
		if out.Parked || out.Applied > 1 {
			touched = true
		}
	}

	if err := upsertOffset(ctx, dbtx, group, topic, partition, offset); err != nil {
		return eventResult{}, err
	}

	if err := dbtx.Commit(ctx); err != nil {
		return eventResult{}, fmt.Errorf("commit projection apply: %w", err)
	}

	res := eventResult{NewlyParked: newlyParked, Touched: touched, Outcome: "skipped"}
	switch {
	case applied > 0:
		res.Outcome = "applied"
	case newlyParked > 0:
		res.Outcome = "parked"
	}
	return res, nil
}
