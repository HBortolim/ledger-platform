package repository

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrInsufficientFunds is returned when a DEBIT account's locked balance cannot cover the posting.
type ErrInsufficientFunds struct {
	AccountID uuid.UUID
}

func (e ErrInsufficientFunds) Error() string {
	return fmt.Sprintf("insufficient funds for account %s", e.AccountID)
}

// ErrDuplicateTransaction is returned when the transactionId already exists in ledger_transactions.
type ErrDuplicateTransaction struct {
	ID uuid.UUID
}

func (e ErrDuplicateTransaction) Error() string {
	return fmt.Sprintf("transaction %s already exists", e.ID)
}

// ErrNotFound is returned when a requested entity is absent.
var ErrNotFound = errors.New("not found")
