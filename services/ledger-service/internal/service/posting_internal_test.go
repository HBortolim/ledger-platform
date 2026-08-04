// services/ledger-service/internal/service/posting_internal_test.go
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

type fakeRepo struct {
	postCalled  bool
	postFunc    func(ctx context.Context, tx domain.LedgerTransaction) error
	getByIDFunc func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error)
}

func (f *fakeRepo) Post(ctx context.Context, tx domain.LedgerTransaction) error {
	f.postCalled = true
	return f.postFunc(ctx, tx)
}

func (f *fakeRepo) GetTransactionByID(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
	return f.getByIDFunc(ctx, id)
}

func validEntries(debit, credit uuid.UUID) []PostEntryInput {
	return []PostEntryInput{
		{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
		{AccountID: credit, EntryType: "CREDIT", Amount: "100.00"},
	}
}

func TestPost_Success_ReturnsRepersistedTransactionFromRepo(t *testing.T) {
	txID := uuid.New()
	debit, credit := uuid.New(), uuid.New()
	persistedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	repo := &fakeRepo{
		postFunc: func(ctx context.Context, tx domain.LedgerTransaction) error {
			if tx.ID != txID {
				t.Errorf("Post() called with tx.ID = %s, want %s", tx.ID, txID)
			}
			return nil
		},
		getByIDFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{ID: id, Type: domain.TransactionTransfer, CreatedAt: persistedAt}, nil
		},
	}
	svc := NewPostingService(repo)

	got, err := svc.Post(context.Background(), PostInput{
		TransactionID: txID,
		Type:          "TRANSFER",
		Entries:       validEntries(debit, credit),
	})
	if err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}
	if !got.CreatedAt.Equal(persistedAt) {
		t.Errorf("Post() CreatedAt = %v, want %v (the repo's re-read value, not a local clock)", got.CreatedAt, persistedAt)
	}
}

func TestPost_UnbalancedEntries_ReturnsErrUnbalanced_WithoutCallingRepo(t *testing.T) {
	debit, credit := uuid.New(), uuid.New()
	repo := &fakeRepo{
		postFunc:    func(ctx context.Context, tx domain.LedgerTransaction) error { return nil },
		getByIDFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) { return domain.LedgerTransaction{}, nil },
	}
	svc := NewPostingService(repo)

	_, err := svc.Post(context.Background(), PostInput{
		TransactionID: uuid.New(),
		Type:          "TRANSFER",
		Entries: []PostEntryInput{
			{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
			{AccountID: credit, EntryType: "CREDIT", Amount: "50.00"},
		},
	})

	var unbalanced ErrUnbalanced
	if !errors.As(err, &unbalanced) {
		t.Fatalf("Post() = %v, want ErrUnbalanced", err)
	}
	if repo.postCalled {
		t.Error("Post() called repo.Post() for an unbalanced posting, want the app-side pre-check to short-circuit")
	}
}

func TestPost_InvalidAmount_ReturnsErrInvalidAmount_WithoutCallingRepo(t *testing.T) {
	debit, credit := uuid.New(), uuid.New()
	repo := &fakeRepo{
		postFunc:    func(ctx context.Context, tx domain.LedgerTransaction) error { return nil },
		getByIDFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) { return domain.LedgerTransaction{}, nil },
	}
	svc := NewPostingService(repo)

	_, err := svc.Post(context.Background(), PostInput{
		TransactionID: uuid.New(),
		Type:          "TRANSFER",
		Entries: []PostEntryInput{
			{AccountID: debit, EntryType: "DEBIT", Amount: "100"},
			{AccountID: credit, EntryType: "CREDIT", Amount: "100"},
		},
	})

	var invalidAmount ErrInvalidAmount
	if !errors.As(err, &invalidAmount) {
		t.Fatalf("Post() = %v, want ErrInvalidAmount", err)
	}
	if repo.postCalled {
		t.Error("Post() called repo.Post() for an invalid amount, want NewMoney to short-circuit first")
	}
}

func TestPost_DuplicateTransaction_PassesThroughTypedError(t *testing.T) {
	debit, credit := uuid.New(), uuid.New()
	txID := uuid.New()
	repo := &fakeRepo{
		postFunc: func(ctx context.Context, tx domain.LedgerTransaction) error {
			return domain.ErrDuplicateTransaction{ID: txID}
		},
	}
	svc := NewPostingService(repo)

	_, err := svc.Post(context.Background(), PostInput{
		TransactionID: txID,
		Type:          "TRANSFER",
		Entries:       validEntries(debit, credit),
	})

	var dup domain.ErrDuplicateTransaction
	if !errors.As(err, &dup) {
		t.Fatalf("Post() = %v, want domain.ErrDuplicateTransaction", err)
	}
	if dup.ID != txID {
		t.Errorf("ErrDuplicateTransaction.ID = %s, want %s", dup.ID, txID)
	}
}

func TestPost_InsufficientFunds_PassesThroughTypedError(t *testing.T) {
	debit, credit := uuid.New(), uuid.New()
	repo := &fakeRepo{
		postFunc: func(ctx context.Context, tx domain.LedgerTransaction) error {
			return domain.ErrInsufficientFunds{AccountID: debit}
		},
	}
	svc := NewPostingService(repo)

	_, err := svc.Post(context.Background(), PostInput{
		TransactionID: uuid.New(),
		Type:          "TRANSFER",
		Entries:       validEntries(debit, credit),
	})

	var insufficient domain.ErrInsufficientFunds
	if !errors.As(err, &insufficient) {
		t.Fatalf("Post() = %v, want domain.ErrInsufficientFunds", err)
	}
	if insufficient.AccountID != debit {
		t.Errorf("ErrInsufficientFunds.AccountID = %s, want %s", insufficient.AccountID, debit)
	}
}

func TestPost_DailyCapExceeded_PassesThroughTypedError(t *testing.T) {
	debit, credit := uuid.New(), uuid.New()
	limit := decimal.RequireFromString("100.00")
	attempted := decimal.RequireFromString("150.00")
	repo := &fakeRepo{
		postFunc: func(ctx context.Context, tx domain.LedgerTransaction) error {
			return domain.ErrDailyCapExceeded{AccountID: debit, Limit: limit, Attempted: attempted}
		},
	}
	svc := NewPostingService(repo)

	_, err := svc.Post(context.Background(), PostInput{
		TransactionID: uuid.New(),
		Type:          "TRANSFER",
		Entries:       validEntries(debit, credit),
	})

	var capExceeded domain.ErrDailyCapExceeded
	if !errors.As(err, &capExceeded) {
		t.Fatalf("Post() = %v, want domain.ErrDailyCapExceeded", err)
	}
	if capExceeded.AccountID != debit {
		t.Errorf("ErrDailyCapExceeded.AccountID = %s, want %s", capExceeded.AccountID, debit)
	}
}

func TestPost_OpaqueRepoError_PassesThroughUnclassified(t *testing.T) {
	debit, credit := uuid.New(), uuid.New()
	connErr := errors.New("connection refused")
	repo := &fakeRepo{
		postFunc: func(ctx context.Context, tx domain.LedgerTransaction) error {
			return connErr
		},
	}
	svc := NewPostingService(repo)

	_, err := svc.Post(context.Background(), PostInput{
		TransactionID: uuid.New(),
		Type:          "TRANSFER",
		Entries:       validEntries(debit, credit),
	})

	if !errors.Is(err, connErr) {
		t.Fatalf("Post() = %v, want the opaque repo error unwrapped, unclassified (this is what the handler maps to 503)", err)
	}
}

func TestGetTransaction_DelegatesToRepo(t *testing.T) {
	id := uuid.New()
	want := domain.LedgerTransaction{ID: id, Type: domain.TransactionDeposit}
	repo := &fakeRepo{
		getByIDFunc: func(ctx context.Context, gotID uuid.UUID) (domain.LedgerTransaction, error) {
			if gotID != id {
				t.Errorf("GetTransactionByID called with %s, want %s", gotID, id)
			}
			return want, nil
		},
	}
	svc := NewPostingService(repo)

	got, err := svc.GetTransaction(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTransaction() = error %v, want nil", err)
	}
	if got.ID != want.ID || got.Type != want.Type {
		t.Errorf("GetTransaction() = %+v, want %+v", got, want)
	}
}
