package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
)

type postingEntryRequest struct {
	AccountID uuid.UUID `json:"accountId" binding:"required"`
	EntryType string    `json:"entryType" binding:"required,oneof=DEBIT CREDIT"`
	Amount    string    `json:"amount" binding:"required"`
}

type postingRequest struct {
	TransactionID uuid.UUID             `json:"transactionId" binding:"required"`
	Type          string                `json:"type" binding:"required,oneof=TRANSFER DEPOSIT WITHDRAWAL REVERSAL"`
	Description   string                `json:"description"`
	Entries       []postingEntryRequest `json:"entries" binding:"required,min=2"`
}

type entryResponse struct {
	EntryID   string `json:"entryId"`
	AccountID string `json:"accountId"`
	EntryType string `json:"entryType"`
	Amount    string `json:"amount"`
}

type postingResponse struct {
	TransactionID string          `json:"transactionId"`
	Type          string          `json:"type"`
	Description   string          `json:"description,omitempty"`
	PostedAt      time.Time       `json:"postedAt"`
	Entries       []entryResponse `json:"entries"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func renderPosting(tx domain.LedgerTransaction) postingResponse {
	entries := make([]entryResponse, len(tx.Entries))
	for i, e := range tx.Entries {
		entries[i] = entryResponse{
			EntryID:   e.ID.String(),
			AccountID: e.AccountID.String(),
			EntryType: string(e.EntryType),
			Amount:    e.Amount.String(),
		}
	}
	return postingResponse{
		TransactionID: tx.ID.String(),
		Type:          string(tx.Type),
		Description:   tx.Description,
		PostedAt:      tx.CreatedAt,
		Entries:       entries,
	}
}

// postingService is the subset of *service.PostingService this handler
// needs. Declared here, the consumer, so tests can supply a fake without
// touching the real service or repository.
type postingService interface {
	Post(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error)
	GetTransaction(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error)
}

type PostingHandler struct {
	svc postingService
}

func NewPostingHandler(svc postingService) *PostingHandler {
	return &PostingHandler{svc: svc}
}

func (h *PostingHandler) PostPosting(c *gin.Context) {
	var req postingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, errorResponse{Code: "VALIDATION_ERROR", Message: err.Error()})
		return
	}

	entries := make([]service.PostEntryInput, len(req.Entries))
	for i, e := range req.Entries {
		entries[i] = service.PostEntryInput{
			AccountID: e.AccountID,
			EntryType: e.EntryType,
			Amount:    e.Amount,
		}
	}

	tx, err := h.svc.Post(c.Request.Context(), service.PostInput{
		TransactionID: req.TransactionID,
		Type:          req.Type,
		Description:   req.Description,
		Entries:       entries,
	})
	if err != nil {
		h.renderPostError(c, err)
		return
	}

	c.JSON(http.StatusCreated, renderPosting(tx))
}

func (h *PostingHandler) renderPostError(c *gin.Context, err error) {
	var dupErr repository.ErrDuplicateTransaction
	var insufficientErr repository.ErrInsufficientFunds
	var unbalancedErr service.ErrUnbalanced
	var invalidAmountErr service.ErrInvalidAmount

	switch {
	case errors.As(err, &dupErr):
		original, fetchErr := h.svc.GetTransaction(c.Request.Context(), dupErr.ID)
		if fetchErr != nil {
			h.renderDBUnavailable(c)
			return
		}
		c.JSON(http.StatusConflict, renderPosting(original))
	case errors.As(err, &unbalancedErr):
		c.JSON(http.StatusUnprocessableEntity, errorResponse{Code: "UNBALANCED", Message: unbalancedErr.Error()})
	case errors.As(err, &invalidAmountErr):
		c.JSON(http.StatusUnprocessableEntity, errorResponse{Code: "INVALID_AMOUNT", Message: invalidAmountErr.Error()})
	case errors.As(err, &insufficientErr):
		c.JSON(http.StatusUnprocessableEntity, errorResponse{Code: "INSUFFICIENT_FUNDS", Message: insufficientErr.Error()})
	default:
		h.renderDBUnavailable(c)
	}
}

func (h *PostingHandler) renderDBUnavailable(c *gin.Context) {
	c.Header("Retry-After", "1")
	c.JSON(http.StatusServiceUnavailable, errorResponse{Code: "DB_UNAVAILABLE"})
}

func (h *PostingHandler) GetTransaction(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Code: "INVALID_ID"})
		return
	}

	tx, err := h.svc.GetTransaction(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Code: "NOT_FOUND"})
			return
		}
		h.renderDBUnavailable(c)
		return
	}
	c.JSON(http.StatusOK, renderPosting(tx))
}
