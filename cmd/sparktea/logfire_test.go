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
	rt.annotate("code_mode", true, "code_backend", "cpython", "script_turn", 2, "skipped", 1.5, 7, "odd")
	span.End()

	got := map[string]attribute.Value{}
	for _, kv := range recorder.Ended()[0].Attributes() {
		got[string(kv.Key)] = kv.Value
	}
	want := map[string]attribute.Value{
		"sparktea.code_mode":    attribute.BoolValue(true),
		"sparktea.code_backend": attribute.StringValue("cpython"),
		"sparktea.script_turn":  attribute.IntValue(2),
		"logfire.tags":          attribute.StringSliceValue([]string{"cpython"}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attributes = %v, want %v", got, want)
	}
}

func TestRunTracerTags(t *testing.T) {
	cases := []struct {
		kv   []any
		want []string
	}{
		{[]any{"web_search", true, "code_mode", true, "code_backend", "monty"}, []string{"web_search", "monty"}},
		{[]any{"web_search", false, "code_mode", false}, nil},
	}
	for _, tc := range cases {
		recorder := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		_, span := tp.Tracer("test").Start(context.Background(), "sparktea turn")
		(&runTracer{span: span}).annotate(tc.kv...)
		span.End()
		var got []string
		for _, kv := range recorder.Ended()[0].Attributes() {
			if kv.Key == "logfire.tags" {
				got = kv.Value.AsStringSlice()
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("annotate(%v) tags = %v, want %v", tc.kv, got, tc.want)
		}
	}
}
