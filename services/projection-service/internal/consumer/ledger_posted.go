package consumer

import (
	"context"
	"log"
)

type LedgerPostedConsumer struct{}

func NewLedgerPostedConsumer() *LedgerPostedConsumer {
	return &LedgerPostedConsumer{}
}

func (c *LedgerPostedConsumer) Run(ctx context.Context) {
	// TODO: consume ledger.posted.v1, apply idempotent projection updates,
	// commit Kafka offset only after projection write commits.
	log.Println("projection consumer started (stub)")
	<-ctx.Done()
}
