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
	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/consumer"
	"github.com/ledger-platform/projection-service/internal/handler"
	"github.com/ledger-platform/projection-service/internal/logging"
	"github.com/ledger-platform/projection-service/internal/observability"
)

func main() {
	logging.Setup("projection-service")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	shutdownTracing, err := observability.SetupTracing(ctx, "projection-service")
	if err != nil {
		slog.Error("cannot initialise tracing", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() {
		// Fresh context: ctx is already cancelled by the time this runs, and
		// a cancelled context would abort the flush this exists to perform.
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		if err := shutdownTracing(flushCtx); err != nil {
			slog.Error("tracing shutdown error", slog.Any("error", err))
		}
	}()

	config, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		slog.Error("cannot connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	router := gin.New()
	router.Use(gin.Recovery())
	handler.RegisterRoutes(router, pool)

	go func() {
		c := consumer.NewLedgerPostedConsumer(pool, config)
		c.Run(ctx)
	}()

	srv := &http.Server{
		Addr:    config.AppPort,
		Handler: router,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.ErrorContext(ctx, "server error", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.ErrorContext(shutdownCtx, "shutdown error", slog.Any("error", err))
	}
}
