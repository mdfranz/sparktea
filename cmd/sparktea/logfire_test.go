package main

import (
	"context"
	"os"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestLogfireSendContent(t *testing.T) {
	t.Run("enabled by default", func(t *testing.T) {
		t.Setenv("LOGFIRE_SEND_CONTENT", "temporary")
		if err := os.Unsetenv("LOGFIRE_SEND_CONTENT"); err != nil {
			t.Fatal(err)
		}
		if !logfireSendContent() {
			t.Fatal("logfireSendContent() = false, want true")
		}
	})

	t.Run("explicitly disabled", func(t *testing.T) {
		t.Setenv("LOGFIRE_SEND_CONTENT", "0")
		if logfireSendContent() {
			t.Fatal("logfireSendContent() = true, want false")
		}
	})
}

func TestRunTracerAnnotate(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := tp.Tracer("test").Start(context.Background(), "sparktea turn")
	rt := &runTracer{span: span}
	rt.annotate("code_mode", true, "code_backend", "python", "script_turn", 2, "skipped", 1.5, 7, "odd")
	span.End()

	got := map[string]attribute.Value{}
	for _, kv := range recorder.Ended()[0].Attributes() {
		got[string(kv.Key)] = kv.Value
	}
	want := map[string]attribute.Value{
		"sparktea.code_mode":    attribute.BoolValue(true),
		"sparktea.code_backend": attribute.StringValue("python"),
		"sparktea.script_turn":  attribute.IntValue(2),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attributes = %v, want %v", got, want)
	}
}
