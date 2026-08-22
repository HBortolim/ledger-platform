// Package observability wires OpenTelemetry tracing per SPEC.md NFR-OBS-2/5.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupTracing installs the global W3C propagator and, when an OTLP endpoint
// is configured, a batching tracer provider exporting to it. The returned
// shutdown function flushes pending spans and must be called before the
// process exits — without it, the batcher drops whatever it is holding.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset, tracing is disabled and the
// returned shutdown is a no-op (milestone-5 overview, decision #3): the
// service must run normally with no observability stack present, which is
// what `make up`, `make test`, and `make test-e2e` all rely on. The
// propagator is installed either way, so inbound traceparent headers are
// still parsed and outbound ones still written.
func SetupTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	// Route OTel SDK-internal errors (e.g. failed OTLP exports when no
	// collector is present) through the structured slog logger instead of
	// the SDK's default fallback, which writes a plain-text line via the
	// stdlib log package straight to stderr — violating the "structured
	// JSON to stdout, nothing else" constraint on the default `make up`
	// path, where no collector exists and every export attempt fails.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("otel sdk error", slog.Any("error", err))
	}))

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	// otlptracegrpc reads OTEL_EXPORTER_OTLP_ENDPOINT itself, including
	// inferring insecure transport from an http:// scheme — so the endpoint
	// is deliberately not passed explicitly here.
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// Always-on: a portfolio demo that drops the reviewer's one transfer
		// is worthless (milestone-5 overview, Global Constraints).
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
