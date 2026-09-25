package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	monty "github.com/ewhauser/gomonty"
	"github.com/ewhauser/gomonty/otelmonty"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRedactedMontyTelemetryHidesContent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	handler := redactedMontyHandler{Handler: otelmonty.Handler{Tracer: provider.Tracer("test")}}
	ctx, span := handler.StartExecution(context.Background(), monty.ExecutionInfo{ScriptName: "run_code.py"})
	handler.RecordPrint(ctx, "private print")
	span.End(monty.Value{}, errors.New("private exception"), monty.ExecutionTiming{}, monty.TruncatedPayload{})

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "monty.run" {
		t.Fatalf("ended spans = %v, want one monty.run", spans)
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", spans[0].Status())
	}
	content := fmt.Sprint(spans[0].Status(), spans[0].Events(), spans[0].Attributes())
	if strings.Contains(content, "private") {
		t.Fatalf("private content appeared in span: %s", content)
	}
}

func TestMontyNanosecondTimings(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	handler := nanosecondMontyHandler{TelemetryHandler: otelmonty.Handler{Tracer: provider.Tracer("test")}}
	_, span := handler.StartExecution(context.Background(), monty.ExecutionInfo{ScriptName: "run_code.py"})
	span.End(monty.Value{}, nil, monty.ExecutionTiming{
		Total:    750 * time.Microsecond,
		Callback: 100 * time.Microsecond,
		Wait:     50 * time.Microsecond,
	}, monty.TruncatedPayload{})

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended %d spans, want 1", len(spans))
	}
	want := map[string]int64{
		"monty.python_duration_ns":   600_000,
		"monty.callback_duration_ns": 100_000,
		"monty.wait_duration_ns":     50_000,
	}
	for _, attr := range spans[0].Attributes() {
		if expected, ok := want[string(attr.Key)]; ok {
			if got := attr.Value.AsInt64(); got != expected {
				t.Errorf("%s = %d, want %d", attr.Key, got, expected)
			}
			delete(want, string(attr.Key))
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing timing attributes: %v", want)
	}
}
