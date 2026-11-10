package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

type invariantFindingResponse struct {
	TransactionID string `json:"transactionId"`
	Net           string `json:"net"`
	EntryCount    int    `json:"entryCount"`
}

type invariantResponse struct {
	Clean        bool                       `json:"clean"`
	FindingCount int                        `json:"findingCount"`
	Truncated    bool                       `json:"truncated"`
	Findings     []invariantFindingResponse `json:"findings"`
}

type driftFindingResponse struct {
	WalletID  string `json:"walletId"`
	Kind      string `json:"kind"`
	Projected string `json:"projected"`
	Actual    string `json:"actual"`
}

type projectionResponse struct {
	Clean        bool                   `json:"clean"`
	FindingCount int                    `json:"findingCount"`
	Truncated    bool                   `json:"truncated"`
	Findings     []driftFindingResponse `json:"findings"`
}

type moneySupplyResponse struct {
	Clean       bool   `json:"clean"`
	LedgerClean bool   `json:"ledgerClean"`
	CachedClean bool   `json:"cachedClean"`
	LedgerTotal string `json:"ledgerTotal"`
	CachedTotal string `json:"cachedTotal"`
}

type reconciliationResponse struct {
	RanAt       time.Time           `json:"ranAt"`
	DurationMs  int64               `json:"durationMs"`
	Clean       bool                `json:"clean"`
	Invariant   invariantResponse   `json:"invariant"`
	Projection  projectionResponse  `json:"projection"`
	MoneySupply moneySupplyResponse `json:"moneySupply"`
}

// renderReconciliation derives every "clean" flag from the domain methods; none is stored.
func renderReconciliation(r domain.ReconciliationReport) reconciliationResponse {
	inv := make([]invariantFindingResponse, 0, len(r.Invariant.Sample))
	for _, f := range r.Invariant.Sample {
		inv = append(inv, invariantFindingResponse{
			TransactionID: f.TransactionID.String(),
			Net:           f.Net.StringFixed(2),
			EntryCount:    f.EntryCount,
		})
	}
	drift := make([]driftFindingResponse, 0, len(r.Projection.Sample))
	for _, f := range r.Projection.Sample {
		drift = append(drift, driftFindingResponse{
			WalletID:  f.WalletID.String(),
			Kind:      string(f.Kind),
			Projected: f.Projected.StringFixed(2),
			Actual:    f.Actual.StringFixed(2),
		})
	}
	return reconciliationResponse{
		RanAt:      r.RanAt,
		DurationMs: r.Duration.Milliseconds(),
		Clean:      r.Clean(),
		Invariant: invariantResponse{
			Clean: r.Invariant.Clean(), FindingCount: r.Invariant.Total,
			Truncated: r.Invariant.Truncated(), Findings: inv,
		},
		Projection: projectionResponse{
			Clean: r.Projection.Clean(), FindingCount: r.Projection.Total,
			Truncated: r.Projection.Truncated(), Findings: drift,
		},
		MoneySupply: moneySupplyResponse{
			Clean:       r.MoneySupply.Clean(),
			LedgerClean: r.MoneySupply.LedgerClean(),
			CachedClean: r.MoneySupply.CachedClean(),
			LedgerTotal: r.MoneySupply.LedgerTotal.StringFixed(2),
			CachedTotal: r.MoneySupply.CachedTotal.StringFixed(2),
		},
	}
}

type reconciliationService interface {
	Run(ctx context.Context) (domain.ReconciliationReport, error)
}

type ReconciliationHandler struct {
	svc reconciliationService
}

func NewReconciliationHandler(svc reconciliationService) *ReconciliationHandler {
	return &ReconciliationHandler{svc: svc}
}

// Run triggers one pass. A pass that finds drift still did its job: 200 with "clean": false.
func (h *ReconciliationHandler) Run(c *gin.Context) {
	report, err := h.svc.Run(c.Request.Context())
	if err != nil {
		var inProgress domain.ErrReconciliationInProgress
		if errors.As(err, &inProgress) {
			c.JSON(http.StatusConflict, errorResponse{Code: "RECONCILIATION_IN_PROGRESS", Message: inProgress.Error()})
			return
		}
		renderDBUnavailable(c)
		return
	}
	c.JSON(http.StatusOK, renderReconciliation(report))
}
