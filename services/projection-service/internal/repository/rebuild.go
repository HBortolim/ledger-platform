package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

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

	// Lock the wallet's row BEFORE reading the ledger. A concurrent consumer apply for this
	// wallet blocks on this same row until we commit, then re-evaluates its last_entry_id
	// guard against the balance we just wrote -- so a delta the consumer folds in either lands
	// before our ledger read (we see it and it's a no-op for the consumer) or after our commit
	// (the consumer applies it on top, since its last_entry_id differs from ours).
	//
	// Residual gap, stated honestly: this locks one wallet's projection row, not the ledger
	// itself. An entry committed to the ledger between our SELECT below and our commit is not
	// lost -- the consumer picks it up afterwards -- but if the consumer had already applied an
	// entry that our own read re-derives (e.g. during a lagging consumer), the guard's single
	// last_entry_id cannot tell "already applied" from "not yet applied" and can double-count.
	// See Task 08 (per-wallet entry sequence) for the fix; this task only closes the race this
	// lock is built to close.
	if _, err := tx.Exec(ctx,
		`SELECT 1 FROM projection_db.wallet_balances WHERE wallet_id = $1 FOR UPDATE`,
		walletID); err != nil {
		return utils.Result{}, fmt.Errorf("lock wallet %s balance row: %w", walletID, err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM projection_db.wallet_balances WHERE wallet_id = $1`,
		walletID); err != nil {
		return utils.Result{}, fmt.Errorf("clear wallet %s balance row: %w", walletID, err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id, entry_type, amount::text
		  FROM ledger_db.ledger_entries
		 WHERE account_id = $1
		 ORDER BY created_at, id`,
		walletID)
	if err != nil {
		return utils.Result{}, fmt.Errorf("read ledger entries for wallet %s: %w", walletID, err)
	}
	defer rows.Close()

	balance := decimal.Zero
	applied := 0
	var lastEntryID *uuid.UUID
	for rows.Next() {
		var (
			entryID   uuid.UUID
			entryType string
			amountTxt string
		)
		if err := rows.Scan(&entryID, &entryType, &amountTxt); err != nil {
			return utils.Result{}, fmt.Errorf("scan ledger entry for wallet %s: %w", walletID, err)
		}
		amount, err := decimal.NewFromString(amountTxt)
		if err != nil {
			return utils.Result{}, fmt.Errorf("parse amount %q of entry %s: %w", amountTxt, entryID, err)
		}
		balance = balance.Add(utils.SignedDelta(entryType, amount))
		applied++
		lastEntryID = &entryID
	}
	if err := rows.Err(); err != nil {
		return utils.Result{}, fmt.Errorf("iterate ledger entries for wallet %s: %w", walletID, err)
	}

	var rebuiltAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO projection_db.wallet_balances (wallet_id, balance, last_entry_id, last_applied_at, updated_at)
		VALUES ($1, $2::numeric, $3, now(), now())
		ON CONFLICT (wallet_id) DO UPDATE
		   SET balance = EXCLUDED.balance,
		       last_entry_id = EXCLUDED.last_entry_id,
		       last_applied_at = now(), updated_at = now()
		RETURNING updated_at`,
		walletID, balance.StringFixed(2), lastEntryID,
	).Scan(&rebuiltAt); err != nil {
		return utils.Result{}, fmt.Errorf("write rebuilt balance for wallet %s: %w", walletID, err)
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
