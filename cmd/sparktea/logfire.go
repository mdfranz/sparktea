package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// tracer creates the turn and tool-call spans startRunTracer adds around
// each agent run. It's safe to use unconditionally: until initLogfire (if
// ever) calls otel.SetTracerProvider, otel's global tracer is a no-op, so
// every span below costs nothing and sends nowhere.
var tracer = otel.Tracer("sparktea")

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

	// otel's default global ErrorHandler prints straight to os.Stderr
	// (log.Print) whenever any SDK-internal operation fails -- and
	// sdkmetric.PeriodicReader calls it on every export tick that errors,
	// not just at shutdown (see (*PeriodicReader).run in
	// go.opentelemetry.io/otel/sdk/metric). Confirmed live 2026-09-06: with
	// a tick landing on a period with no new metric points, Logfire 422s
	// that (now-empty) export the same way it 422s the shutdown-forced one
	// (see the comment below) -- and unlike our own shutdown path, that
	// error never passes through code we control, so it can't be
	// conditionally silenced there. It goes straight to the raw terminal
	// underneath bubbletea's alt-screen, corrupting the TUI. Telemetry
	// failures should never do that regardless of cause, so route every
	// otel-internal error to the local debug log instead -- still
	// inspectable, never on screen.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logLocal(slog.LevelWarn, "otel_internal_error", "error", err.Error())
	}))

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
		serviceName = "sparktea"
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
		metricsErr := meterProvider.Shutdown(ctx)
		if metricsErr != nil {
			// Confirmed via Logfire query (2026-09-05): the periodic reader
			// already ships every recorded histogram point on its own
			// ~60s cadence, so by the time shutdown forces one more
			// collect-and-export there's nothing new to send. Logfire's
			// metrics endpoint 422s on that trailing, unchanged export
			// instead of treating it as a no-op — cosmetic, no data is
			// lost, so don't surface it as a telemetry failure.
			logLocal(slog.LevelDebug, "logfire_metrics_final_flush_failed", "error", metricsErr.Error())
			metricsErr = nil
		}
		return errors.Join(tracerProvider.Shutdown(ctx), metricsErr)
	}, nil
}

// runTracer wraps one agent run in a span and turns each native
// (provider-executed) tool call into its own child span. Without this, tools
// like web_search or code_execution never appear structurally in Logfire:
// the pydantic-ai-go library only folds them into the chat span's
// message-history attribute, which is redacted unless LOGFIRE_SEND_CONTENT=1
// — so a run that made a dozen native tool calls shows up as a single
// opaque "chat" span.
//
// The turn span exists so those tool-call spans have somewhere to nest:
// newAgentFor's caller doesn't get back the context the library's own
// invoke_agent/chat spans run in, so a span opened directly on the run's
// context and passed down (see startRunTracer) is the only way to put tool
// spans in the same trace as the model call they belong to.
type runTracer struct {
	span  trace.Span
	ctx   context.Context
	tools map[string]trace.Span
}

// startRunTracer opens the turn span and returns the context to run the
// agent with, so invoke_agent/chat and any tool-call spans this reports all
// land in one trace.
func startRunTracer(ctx context.Context, name string) (*runTracer, context.Context) {
	spanCtx, span := tracer.Start(ctx, name)
	return &runTracer{span: span, ctx: spanCtx, tools: map[string]trace.Span{}}, spanCtx
}

// observe starts or ends a child span for a native tool call/return part; it
// ignores every other part kind. Call it for every ai.PartStartEvent.Part
// seen while draining a run's event stream.
func (t *runTracer) observe(part ai.ResponsePart) {
	switch part := part.(type) {
	case ai.NativeToolCallPart:
		if part.ToolCallID == "" {
			return
		}
		logLocal(slog.LevelInfo, "native_tool_started", "tool", part.ToolName)
		_, span := tracer.Start(t.ctx, part.ToolName, trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", part.ToolName),
			attribute.String("gen_ai.tool.call.id", part.ToolCallID),
		))
		t.tools[part.ToolCallID] = span

	case ai.NativeToolReturnPart:
		span, ok := t.tools[part.ToolCallID]
		if !ok {
			return
		}
		if part.Outcome != "" && part.Outcome != ai.ToolReturnOutcomeSuccess {
			span.SetStatus(codes.Error, string(part.Outcome))
			logLocal(slog.LevelWarn, "native_tool_finished", "tool", part.ToolName, "outcome", string(part.Outcome))
		} else {
			logLocal(slog.LevelInfo, "native_tool_finished", "tool", part.ToolName, "outcome", "success")
		}
		span.End()
		delete(t.tools, part.ToolCallID)
	}
}

// end closes any tool spans left open — e.g. the run was cancelled
// mid-call — and then the turn span itself. Call it exactly once, however
// the run finished.
func (t *runTracer) end(err error) {
	for id, span := range t.tools {
		span.SetStatus(codes.Error, "interrupted")
		span.End()
		delete(t.tools, id)
	}
	if err != nil {
		t.span.SetStatus(codes.Error, err.Error())
		t.span.RecordError(err)
	}
	t.span.End()
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
