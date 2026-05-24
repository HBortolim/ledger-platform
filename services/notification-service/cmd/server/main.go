package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/ledger-platform/notification-service/internal/consumer"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	c := consumer.NewNotificationConsumer()
	log.Println("notification service started")
	c.Run(ctx)
}
