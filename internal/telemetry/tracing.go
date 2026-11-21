package telemetry

// OpenTelemetry tracing, opt-in via the standard OTEL_EXPORTER_OTLP_ENDPOINT
// environment variable. When unset the global tracer stays a no-op and the
// otelgin/otelpgx instrumentation costs nothing (#374). OTLP over HTTP keeps
// the gRPC stack out of the binary.

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracing installs the global TracerProvider when an OTLP endpoint is
// configured. The returned shutdown flushes the batcher; it is nil-safe.
func InitTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}
	exp, err := otlptracehttp.New(ctx) // endpoint and headers come from the env
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
