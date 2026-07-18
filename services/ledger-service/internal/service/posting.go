package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/metrics"
)

type PostingRepository interface {
	Post(ctx context.Context, tx domain.LedgerTransaction) error
	GetTransactionByID(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error)
}

type PostingService struct {
	repo PostingRepository
}

func NewPostingService(repo PostingRepository) *PostingService {
	return &PostingService{repo: repo}
}

// PostEntryInput is one entry of a PostInput, free of gin/JSON types.
type PostEntryInput struct {
	AccountID uuid.UUID
	EntryType string
	Amount    string
}

// PostInput mirrors the validated HTTP request the handler builds.
type PostInput struct {
	TransactionID uuid.UUID
	Type          string
	Description   string
	Entries       []PostEntryInput
}

// Post parses entry amounts, pre-checks the double-entry invariant
// (SPEC.md §3.2 — application-side defense in depth ahead of the DB
// trigger), and persists via the repository. On success it re-reads the
// persisted transaction so the returned CreatedAt is Postgres's now(), not
// this service's clock (SPEC.md §9.10).
func (s *PostingService) Post(ctx context.Context, in PostInput) (domain.LedgerTransaction, error) {
	entries := make([]domain.LedgerEntry, len(in.Entries))
	for i, e := range in.Entries {
		amount, err := domain.NewMoney(e.Amount)
		if err != nil {
			return domain.LedgerTransaction{}, ErrInvalidAmount{Reason: err.Error()}
		}
		entries[i] = domain.LedgerEntry{
			ID:            uuid.New(),
			TransactionID: in.TransactionID,
			AccountID:     e.AccountID,
			EntryType:     domain.EntryType(e.EntryType),
			Amount:        amount,
		}
	}

	tx := domain.LedgerTransaction{
		ID:          in.TransactionID,
		Type:        domain.TransactionType(in.Type),
		Description: in.Description,
		Entries:     entries,
	}
	if err := tx.ValidateBalance(); err != nil {
		return domain.LedgerTransaction{}, ErrUnbalanced{Message: err.Error()}
	}

	status := "error"
	start := time.Now()
	postErr := s.repo.Post(ctx, tx)
	metrics.PostingDuration.WithLabelValues(string(tx.Type)).Observe(time.Since(start).Seconds())
	defer func() {
		metrics.PostingsTotal.WithLabelValues(string(tx.Type), status).Inc()
	}()

	var dupErr          domain.ErrDuplicateTransaction
	var insufficientErr domain.ErrInsufficientFunds

	switch {
	case postErr == nil:
		status = "posted"
		persisted, err := s.repo.GetTransactionByID(ctx, tx.ID)
		if err != nil {
			status = "error"
			return domain.LedgerTransaction{}, err
		}
		return persisted, nil
	case errors.As(postErr, &dupErr):
		status = "duplicate"
		return domain.LedgerTransaction{}, dupErr
	case errors.As(postErr, &insufficientErr):
		status = "rejected"
		return domain.LedgerTransaction{}, insufficientErr
	default:
		return domain.LedgerTransaction{}, postErr
	}
}

func (s *PostingService) GetTransaction(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
	return s.repo.GetTransactionByID(ctx, id)
}
