// Package sequence applies ledger entries to a wallet's projected balance exactly once, using
// the ledger's per-account entry number (Task 08, SEQ-4..SEQ-8). It is shared by the consumer
// and by the rebuild so there is one apply rule and one drain implementation.
package sequence

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Decision is what to do with an entry given the wallet's watermark.
type Decision int

const (
	Skip  Decision = iota // seq <= lastSeq: already applied (SEQ-5)
	Apply                 // seq == lastSeq+1: the next entry (SEQ-4)
	Park                  // seq > lastSeq+1: an earlier entry has not arrived (SEQ-6)
)

func (d Decision) String() string {
	switch d {
	case Skip:
		return "skip"
	case Apply:
		return "apply"
	case Park:
		return "park"
	}
	return "unknown"
}

// Decide is the whole dedupe rule: an integer comparison.
func Decide(seq, lastSeq int64) Decision {
	switch {
	case seq <= lastSeq:
		return Skip
	case seq == lastSeq+1:
		return Apply
	default:
		return Park
	}
}

// Outcome reports what ApplyEntry did.
type Outcome struct {
	Applied int  // entries applied to the balance, including parked ones drained afterwards
	Parked  bool // the entry was newly parked (false for a redelivery of an already-parked entry)
}

// ApplyEntry applies one entry to walletID inside tx, following SEQ-4..SEQ-7:
// it locks the wallet's row, then skips, applies (and drains) or parks the entry.
func ApplyEntry(ctx context.Context, tx pgx.Tx, walletID, entryID uuid.UUID, seq int64, delta decimal.Decimal) (Outcome, error) {
	// Lock first. Without the lock two consumer instances (or the consumer and a rebuild) can
	// both read last_seq = 4 and both apply seq = 5.
	lastSeq, err := LockWallet(ctx, tx, walletID)
	if err != nil {
		return Outcome{}, err
	}

	switch Decide(seq, lastSeq) {
	case Skip:
		return Outcome{}, nil
	case Park:
		tag, err := tx.Exec(ctx, `
			INSERT INTO projection_db.pending_entries (wallet_id, seq, entry_id, delta)
			VALUES ($1, $2, $3, $4::numeric)
			ON CONFLICT (wallet_id, seq) DO NOTHING`,
			walletID, seq, entryID, delta.StringFixed(2))
		if err != nil {
			return Outcome{}, fmt.Errorf("park entry %s (seq %d) of wallet %s: %w", entryID, seq, walletID, err)
		}
		return Outcome{Parked: tag.RowsAffected() > 0}, nil
	}

	if err := advance(ctx, tx, walletID, entryID, seq, delta); err != nil {
		return Outcome{}, err
	}
	drained, err := Drain(ctx, tx, walletID, seq)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Applied: 1 + drained}, nil
}

// LockWallet makes sure the wallet has a row and returns its last_seq with the row locked.
func LockWallet(ctx context.Context, tx pgx.Tx, walletID uuid.UUID) (lastSeq int64, err error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO projection_db.wallet_balances (wallet_id, balance, last_seq)
		VALUES ($1, 0, 0)
		ON CONFLICT (wallet_id) DO NOTHING`, walletID); err != nil {
		return 0, fmt.Errorf("ensure wallet %s row: %w", walletID, err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT last_seq FROM projection_db.wallet_balances WHERE wallet_id = $1 FOR UPDATE`,
		walletID).Scan(&lastSeq); err != nil {
		return 0, fmt.Errorf("lock wallet %s row: %w", walletID, err)
	}
	return lastSeq, nil
}

// advance adds delta to the balance and moves the watermark to seq in one statement.
func advance(ctx context.Context, tx pgx.Tx, walletID, entryID uuid.UUID, seq int64, delta decimal.Decimal) error {
	if _, err := tx.Exec(ctx, `
		UPDATE projection_db.wallet_balances
		   SET balance = balance + $2::numeric,
		       last_seq = $3,
		       last_entry_id = $4,
		       last_applied_at = now(), updated_at = now()
		 WHERE wallet_id = $1`,
		walletID, delta.StringFixed(2), seq, entryID); err != nil {
		return fmt.Errorf("apply entry %s (seq %d) to wallet %s: %w", entryID, seq, walletID, err)
	}
	return nil
}

// Drain applies parked entries that have become contiguous with lastSeq, in order, and returns
// how many it applied (SEQ-7). The caller must hold the wallet's row lock, in the same transaction.
func Drain(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, lastSeq int64) (int, error) {
	drained := 0
	for {
		var (
			entryID uuid.UUID
			deltaTx string
		)
		err := tx.QueryRow(ctx, `
			DELETE FROM projection_db.pending_entries
			 WHERE wallet_id = $1 AND seq = $2
			RETURNING entry_id, delta::text`,
			walletID, lastSeq+1).Scan(&entryID, &deltaTx)
		if errors.Is(err, pgx.ErrNoRows) {
			return drained, nil
		}
		if err != nil {
			return drained, fmt.Errorf("drain parked entries of wallet %s: %w", walletID, err)
		}
		delta, err := decimal.NewFromString(deltaTx)
		if err != nil {
			return drained, fmt.Errorf("parse parked delta %q of entry %s: %w", deltaTx, entryID, err)
		}
		lastSeq++
		if err := advance(ctx, tx, walletID, entryID, lastSeq, delta); err != nil {
			return drained, err
		}
		drained++
	}
}
