package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/projection-service/internal/sequence"
	"github.com/ledger-platform/projection-service/internal/utils"
)

type RebuildRepository struct {
	pool *pgxpool.Pool
}

func NewRebuildRepository(pool *pgxpool.Pool) *RebuildRepository {
	return &RebuildRepository{pool: pool}
}

// Rebuild discards a wallet's cached balance and recomputes it from the
// authoritative ledger (SPEC.md §9.7). The projection is derivable, so
// recovery is replay, never a manual correction.
func (r *RebuildRepository) Rebuild(ctx context.Context, walletID uuid.UUID) (utils.Result, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return utils.Result{}, fmt.Errorf("begin rebuild tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the wallet's row BEFORE reading the ledger, creating it first if it does not exist yet
	// (sequence.LockWallet) so there is always a row to lock. A concurrent consumer apply for this
	// wallet then blocks on this same row until we commit and re-reads last_seq, which is the
	// watermark we are about to write: it skips what our ledger read already covers and applies
	// anything newer on top. Replay is therefore exact even while the consumer lags (Task 08).
	if _, err := sequence.LockWallet(ctx, tx, walletID); err != nil {
		return utils.Result{}, err
	}

	// seq is the true per-wallet order: (created_at, id) was only a best effort.
	rows, err := tx.Query(ctx, `
		SELECT id, entry_type, amount::text, seq
		  FROM ledger_db.ledger_entries
		 WHERE account_id = $1
		 ORDER BY seq`,
		walletID)
	if err != nil {
		return utils.Result{}, fmt.Errorf("read ledger entries for wallet %s: %w", walletID, err)
	}
	defer rows.Close()

	var balanceTxt string
	balance := decimal.Zero
	applied := 0
	var lastEntryID *uuid.UUID
	var lastSeq int64
	for rows.Next() {
		var (
			entryID   uuid.UUID
			entryType string
			amountTxt string
			seq       int64
		)
		if err := rows.Scan(&entryID, &entryType, &amountTxt, &seq); err != nil {
			return utils.Result{}, fmt.Errorf("scan ledger entry for wallet %s: %w", walletID, err)
		}
		amount, err := decimal.NewFromString(amountTxt)
		if err != nil {
			return utils.Result{}, fmt.Errorf("parse amount %q of entry %s: %w", amountTxt, entryID, err)
		}
		balance = balance.Add(utils.SignedDelta(entryType, amount))
		applied++
		lastEntryID = &entryID
		lastSeq = seq
	}
	if err := rows.Err(); err != nil {
		return utils.Result{}, fmt.Errorf("iterate ledger entries for wallet %s: %w", walletID, err)
	}
	rows.Close()

	// Replace the row's values (not merge) and set the watermark to the highest seq replayed (SEQ-8).
	var rebuiltAt time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE projection_db.wallet_balances
		   SET balance = $2::numeric,
		       last_entry_id = $3,
		       last_seq = $4,
		       last_applied_at = now(), updated_at = now()
		 WHERE wallet_id = $1
		RETURNING updated_at`,
		walletID, balance.StringFixed(2), lastEntryID, lastSeq,
	).Scan(&rebuiltAt); err != nil {
		return utils.Result{}, fmt.Errorf("write rebuilt balance for wallet %s: %w", walletID, err)
	}

	// Parked entries at or below the watermark are covered by the replay: discard them. Those
	// that are now contiguous with it are applied by the same drain the consumer uses (SEQ-8).
	if _, err := tx.Exec(ctx,
		`DELETE FROM projection_db.pending_entries WHERE wallet_id = $1 AND seq <= $2`,
		walletID, lastSeq); err != nil {
		return utils.Result{}, fmt.Errorf("discard covered parked entries of wallet %s: %w", walletID, err)
	}
	drained, err := sequence.Drain(ctx, tx, walletID, lastSeq)
	if err != nil {
		return utils.Result{}, err
	}
	if drained > 0 {
		if err := tx.QueryRow(ctx,
			`SELECT balance::text, updated_at FROM projection_db.wallet_balances WHERE wallet_id = $1`,
			walletID).Scan(&balanceTxt, &rebuiltAt); err != nil {
			return utils.Result{}, fmt.Errorf("re-read rebuilt balance of wallet %s: %w", walletID, err)
		}
		if balance, err = decimal.NewFromString(balanceTxt); err != nil {
			return utils.Result{}, fmt.Errorf("parse rebuilt balance %q: %w", balanceTxt, err)
		}
		applied += drained
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.Result{}, fmt.Errorf("commit rebuild of wallet %s: %w", walletID, err)
	}

	return utils.Result{
		WalletID:       walletID,
		Balance:        balance,
		EntriesApplied: applied,
		RebuiltAt:      rebuiltAt,
	}, nil
}
