package service

import "fmt"

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
