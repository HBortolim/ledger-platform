package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ledger-platform/projection-service/internal/utils"
)

type RebuildHandler struct {
	svc rebuildService
}

type rebuildService interface {
	Rebuild(ctx context.Context, walletID uuid.UUID) (utils.Result, error)
}

func NewRebuildHandler(svc rebuildService) *RebuildHandler {
	return &RebuildHandler{svc: svc}
}

func (h *RebuildHandler) Rebuild(c *gin.Context) {
	walletIDStr := c.Param("walletId")
	walletID, err := uuid.Parse(walletIDStr)

	if err != nil {
		c.JSON(400, gin.H{"error": "invalid wallet ID"})
		return
	}

	result, err := h.svc.Rebuild(c.Request.Context(), walletID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "rebuild failed"})
		return
      }

	c.JSON(http.StatusOK, gin.H{
		"walletId": result.WalletID,
		"balance":  result.Balance.StringFixed(2),
		"entriesApplied": result.EntriesApplied,
		"rebuiltAt": result.RebuiltAt,
	})
}