package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type EntryType string

const (
	Debit  EntryType = "DEBIT"
	Credit EntryType = "CREDIT"
)

type TransactionType string

const (
	TransactionTransfer    TransactionType = "TRANSFER"
	TransactionDeposit     TransactionType = "DEPOSIT"
	TransactionWithdrawal  TransactionType = "WITHDRAWAL"
	TransactionReversal    TransactionType = "REVERSAL"
)

type LedgerEntry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	EntryType     EntryType
	Amount        Money
	CreatedAt     time.Time
}

type LedgerTransaction struct {
	ID                   uuid.UUID
	Type                 TransactionType
	ReversesTransactionID *uuid.UUID
	Description          string
	Entries              []LedgerEntry
	CreatedAt            time.Time
}

func (t *LedgerTransaction) ValidateBalance() error {
	net := decimal.Zero
	for _, e := range t.Entries {
		if e.EntryType == Credit {
			net = net.Add(e.Amount.Decimal())
		} else {
			net = net.Sub(e.Amount.Decimal())
		}
	}
	if !net.IsZero() {
		return fmt.Errorf("transaction %s is unbalanced (net = %s)", t.ID, net.StringFixed(2))
	}
	return nil
}
