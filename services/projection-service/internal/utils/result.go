package utils

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Result struct {
	WalletID       uuid.UUID
	Balance        decimal.Decimal
	EntriesApplied int
	RebuiltAt      time.Time
}