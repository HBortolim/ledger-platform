package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/shopspring/decimal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const pgErrUniqueViolation = "23505"

type PostingRepository struct {
	pool            *pgxpool.Pool
	dailyCap        decimal.Decimal
	systemAccountID uuid.UUID
}

func NewPostingRepository(pool *pgxpool.Pool, dailyCap decimal.Decimal, systemAccountID uuid.UUID) *PostingRepository {
	return &PostingRepository{pool: pool, dailyCap: dailyCap, systemAccountID: systemAccountID}
}

// Post atomically writes a LedgerTransaction: entries, locked-balance update, and outbox row.
func (r *PostingRepository) Post(ctx context.Context, tx domain.LedgerTransaction) error {
	dbtx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	// Step 1: Upsert lock rows for every distinct account referenced in the entries.
	if err := upsertAccountLocks(ctx, dbtx, allAccountIDs(tx.Entries)); err != nil {
		return err
	}

	// Step 2: Acquire FOR UPDATE on every account referenced — DEBIT *and* CREDIT — sorted
	lockedBalances, err := lockAccounts(ctx, dbtx, sortedAccountIDs(tx.Entries))
	if err != nil {
		return err
	}

	// Step 3: Available-balance check — cached balance minus total debit must be non-negative.
	if err := r.checkAvailableBalance(tx.Entries, lockedBalances); err != nil {
		return err
	}

	// Step 3.5: Daily per-account transfer cap, same locked transaction as the balance check
	// above — closes the TOCTOU race an unlocked pre-check outside this transaction would have.
	if err := checkDailyTransferCap(ctx, dbtx, tx.Entries, r.dailyCap); err != nil {
		return err
	}

	// Step 4: Insert the transaction header.
	if err := insertLedgerTransaction(ctx, dbtx, tx); err != nil {
		return err
	}

	// Step 5: Batch-insert ledger entries (NFR-SEC-6: parameterized only).
	if err := insertLedgerEntries(ctx, dbtx, tx.Entries); err != nil {
		return err
	}

	// Step 6: Apply signed deltas to cached locked balances.
	if err := applyBalanceDeltas(ctx, dbtx, tx.Entries); err != nil {
		return err
	}

	if err := insertOutboxRow(ctx, dbtx, tx); err != nil {
		return err
	}

	return dbtx.Commit(ctx)
}

// GetTransactionByID retrieves a transaction and its entries. Returns ErrNotFound when absent.
func (r *PostingRepository) GetTransactionByID(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
	var (
		txType     string
		reversesID pgtype.UUID
		desc       pgtype.Text
		createdAt  time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT type, reverses_transaction_id, description, created_at
		   FROM ledger_db.ledger_transactions
		  WHERE id = $1`,
		id,
	).Scan(&txType, &reversesID, &desc, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.LedgerTransaction{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.LedgerTransaction{}, fmt.Errorf("query transaction: %w", err)
	}

	result := domain.LedgerTransaction{
		ID:        id,
		Type:      domain.TransactionType(txType),
		CreatedAt: createdAt,
	}
	if desc.Valid {
		result.Description = desc.String
	}
	if reversesID.Valid {
		uid := uuid.UUID(reversesID.Bytes)
		result.ReversesTransactionID = &uid
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, account_id, entry_type, amount::text, created_at
		   FROM ledger_db.ledger_entries
		  WHERE transaction_id = $1
		  ORDER BY id`,
		id,
	)
	if err != nil {
		return domain.LedgerTransaction{}, fmt.Errorf("query entries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			entryID   uuid.UUID
			accountID uuid.UUID
			entryType string
			amountStr string
			entCreAt  time.Time
		)
		if err := rows.Scan(&entryID, &accountID, &entryType, &amountStr, &entCreAt); err != nil {
			return domain.LedgerTransaction{}, fmt.Errorf("scan entry row: %w", err)
		}
		money, err := domain.NewMoney(amountStr)
		if err != nil {
			return domain.LedgerTransaction{}, fmt.Errorf("parse entry amount %q: %w", amountStr, err)
		}
		result.Entries = append(result.Entries, domain.LedgerEntry{
			ID:            entryID,
			TransactionID: id,
			AccountID:     accountID,
			EntryType:     domain.EntryType(entryType),
			Amount:        money,
			CreatedAt:     entCreAt,
		})
	}
	if err := rows.Err(); err != nil {
		return domain.LedgerTransaction{}, fmt.Errorf("iterate entries: %w", err)
	}

	return result, nil
}

// --- helpers ---

func allAccountIDs(entries []domain.LedgerEntry) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(entries))
	ids := make([]uuid.UUID, 0, len(entries))
	for _, e := range entries {
		if _, ok := seen[e.AccountID]; !ok {
			seen[e.AccountID] = struct{}{}
			ids = append(ids, e.AccountID)
		}
	}
	return ids
}

// sortedAccountIDs dedupes every distinct account referenced across all
// entries — DEBIT *and* CREDIT — and sorts them for a globally consistent
func sortedAccountIDs(entries []domain.LedgerEntry) []uuid.UUID {
	ids := allAccountIDs(entries)
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})
	return ids
}

func upsertAccountLocks(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	batch := &pgx.Batch{} // pipelines multiple INSERTs to avoid N round trips for N accounts
	for _, id := range ids {
		batch.Queue(
			`INSERT INTO ledger_db.account_balances_locked(account_id, balance)
			 VALUES ($1, 0)
			 ON CONFLICT (account_id) DO NOTHING`,
			id,
		)
	}
	br := tx.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range ids {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert account lock: %w", err)
		}
	}
	return br.Close()
}

// lockAccounts issues SELECT … FOR UPDATE on each given account in sorted order.
// Sorting prevents deadlocks when concurrent transactions target overlapping account sets.
func lockAccounts(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	balances := make(map[uuid.UUID]decimal.Decimal, len(ids))
	for _, id := range ids {
		var balStr string
		err := tx.QueryRow(ctx,
			`SELECT balance::text
			   FROM ledger_db.account_balances_locked
			  WHERE account_id = $1
			    FOR UPDATE`, // balance cast to text to avoid pgx decimal parsing issues
			id,
		).Scan(&balStr)
		if err != nil {
			return nil, fmt.Errorf("lock account %s: %w", id, err)
		}
		d, err := decimal.NewFromString(balStr)
		if err != nil {
			return nil, fmt.Errorf("parse locked balance for %s: %w", id, err)
		}
		balances[id] = d
	}
	return balances, nil
}

// checkAvailableBalance rejects any debit that would overdraw its account,
// except the configured system funding account: per NFR-CONS-5, its balance
// is definitionally the negative mirror of all money in circulation, so it
// must be allowed to go negative (ADR-0008). The exemption matches on this
// one configured UUID only — never on "account absent from
// account_balances_locked", since every account auto-provisions at 0 on
// first touch and every wallet must keep failing overdrafts.
func (r *PostingRepository) checkAvailableBalance(entries []domain.LedgerEntry, balances map[uuid.UUID]decimal.Decimal) error {
	totalDebit := make(map[uuid.UUID]decimal.Decimal)
	for _, e := range entries {
		if e.EntryType == domain.Debit {
			totalDebit[e.AccountID] = totalDebit[e.AccountID].Add(e.Amount.Decimal())
		}
	}
	for accountID, debitAmt := range totalDebit {
		if accountID == r.systemAccountID {
			continue
		}
		if balances[accountID].LessThan(debitAmt) {
			return domain.ErrInsufficientFunds{AccountID: accountID}
		}
	}
	return nil
}

// checkDailyTransferCap sums each DEBIT account's committed debits for the current day plus
// this posting's own debit amount, and rejects if that total exceeds dailyCap. Runs inside the
// same locked transaction as checkAvailableBalance (called after lockAccounts), so a concurrent
// posting against the same account can't slip past both checks with a stale read.
func checkDailyTransferCap(ctx context.Context, tx pgx.Tx, entries []domain.LedgerEntry, dailyCap decimal.Decimal) error {
	debitTotals := make(map[uuid.UUID]decimal.Decimal)
	for _, e := range entries {
		if e.EntryType == domain.Debit {
			debitTotals[e.AccountID] = debitTotals[e.AccountID].Add(e.Amount.Decimal())
		}
	}
	for accountID, amount := range debitTotals {
		var sumStr string
		err := tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount),0)::text
			   FROM ledger_db.ledger_entries
			  WHERE account_id = $1
			    AND entry_type = 'DEBIT'
			    AND created_at >= date_trunc('day', now())`,
			accountID,
		).Scan(&sumStr)
		if err != nil {
			return fmt.Errorf("sum today's debits for %s: %w", accountID, err)
		}
		todaysDebits, err := decimal.NewFromString(sumStr)
		if err != nil {
			return fmt.Errorf("parse today's debits for %s: %w", accountID, err)
		}
		attempted := todaysDebits.Add(amount)
		if attempted.GreaterThan(dailyCap) {
			return domain.ErrDailyCapExceeded{AccountID: accountID, Limit: dailyCap, Attempted: attempted}
		}
	}
	return nil
}

func insertLedgerTransaction(ctx context.Context, tx pgx.Tx, t domain.LedgerTransaction) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO ledger_db.ledger_transactions(id, type, reverses_transaction_id, description)
		 VALUES ($1, $2, $3, $4)`,
		t.ID, string(t.Type), t.ReversesTransactionID, nilIfEmpty(t.Description),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgErrUniqueViolation {
			return domain.ErrDuplicateTransaction{ID: t.ID}
		}
		return fmt.Errorf("insert ledger_transaction: %w", err)
	}
	return nil
}

func insertLedgerEntries(ctx context.Context, tx pgx.Tx, entries []domain.LedgerEntry) error {
	batch := &pgx.Batch{}
	for _, e := range entries {
		batch.Queue(
			`INSERT INTO ledger_db.ledger_entries(id, transaction_id, account_id, entry_type, amount)
			 VALUES ($1, $2, $3, $4, $5::numeric)`,
			e.ID, e.TransactionID, e.AccountID, string(e.EntryType), e.Amount.String(),
		)
	}
	br := tx.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range entries {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert ledger_entry: %w", err)
		}
	}
	return br.Close()
}

func applyBalanceDeltas(ctx context.Context, tx pgx.Tx, entries []domain.LedgerEntry) error {
	deltas := make(map[uuid.UUID]decimal.Decimal, len(entries))
	for _, e := range entries {
		if e.EntryType == domain.Credit {
			deltas[e.AccountID] = deltas[e.AccountID].Add(e.Amount.Decimal())
		} else {
			deltas[e.AccountID] = deltas[e.AccountID].Sub(e.Amount.Decimal())
		}
	}
	batch := &pgx.Batch{}
	for accountID, delta := range deltas {
		batch.Queue(
			`UPDATE ledger_db.account_balances_locked
			    SET balance = balance + $1::numeric, updated_at = now()
			  WHERE account_id = $2`,
			delta.StringFixed(2), accountID,
		)
	}
	br := tx.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range deltas {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("update locked balance: %w", err)
		}
	}
	return br.Close()
}

type ledgerPostedPayload struct {
	SchemaVersion   int            `json:"schemaVersion"`
	EventID         uuid.UUID      `json:"eventId"`
	EventType       string         `json:"eventType"`
	OccurredAt      time.Time      `json:"occurredAt"`
	TransactionID   uuid.UUID      `json:"transactionId"`
	TransactionType string         `json:"transactionType"`
	Entries         []entryPayload `json:"entries"`
}

type entryPayload struct {
	EntryID   uuid.UUID `json:"entryId"`
	AccountID uuid.UUID `json:"accountId"`
	EntryType string    `json:"entryType"`
	Amount    string    `json:"amount"`
}

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

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
