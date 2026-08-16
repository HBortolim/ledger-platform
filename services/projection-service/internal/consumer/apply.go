package consumer

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// signedDelta converts an entry's unsigned amount into the signed delta
// applied to a wallet's cached balance, per SPEC.md §3.3: CREDIT is
// positive, DEBIT is negative.
func signedDelta(entryType string, amount decimal.Decimal) decimal.Decimal {
	if entryType == "DEBIT" {
		return amount.Neg()
	}
	return amount
}

// applyEntry idempotently applies one entry's signed delta to
// projection_db.wallet_balances. The WHERE clause on the ON CONFLICT branch
// makes a redelivery of the same entryID a no-op: applied reports whether
// this call actually changed the row (false means "already applied").
func applyEntry(ctx context.Context, tx pgx.Tx, walletID, entryID uuid.UUID, delta decimal.Decimal) (applied bool, err error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO projection_db.wallet_balances (wallet_id, balance, last_entry_id, last_applied_at, updated_at)
		VALUES ($1, $2::numeric, $3, now(), now())
		ON CONFLICT (wallet_id) DO UPDATE
		   SET balance = wallet_balances.balance + EXCLUDED.balance,
		       last_entry_id = EXCLUDED.last_entry_id,
		       last_applied_at = now(), updated_at = now()
		 WHERE wallet_balances.last_entry_id IS DISTINCT FROM EXCLUDED.last_entry_id`,
		walletID, delta.StringFixed(2), entryID,
	)
	if err != nil {
		return false, fmt.Errorf("apply entry %s to wallet %s: %w", entryID, walletID, err)
	}
	return tag.RowsAffected() > 0, nil
}

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

// applyEvent applies every entry in event within one DB transaction, records
// the consumer's last-processed offset in the same transaction, and commits.
// The whole event is classified as a single outcome, "applied" or "skipped" —
// matching the granularity of metrics.EventsProcessedTotal's "error" label,
// which is already per-message (a poison message has no entries to count).
// Because every entry in one event commits together in this one transaction,
// an event is never partially applied across delivery attempts: on first
// delivery every entry is newly applied ("applied"); on redelivery every
// entry was already applied by the prior commit ("skipped").
func applyEvent(ctx context.Context, pool *pgxpool.Pool, event ledgerPostedEvent, group, topic string, partition int32, offset int64) (result string, err error) {
	dbtx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	anyApplied := false
	for _, e := range event.Entries {
		ok, err := applyEntry(ctx, dbtx, e.AccountID, e.EntryID, signedDelta(e.EntryType, e.Amount))
		if err != nil {
			return "", err
		}
		if ok {
			anyApplied = true
		}
	}

	if err := upsertOffset(ctx, dbtx, group, topic, partition, offset); err != nil {
		return "", err
	}

	if err := dbtx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit projection apply: %w", err)
	}
	if anyApplied {
		return "applied", nil
	}
	return "skipped", nil
}
