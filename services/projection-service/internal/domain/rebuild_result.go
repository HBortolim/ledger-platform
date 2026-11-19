package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// RebuildResult is the outcome of rebuilding one wallet's projected balance from the ledger.
type RebuildResult struct {
	WalletID       uuid.UUID
	Balance        decimal.Decimal
	EntriesApplied int
	RebuiltAt      time.Time
}
