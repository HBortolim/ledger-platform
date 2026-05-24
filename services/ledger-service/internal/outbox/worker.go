package outbox

import (
	"context"
	"log"
	"time"
)

const pollInterval = 100 * time.Millisecond

type Worker struct{}

func NewWorker() *Worker {
	return &Worker{}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.poll(ctx); err != nil {
				log.Printf("outbox poll error: %v", err)
			}
		}
	}
}

func (w *Worker) poll(ctx context.Context) error {
	// TODO: SELECT unpublished rows FOR UPDATE SKIP LOCKED, publish to Kafka, mark published
	return nil
}
