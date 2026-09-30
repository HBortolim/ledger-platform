package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ledger-platform/projection-service/internal/metrics"
	"github.com/ledger-platform/projection-service/internal/utils"
)

type RebuildRepository interface {
	Rebuild(ctx context.Context, walletID uuid.UUID) (utils.Result, error)
}

type RebuildService struct {
	repo RebuildRepository
}

func NewRebuildService(repo RebuildRepository) *RebuildService {
	return &RebuildService{repo: repo}
}

// Rebuild delegates to the repository and makes the outcome observable. That is all this
// layer adds: no policy, no auth, no audit -- those belong to the Wallet Service (ADR-0016).
func (s *RebuildService) Rebuild(ctx context.Context, walletID uuid.UUID) (utils.Result, error) {
	result, err := s.repo.Rebuild(ctx, walletID)
	if err != nil {
		metrics.RebuildsTotal.WithLabelValues("error").Inc()
		// The handler deliberately returns a generic 503 (no internals in the body), so this
		// line is the only place the cause of a failed rebuild is ever recorded.
		slog.ErrorContext(ctx, "projection rebuild failed",
			slog.String("wallet_id", walletID.String()),
			slog.Any("error", err))
		return utils.Result{}, err
	}

	metrics.RebuildsTotal.WithLabelValues("ok").Inc()
	// Mechanism-side counterpart of the Wallet Service's audit row.
	slog.InfoContext(ctx, "projection rebuilt from ledger",
		slog.String("wallet_id", result.WalletID.String()),
		slog.Int("entries_applied", result.EntriesApplied),
		slog.String("balance", result.Balance.StringFixed(2)))
	return result, nil
}