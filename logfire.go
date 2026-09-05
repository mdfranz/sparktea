package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// defaultLogfireEndpoint is Logfire's US-region OTLP host. Set LOGFIRE_ENDPOINT
// (e.g. "logfire-eu.pydantic.dev") to use another region or a proxy.
const defaultLogfireEndpoint = "logfire-us.pydantic.dev"

// logfireCapability instruments every agent this session builds. It stays
// nil, silently, when LOGFIRE_TOKEN isn't set.
var logfireCapability ai.Capability

// initLogfire wires OpenTelemetry traces and metrics to Logfire when
// LOGFIRE_TOKEN is set. It sets the global tracer/meter providers and
// logfireCapability for newAgentFor to pick up, then returns a shutdown func
// that flushes and closes the exporters; call it before the program exits.
// A nil LOGFIRE_TOKEN is not an error: shutdown is a no-op and the agent
// runs uninstrumented, exactly as before this feature existed.
func initLogfire(ctx context.Context) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }

	token := os.Getenv("LOGFIRE_TOKEN")
	if token == "" {
		return noop, nil
	}

	endpoint := logfireEndpoint()
	headers := map[string]string{"Authorization": token}

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithHeaders(headers),
	)
	if err != nil {
		return noop, fmt.Errorf("logfire: create trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithHeaders(headers),
	)
	if err != nil {
		return noop, fmt.Errorf("logfire: create metric exporter: %w", err)
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "openrouter-agent"
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceNameKey.String(serviceName),
	))
	if err != nil {
		return noop, fmt.Errorf("logfire: build resource: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	// Redact prompts, completions, and request parameters by default: this
	// telemetry leaves the local machine. Opt in once the Logfire project is
	// one you trust with conversation content.
	sendContent := os.Getenv("LOGFIRE_SEND_CONTENT") == "1"
	logfireCapability = ai.NewInstrumentation(
		ai.WithInstrumentationContent(sendContent),
		ai.WithInstrumentationBinaryContent(sendContent),
		ai.WithInstrumentationModelRequestParameters(sendContent),
	)

	return func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}, nil
}

// logfireEndpoint returns the bare host:port otlptracehttp/otlpmetrichttp
// expect, accepting LOGFIRE_ENDPOINT as either a bare host or a full URL.
func logfireEndpoint() string {
	endpoint := os.Getenv("LOGFIRE_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultLogfireEndpoint
	}
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return strings.TrimSuffix(endpoint, "/")
}
