package domain

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ErrNotFound is returned when a requested entity is absent.
var ErrNotFound = errors.New("not found")

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

// ErrDailyCapExceeded is returned when a DEBIT account's cumulative debits for the current
// day, including this posting, would exceed the configured daily transfer cap.
type ErrDailyCapExceeded struct {
	AccountID uuid.UUID
	Limit     decimal.Decimal
	Attempted decimal.Decimal
}

func (e ErrDailyCapExceeded) Error() string {
	return fmt.Sprintf("daily transfer cap exceeded for account %s: attempted %s, limit %s", e.AccountID, e.Attempted, e.Limit)
}

// ErrUnbalanced is returned when a posting's entries do not sum to zero
// (SPEC.md §3.2), caught by the application-side pre-check before the
// repository is ever called.
type ErrUnbalanced struct {
	Message string
}

func (e ErrUnbalanced) Error() string { return e.Message }

// ErrInvalidAmount is returned when an entry's amount fails domain.NewMoney
// parsing (not exactly 2 decimal places, non-positive, or malformed).
type ErrInvalidAmount struct {
	Reason string
}

func (e ErrInvalidAmount) Error() string {
	return fmt.Sprintf("invalid amount: %s", e.Reason)
}
