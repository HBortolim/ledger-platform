package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type postingEntryRequest struct {
	AccountID uuid.UUID `json:"accountId" binding:"required"`
	EntryType string    `json:"entryType" binding:"required,oneof=DEBIT CREDIT"`
	Amount    string    `json:"amount" binding:"required"`
}

type postingRequest struct {
	TransactionID uuid.UUID             `json:"transactionId" binding:"required"`
	Type          string                `json:"type" binding:"required"`
	Description   string                `json:"description"`
	Entries       []postingEntryRequest `json:"entries" binding:"required,min=2"`
}

func PostPosting(c *gin.Context) {
	var req postingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_ERROR", "message": err.Error()})
		return
	}
	// TODO: call service layer
	c.JSON(http.StatusNotImplemented, gin.H{"message": "not yet implemented"})
}

func GetTransaction(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_ID"})
		return
	}
	_ = id
	// TODO: call service layer
	c.JSON(http.StatusNotImplemented, gin.H{"message": "not yet implemented"})
}
