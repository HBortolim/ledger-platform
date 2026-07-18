package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/sync/errgroup"

	"github.com/ledger-platform/ledger-service/internal/config"
	"github.com/ledger-platform/ledger-service/internal/handler"
	"github.com/ledger-platform/ledger-service/internal/outbox"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("cannot connect to database: %v", err)
	}
	defer pool.Close()

	producer, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ClientID("ledger-service-outbox-worker"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.AllowIdempotentProduceCancellation(),
	)
	if err != nil {
		log.Fatalf("cannot create kafka producer: %v", err)
	}
	defer func() {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if err := producer.Flush(flushCtx); err != nil {
			log.Printf("kafka flush error: %v", err)
		}
		producer.Close()
	}()

	postingRepo := repository.NewPostingRepository(pool)
	postingSvc := service.NewPostingService(postingRepo)
	postingHandler := handler.NewPostingHandler(postingSvc)

	router := gin.New()
	router.Use(gin.Recovery())

	handler.RegisterRoutes(router, pool, postingHandler)

	srv := &http.Server{
		Addr:    cfg.AppPort,
		Handler: router,
	}

	worker := outbox.NewWorker(pool, producer)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return worker.Run(gctx)
	})
	g.Go(func() error {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	})

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	if err := g.Wait(); err != nil {
		log.Printf("worker group error: %v", err)
	}
}
