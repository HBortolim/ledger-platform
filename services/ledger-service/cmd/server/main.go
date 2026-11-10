package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/sync/errgroup"

	"github.com/ledger-platform/ledger-service/internal/config"
	"github.com/ledger-platform/ledger-service/internal/handler"
	"github.com/ledger-platform/ledger-service/internal/logging"
	"github.com/ledger-platform/ledger-service/internal/observability"
	"github.com/ledger-platform/ledger-service/internal/outbox"
	"github.com/ledger-platform/ledger-service/internal/reconciliation"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
)

func main() {
	logging.Setup("ledger-service")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	shutdownTracing, err := observability.SetupTracing(ctx, "ledger-service")
	if err != nil {
		slog.Error("cannot initialise tracing", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if err := shutdownTracing(flushCtx); err != nil {
			slog.Error("tracing shutdown error", slog.Any("error", err))
		}
	}()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("cannot connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	producer, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ClientID("ledger-service-outbox-worker"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.AllowIdempotentProduceCancellation(),
	)
	if err != nil {
		slog.Error("cannot create kafka producer", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if err := producer.Flush(flushCtx); err != nil {
			slog.ErrorContext(flushCtx, "kafka flush error", slog.Any("error", err))
		}
		producer.Close()
	}()

	postingRepo := repository.NewPostingRepository(pool, cfg.DailyTransferCap, cfg.SystemAccountID)
	postingSvc := service.NewPostingService(postingRepo)
	postingHandler := handler.NewPostingHandler(postingSvc)

	reconRepo := repository.NewReconciliationRepository(pool)
	reconSvc := service.NewReconciliationService(reconRepo, service.ReconciliationOptions{
		InvariantWindow: 2 * cfg.ReconciliationInterval, // slack, so a slow tick never leaves a gap
		DriftGrace:      10 * time.Second,               // 2 x NFR-CONS-2's p99 lag of 5s
		PassTimeout:     2 * time.Minute,
	})
	reconHandler := handler.NewReconciliationHandler(reconSvc)

	router := gin.New()
	router.Use(gin.Recovery())

	handler.RegisterRoutes(router, pool, postingHandler, reconHandler)

	srv := &http.Server{
		Addr:    cfg.AppPort,
		Handler: router,
	}

	worker := outbox.NewWorker(pool, producer)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return worker.Run(gctx)
	})
	if cfg.ReconciliationEnabled {
		scheduler := reconciliation.NewScheduler(reconSvc, cfg.ReconciliationInterval)
		g.Go(func() error { return scheduler.Run(gctx) })
	}
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
		slog.ErrorContext(shutdownCtx, "shutdown error", slog.Any("error", err))
	}

	if err := g.Wait(); err != nil {
		slog.ErrorContext(shutdownCtx, "worker group error", slog.Any("error", err))
	}
}
