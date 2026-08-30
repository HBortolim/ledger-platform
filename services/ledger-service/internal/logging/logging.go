// Package logging provides the structured JSON logger required by
// SPEC.md NFR-OBS-1: stdout, JSON, with timestamp/level/service/trace_id/
// span_id/msg. The field set and shape deliberately match the wallet-service's
// Logback pattern (services/wallet-service/src/main/resources/application.yml)
// so log lines from all three services collate cleanly.
package logging

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler decorates every record with the trace and span IDs of the
// active span in ctx, rendering empty strings when there is none.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)

	var traceID, spanID string
	if sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	if sc.HasSpanID() {
		spanID = sc.SpanID().String()
	}

	r.AddAttrs(
		slog.String("trace_id", traceID),
		slog.String("span_id", spanID),
	)
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// Setup installs a JSON logger as the slog default and returns it. Call once,
// early in main, before anything logs.
func Setup(service string) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// slog's default key is "time"; NFR-OBS-1's shape (and the
			// wallet-service's pattern) uses "timestamp".
			if a.Key == slog.TimeKey {
				a.Key = "timestamp"
			}
			return a
		},
	})

	logger := slog.New(traceHandler{base}).With(slog.String("service", service))
	slog.SetDefault(logger)
	return logger
}
