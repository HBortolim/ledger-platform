package tests

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestPostingRepository exercises PostingRepository.Post / GetTransactionByID
// against a real Postgres, connected as the restricted `ledger_app` role
// (matching cmd/server/main.go's production wiring). Subtests are scoped by
// their own fresh uuid.New() accounts/transactions, so they share one
// container/pool without needing TRUNCATE between them.
func TestPostingRepository(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool, testDailyCap, systemFundingAccountID)

	t.Run("BalancedTransaction/PersistsEntriesAndOutbox", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")

		tx := newBalancedTransfer(debit, credit, "100.00")
		if err := repo.Post(ctx, tx); err != nil {
			t.Fatalf("Post() = error %v, want nil", err)
		}

		var txCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = $1", tx.ID).Scan(&txCount); err != nil || txCount != 1 {
			t.Errorf("ledger_transactions count = %d, err=%v, want 1", txCount, err)
		}

		var entryCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_entries WHERE transaction_id = $1", tx.ID).Scan(&entryCount); err != nil || entryCount != len(tx.Entries) {
			t.Errorf("ledger_entries count = %d, err=%v, want %d", entryCount, err, len(tx.Entries))
		}

		var outboxCount int
		var payload []byte
		row := pool.QueryRow(ctx, "SELECT payload FROM ledger_db.outbox WHERE key = $1", tx.ID.String())
		if err := row.Scan(&payload); err != nil {
			t.Fatalf("outbox row for key %s: %v", tx.ID, err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&outboxCount); err != nil || outboxCount != 1 {
			t.Errorf("outbox count = %d, err=%v, want 1", outboxCount, err)
		}

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
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("could not unmarshal outbox payload: %v", err)
		}
		if envelope.SchemaVersion != 1 || envelope.EventType != "LEDGER_POSTED" || envelope.TransactionID != tx.ID.String() {
			t.Errorf("outbox payload envelope mismatch: %+v", envelope)
		}
		if len(envelope.Entries) != 2 {
			t.Errorf("outbox payload entries = %d, want 2", len(envelope.Entries))
		}

		var debitBalance, creditBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", debit).Scan(&debitBalance); err != nil {
			t.Fatalf("debit account balance query: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", credit).Scan(&creditBalance); err != nil {
			t.Fatalf("credit account balance query: %v", err)
		}
		if debitBalance != "100.00" {
			t.Errorf("debit account balance = %s, want 100.00 (200.00 seed - 100.00 debit)", debitBalance)
		}
		if creditBalance != "100.00" {
			t.Errorf("credit account balance = %s, want 100.00", creditBalance)
		}
	})

	t.Run("UnbalancedTransaction/TriggerAbortsCommit", func(t *testing.T) {
		debit := uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")

		money, err := domain.NewMoney("50.00")
		if err != nil {
			t.Fatalf("domain.NewMoney() = error %v, want nil", err)
		}
		txID := uuid.New()
		tx := domain.LedgerTransaction{
			ID:   txID,
			Type: domain.TransactionTransfer,
			Entries: []domain.LedgerEntry{
				// Deliberately no offsetting CREDIT entry — Post never calls
				// ValidateBalance itself, so this is a legal Go-level call.
				{ID: uuid.New(), TransactionID: txID, AccountID: debit, EntryType: domain.Debit, Amount: money},
			},
		}

		if err := repo.Post(ctx, tx); err == nil {
			t.Fatal("Post() = nil, want an error from the deferred balance trigger")
		}

		var txCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = $1", txID).Scan(&txCount); err != nil {
			t.Fatalf("count ledger_transactions: %v", err)
		}
		if txCount != 0 {
			t.Errorf("ledger_transactions count after failed unbalanced post = %d, want 0", txCount)
		}
		var entryCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_entries WHERE transaction_id = $1", txID).Scan(&entryCount); err != nil {
			t.Fatalf("count ledger_entries: %v", err)
		}
		if entryCount != 0 {
			t.Errorf("ledger_entries count after failed unbalanced post = %d, want 0", entryCount)
		}
		var outboxCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.outbox WHERE key = $1", txID.String()).Scan(&outboxCount); err != nil {
			t.Fatalf("count outbox: %v", err)
		}
		if outboxCount != 0 {
			t.Errorf("outbox count after failed unbalanced post = %d, want 0", outboxCount)
		}
	})

	t.Run("InsufficientFunds/ReturnsTypedError", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "50.00")

		tx := newBalancedTransfer(debit, credit, "100.00")
		err := repo.Post(ctx, tx)

		var insufficient domain.ErrInsufficientFunds
		if !errors.As(err, &insufficient) {
			t.Fatalf("Post() = %v, want ErrInsufficientFunds", err)
		}
		if insufficient.AccountID != debit {
			t.Errorf("ErrInsufficientFunds.AccountID = %s, want %s", insufficient.AccountID, debit)
		}

		var txCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = $1", tx.ID).Scan(&txCount); err != nil {
			t.Fatalf("count ledger_transactions: %v", err)
		}
		if txCount != 0 {
			t.Errorf("ledger_transactions count = %d, want 0", txCount)
		}
	})

	t.Run("DailyCapExceeded/ReturnsTypedError", func(t *testing.T) {
		// Own repo instance with a tight cap — the outer repo uses testDailyCap
		// (generously high) so this subtest's cap doesn't leak into the others.
		cappedRepo := repository.NewPostingRepository(pool, decimal.RequireFromString("100.00"), systemFundingAccountID)

		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "10000.00") // plenty of balance headroom; only the cap should trigger

		first := newBalancedTransfer(debit, credit, "60.00")
		if err := cappedRepo.Post(ctx, first); err != nil {
			t.Fatalf("first Post() = error %v, want nil", err)
		}

		second := newBalancedTransfer(debit, credit, "50.00") // 60 + 50 = 110 > 100 cap
		err := cappedRepo.Post(ctx, second)

		var capExceeded domain.ErrDailyCapExceeded
		if !errors.As(err, &capExceeded) {
			t.Fatalf("second Post() = %v, want ErrDailyCapExceeded", err)
		}
		if capExceeded.AccountID != debit {
			t.Errorf("ErrDailyCapExceeded.AccountID = %s, want %s", capExceeded.AccountID, debit)
		}

		var txCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = $1", second.ID).Scan(&txCount); err != nil {
			t.Fatalf("count ledger_transactions: %v", err)
		}
		if txCount != 0 {
			t.Errorf("ledger_transactions count for rejected posting = %d, want 0", txCount)
		}

		var debitBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", debit).Scan(&debitBalance); err != nil {
			t.Fatalf("query debit balance: %v", err)
		}
		if debitBalance != "9940.00" {
			t.Errorf("debit balance = %s, want 9940.00 (only the first, successful 60.00 debit applied)", debitBalance)
		}
	})

	t.Run("SystemAccountOverdraft/DepositEntersMoneyThroughAPI", func(t *testing.T) {
		wallet := uuid.New()
		// System account is seeded at 1000.00 by migration 0002; debit well past
		// that to prove the exemption, not just headroom within the seed value.
		deposit := newBalancedTransfer(systemFundingAccountID, wallet, "1500.00")
		if err := repo.Post(ctx, deposit); err != nil {
			t.Fatalf("deposit Post() = error %v, want nil (system account must be exempt from the overdraft check)", err)
		}

		var systemBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", systemFundingAccountID).Scan(&systemBalance); err != nil {
			t.Fatalf("query system account balance: %v", err)
		}
		if systemBalance != "-500.00" {
			t.Errorf("system account balance = %s, want -500.00 (1000.00 seed - 1500.00 debit, negative per NFR-CONS-5)", systemBalance)
		}

		var walletBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", wallet).Scan(&walletBalance); err != nil {
			t.Fatalf("query wallet balance: %v", err)
		}
		if walletBalance != "1500.00" {
			t.Errorf("wallet balance = %s, want 1500.00", walletBalance)
		}

		// The credited wallet can now fund a normal wallet-to-wallet transfer.
		destination := uuid.New()
		transfer := newBalancedTransfer(wallet, destination, "200.00")
		if err := repo.Post(ctx, transfer); err != nil {
			t.Fatalf("follow-up transfer Post() = error %v, want nil", err)
		}
	})

	t.Run("SystemAccountOverdraft/ExemptionIsExactlyOneAccountWide", func(t *testing.T) {
		zeroBalanceWallet, destination := uuid.New(), uuid.New()
		// zeroBalanceWallet is auto-provisioned at 0 on first touch -- not seeded.

		tx := newBalancedTransfer(zeroBalanceWallet, destination, "1.00")
		err := repo.Post(ctx, tx)

		var insufficient domain.ErrInsufficientFunds
		if !errors.As(err, &insufficient) {
			t.Fatalf("Post() = %v, want ErrInsufficientFunds (only the system account is exempt)", err)
		}
	})

	t.Run("DuplicateTransactionID/ReturnsTypedError", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")

		tx := newBalancedTransfer(debit, credit, "50.00")
		if err := repo.Post(ctx, tx); err != nil {
			t.Fatalf("first Post() = error %v, want nil", err)
		}

		dup := newBalancedTransfer(debit, credit, "50.00")
		dup.ID = tx.ID

		err := repo.Post(ctx, dup)
		var dupErr domain.ErrDuplicateTransaction
		if !errors.As(err, &dupErr) {
			t.Fatalf("second Post() = %v, want ErrDuplicateTransaction", err)
		}
		if dupErr.ID != tx.ID {
			t.Errorf("ErrDuplicateTransaction.ID = %s, want %s", dupErr.ID, tx.ID)
		}

		var entryCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_entries WHERE transaction_id = $1", tx.ID).Scan(&entryCount); err != nil {
			t.Fatalf("count ledger_entries: %v", err)
		}
		if entryCount != len(tx.Entries) {
			t.Errorf("ledger_entries count after duplicate attempt = %d, want %d (unchanged)", entryCount, len(tx.Entries))
		}
	})

	t.Run("GetTransactionByID/HappyPath", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")

		tx := newBalancedTransfer(debit, credit, "75.25")
		if err := repo.Post(ctx, tx); err != nil {
			t.Fatalf("Post() = error %v, want nil", err)
		}

		got, err := repo.GetTransactionByID(ctx, tx.ID)
		if err != nil {
			t.Fatalf("GetTransactionByID() = error %v, want nil", err)
		}
		if got.ID != tx.ID || got.Type != tx.Type {
			t.Errorf("GetTransactionByID() ID/Type = %s/%s, want %s/%s", got.ID, got.Type, tx.ID, tx.Type)
		}
		if len(got.Entries) != 2 {
			t.Fatalf("GetTransactionByID() entries = %d, want 2", len(got.Entries))
		}
		for i, e := range got.Entries {
			if e.Amount.String() != "75.25" {
				t.Errorf("entry[%d].Amount = %s, want 75.25 (Money round-trip through NUMERIC(19,2))", i, e.Amount.String())
			}
			if i > 0 && got.Entries[i-1].ID.String() > e.ID.String() {
				t.Errorf("entries not ordered by id: entry[%d]=%s > entry[%d]=%s", i-1, got.Entries[i-1].ID, i, e.ID)
			}
		}
	})

	t.Run("GetTransactionByID/UnknownID", func(t *testing.T) {
		_, err := repo.GetTransactionByID(ctx, uuid.New())
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetTransactionByID() = %v, want ErrNotFound", err)
		}
	})

	t.Run("ReversalTransaction/PersistsAndRoundTrips", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")

		original := newBalancedTransfer(debit, credit, "40.00")
		if err := repo.Post(ctx, original); err != nil {
			t.Fatalf("original Post() = error %v, want nil", err)
		}

		reversal := newBalancedTransfer(credit, debit, "40.00")
		reversal.Type = domain.TransactionReversal
		reversal.ReversesTransactionID = &original.ID

		if err := repo.Post(ctx, reversal); err != nil {
			t.Fatalf("reversal Post() = error %v, want nil", err)
		}

		got, err := repo.GetTransactionByID(ctx, reversal.ID)
		if err != nil {
			t.Fatalf("GetTransactionByID(reversal) = error %v, want nil", err)
		}
		if got.ReversesTransactionID == nil || *got.ReversesTransactionID != original.ID {
			t.Errorf("GetTransactionByID(reversal).ReversesTransactionID = %v, want %s", got.ReversesTransactionID, original.ID)
		}
	})

	t.Run("NewAccount/AutoProvisionedViaUpsert", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")
		// credit is never seeded/pre-provisioned — it must be auto-created.

		tx := newBalancedTransfer(debit, credit, "30.00")
		if err := repo.Post(ctx, tx); err != nil {
			t.Fatalf("Post() = error %v, want nil", err)
		}

		var creditBalance string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", credit).Scan(&creditBalance); err != nil {
			t.Fatalf("expected account_balances_locked row to be auto-provisioned for %s: %v", credit, err)
		}
		if creditBalance != "30.00" {
			t.Errorf("credit balance = %s, want 30.00", creditBalance)
		}
	})

	t.Run("MidFlowFailure/RollsBackFully", func(t *testing.T) {
		accountA, accountB, accountD := uuid.New(), uuid.New(), uuid.New()
		seedAccountBalance(t, pool, accountA, "10.00")
		seedAccountBalance(t, pool, accountB, "200.00")

		amountA, _ := domain.NewMoney("50.00")
		amountB, _ := domain.NewMoney("30.00")
		amountD, _ := domain.NewMoney("80.00")
		txID := uuid.New()
		tx := domain.LedgerTransaction{
			ID:   txID,
			Type: domain.TransactionTransfer,
			Entries: []domain.LedgerEntry{
				{ID: uuid.New(), TransactionID: txID, AccountID: accountA, EntryType: domain.Debit, Amount: amountA},  // insufficient (balance 10.00)
				{ID: uuid.New(), TransactionID: txID, AccountID: accountB, EntryType: domain.Debit, Amount: amountB},  // individually fine (balance 200.00)
				{ID: uuid.New(), TransactionID: txID, AccountID: accountD, EntryType: domain.Credit, Amount: amountD}, // balances the transaction (50+30=80)
			},
		}

		err := repo.Post(ctx, tx)
		var insufficient domain.ErrInsufficientFunds
		if !errors.As(err, &insufficient) {
			t.Fatalf("Post() = %v, want ErrInsufficientFunds", err)
		}
		if insufficient.AccountID != accountA {
			t.Errorf("ErrInsufficientFunds.AccountID = %s, want %s (the actually-insufficient account)", insufficient.AccountID, accountA)
		}

		var txCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = $1", txID).Scan(&txCount); err != nil {
			t.Fatalf("count ledger_transactions: %v", err)
		}
		if txCount != 0 {
			t.Errorf("ledger_transactions count = %d, want 0", txCount)
		}

		var balanceB string
		if err := pool.QueryRow(ctx, "SELECT balance::text FROM ledger_db.account_balances_locked WHERE account_id = $1", accountB).Scan(&balanceB); err != nil {
			t.Fatalf("query account B balance: %v", err)
		}
		if balanceB != "200.00" {
			t.Errorf("account B balance = %s, want unchanged 200.00 (mid-flow failure must roll back every account, not just the failing one)", balanceB)
		}
	})
}
