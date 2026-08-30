package consumer

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ledgerPostedEvent mirrors the LEDGER_POSTED payload produced by the Ledger
// Service's outbox (services/ledger-service/internal/repository/posting.go,
// type ledgerPostedPayload), per SPEC.md §7.2.
type ledgerPostedEvent struct {
	SchemaVersion   int          `json:"schemaVersion"`
	EventID         uuid.UUID    `json:"eventId"`
	EventType       string       `json:"eventType"`
	OccurredAt      time.Time    `json:"occurredAt"`
	TransactionID   uuid.UUID    `json:"transactionId"`
	TransactionType string       `json:"transactionType"`
	Entries         []entryEvent `json:"entries"`
}

type entryEvent struct {
	EntryID   uuid.UUID       `json:"entryId"`
	AccountID uuid.UUID       `json:"accountId"`
	EntryType string          `json:"entryType"`
	Amount    decimal.Decimal `json:"amount"`
}

// decodeLedgerPostedEvent parses a raw Kafka record value into a
// ledgerPostedEvent. Callers treat a decode failure as a poison message:
// log, count, and move on rather than blocking the partition.
func decodeLedgerPostedEvent(payload []byte) (ledgerPostedEvent, error) {
	var e ledgerPostedEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return ledgerPostedEvent{}, fmt.Errorf("decode ledger posted event: %w", err)
	}
	return e, nil
}
