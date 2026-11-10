package repository

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/shopspring/decimal"
)

// Arbitrary but fixed. All services share one Postgres database and advisory keys are one
// namespace per database, so any future advisory lock must use a different key.
const reconciliationLockKey int64 = 0x6C656467_0006 // "ledg" + task number

type ReconciliationRepository struct{ pool *pgxpool.Pool }

func NewReconciliationRepository(pool *pgxpool.Pool) *ReconciliationRepository {
	return &ReconciliationRepository{pool: pool}
}

func (r *ReconciliationRepository) UnbalancedTransactions(ctx context.Context, window time.Duration, limit int) (domain.InvariantFindings, error) {
	unbalancedTransactionsSQL := `
		SELECT id, net::text, entry_count, COUNT(*) OVER () AS total
		FROM (
				SELECT t.id, t.created_at,
					COALESCE(SUM(CASE WHEN e.entry_type = 'CREDIT' THEN e.amount ELSE -e.amount END), 0) AS net,
					COUNT(e.id) AS entry_count
				FROM ledger_db.ledger_transactions t
				LEFT JOIN ledger_db.ledger_entries e ON e.transaction_id = t.id
				WHERE t.created_at >= now() - make_interval(secs => $1)
				GROUP BY t.id, t.created_at
			) x
		WHERE net <> 0 OR entry_count = 0
		ORDER BY created_at, id
		LIMIT $2
	`

	rows, err := r.pool.Query(ctx, unbalancedTransactionsSQL, window.Seconds(), limit)
	if err != nil {
		return domain.InvariantFindings{}, fmt.Errorf("query unbalanced transactions: %w", err)
	}
	defer rows.Close()

	var out domain.InvariantFindings
	for rows.Next() {
		var (
			id                uuid.UUID
			netTxt            string
			entryCount, total int
		)
		if err := rows.Scan(&id, &netTxt, &entryCount, &total); err != nil {
			return domain.InvariantFindings{}, fmt.Errorf("scan unbalanced transaction: %w", err)
		}

		net, err := decimal.NewFromString(netTxt)
		if err != nil {
			return domain.InvariantFindings{}, fmt.Errorf("parse net amount: %w", err)
		}
		out.Total = total
		out.Sample = append(out.Sample, domain.UnbalancedTransaction{TransactionID: id, Net: net, EntryCount: entryCount})
	}
	if err := rows.Err(); err != nil {
		return domain.InvariantFindings{}, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil

}

func (r *ReconciliationRepository) ProjectionDrift(ctx context.Context, grace time.Duration, limit int) (domain.DriftFindings, error) {
	driftedSQL := `
		SELECT wallet_id, kind, COALESCE(projected, 0)::text, COALESCE(actual, 0)::text, COUNT(*) OVER () AS total
		FROM (
				SELECT COALESCE(b.wallet_id, e.account_id) AS wallet_id,
					b.balance          AS projected,
					e.ledger_balance   AS actual,
					e.newest_entry_at  AS newest_entry_at,
					CASE WHEN b.wallet_id IS NULL THEN 'PROJECTION_MISSING'
							WHEN e.account_id IS NULL THEN 'PROJECTION_ORPHAN'
							ELSE 'MISMATCH' END AS kind
				FROM projection_db.wallet_balances b
				FULL OUTER JOIN (
						SELECT account_id,
							SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE -amount END) AS ledger_balance,
							MAX(created_at) AS newest_entry_at
						FROM ledger_db.ledger_entries
						GROUP BY account_id
				) e ON e.account_id = b.wallet_id
			) d
		WHERE COALESCE(projected, 0) <> COALESCE(actual, 0)
		AND (newest_entry_at IS NULL OR newest_entry_at < now() - make_interval(secs => $1))
		ORDER BY wallet_id
		LIMIT $2

	`
	rows, err := r.pool.Query(ctx, driftedSQL, grace.Seconds(), limit)
	if err != nil {
		return domain.DriftFindings{}, fmt.Errorf("query projection drift: %w", err)
	}
	defer rows.Close()

	var out domain.DriftFindings
	for rows.Next() {
		var (
			walletID                uuid.UUID
			kindStr                 string
			projectedTxt, actualTxt string
			total                   int
		)
		if err := rows.Scan(&walletID, &kindStr, &projectedTxt, &actualTxt, &total); err != nil {
			return domain.DriftFindings{}, fmt.Errorf("scan projection drift: %w", err)
		}

		projected, err := decimal.NewFromString(projectedTxt)
		if err != nil {
			return domain.DriftFindings{}, fmt.Errorf("parse projected amount: %w", err)
		}
		actual, err := decimal.NewFromString(actualTxt)
		if err != nil {
			return domain.DriftFindings{}, fmt.Errorf("parse actual amount: %w", err)
		}
		out.Total = total
		out.Sample = append(out.Sample, domain.ProjectionDrift{WalletID: walletID, Kind: domain.DriftKind(kindStr), Projected: projected, Actual: actual})
	}
	if err := rows.Err(); err != nil {
		return domain.DriftFindings{}, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}

func (r *ReconciliationRepository) MoneySupply(ctx context.Context) (domain.MoneySupply, error) {
	var ledgerTxt, cachedTxt string
	err := r.pool.QueryRow(ctx, `SELECT (SELECT COALESCE(SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE -amount END), 0)
          FROM ledger_db.ledger_entries)::text,
       (SELECT COALESCE(SUM(balance), 0) FROM ledger_db.account_balances_locked)::text`).Scan(&ledgerTxt, &cachedTxt)

	if err != nil {
		return domain.MoneySupply{}, fmt.Errorf("query money supply: %w", err)
	}
	ledgerTotal, err := decimal.NewFromString(ledgerTxt)
	if err != nil {
		return domain.MoneySupply{}, fmt.Errorf("parse ledger total: %w", err)
	}
	cachedTotal, err := decimal.NewFromString(cachedTxt)
	if err != nil {
		return domain.MoneySupply{}, fmt.Errorf("parse cached total: %w", err)
	}
	return domain.MoneySupply{LedgerTotal: ledgerTotal, CachedTotal: cachedTotal}, nil
}

// AcquirePassLock tries to take the cross-process "a reconciliation pass is running" lock.
//
//	acquired == true  -> the caller owns the lock and MUST call release exactly once.
//	acquired == false -> another pass (this process or another replica) holds it; release is nil.
//	err != nil        -> the lock state is unknown; nothing was taken; release is nil.
//
// The lock is session-level, so it is pinned to one dedicated connection until released.
func (r *ReconciliationRepository) AcquirePassLock(ctx context.Context) (release func(), acquired bool, err error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire connection: %w", err)
	}

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", reconciliationLockKey).Scan(&got); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}

	release = func() {
		// WithoutCancel: the unlock must still run after PassTimeout or an aborted request.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		var ok bool
		if err := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", reconciliationLockKey).Scan(&ok); err != nil || !ok {
			// Never return a still-locked connection to the pool: ending the session drops the lock.
			slog.Error("advisory unlock failed; closing the connection", slog.Any("error", err))
			_ = conn.Hijack().Close(context.Background())
			return
		}
		conn.Release()
	}
	return release, true, nil
}
