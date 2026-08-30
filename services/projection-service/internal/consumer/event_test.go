package consumer

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestDecodeLedgerPostedEvent_ValidPayload(t *testing.T) {
	payload := []byte(`{
		"schemaVersion": 1,
		"eventId": "9b1f2c3a-1111-4a2b-8c3d-4e5f60718293",
		"eventType": "LEDGER_POSTED",
		"occurredAt": "2026-05-19T14:23:00.123Z",
		"transactionId": "550e8400-e29b-41d4-a716-446655440000",
		"transactionType": "TRANSFER",
		"entries": [
			{"entryId":"11111111-1111-1111-1111-111111111111","accountId":"22222222-2222-2222-2222-222222222222","entryType":"DEBIT","amount":"100.00"},
			{"entryId":"33333333-3333-3333-3333-333333333333","accountId":"44444444-4444-4444-4444-444444444444","entryType":"CREDIT","amount":"100.00"}
		]
	}`)

	event, err := decodeLedgerPostedEvent(payload)
	if err != nil {
		t.Fatalf("decodeLedgerPostedEvent() error = %v, want nil", err)
	}

	if event.EventType != "LEDGER_POSTED" {
		t.Errorf("EventType = %q, want LEDGER_POSTED", event.EventType)
	}
	if event.TransactionID.String() != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("TransactionID = %q, want 550e8400-e29b-41d4-a716-446655440000", event.TransactionID)
	}
	if len(event.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(event.Entries))
	}
	if event.Entries[0].EntryType != "DEBIT" || !event.Entries[0].Amount.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("Entries[0] = %+v, want DEBIT 100.00", event.Entries[0])
	}
	if event.Entries[1].EntryType != "CREDIT" || !event.Entries[1].Amount.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("Entries[1] = %+v, want CREDIT 100.00", event.Entries[1])
	}
}

func TestDecodeLedgerPostedEvent_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := decodeLedgerPostedEvent([]byte(`{not json`))
	if err == nil {
		t.Fatal("decodeLedgerPostedEvent() error = nil, want error for malformed JSON")
	}
}
