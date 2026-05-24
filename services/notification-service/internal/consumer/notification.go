package consumer

import (
	"context"
	"log"
)

type NotificationConsumer struct{}

func NewNotificationConsumer() *NotificationConsumer {
	return &NotificationConsumer{}
}

func (c *NotificationConsumer) Run(ctx context.Context) {
	// TODO: consume LEDGER_POSTED and BALANCE_UPDATED, emit notifications to stdout.
	log.Println("notification consumer started (stub)")
	<-ctx.Done()
}
