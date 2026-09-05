package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Kludex/pydantic-ai-go/ai"
	monty "github.com/ewhauser/gomonty"
)

// runRaw runs code through a fresh CodeMode with the given limits
// (defaultLimits() if nil) and returns handleRunCode's raw (result, error) —
// no test assertions, so it's safe to call from a non-test goroutine (see
// TestRunCodeExceedsMaxDuration; a *testing.T's Fatal family must only be
// called from the goroutine running the test).
func runRaw(code string, limits *monty.ResourceLimits) (any, error) {
	c := New()
	if limits != nil {
		c.limits = limits
	}
	args, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return nil, err
	}
	return c.handleRunCode(context.Background(), args)
}

// run asserts code succeeds and returns its result.
func run(t *testing.T, code string, limits *monty.ResourceLimits) any {
	t.Helper()
	result, err := runRaw(code, limits)
	if err != nil {
		t.Fatalf("handleRunCode returned an error for a script expected to succeed: %v", err)
	}
	return result
}

// runExpectingRetry asserts code fails the way handleRunCode reports a bad
// script: a nil result and an *ai.RetryError (not ordinary tool content, and
// not some other Go error) — see codemode.go's handleRunCode and
// runCodeMaxRetries. Returns the retry message.
func runExpectingRetry(t *testing.T, code string, limits *monty.ResourceLimits) string {
	t.Helper()
	result, err := runRaw(code, limits)
	return checkRetry(t, result, err)
}

func checkRetry(t *testing.T, result any, err error) string {
	t.Helper()
	if result != nil {
		t.Fatalf("got non-nil result %#v alongside an error", result)
	}
	var retry *ai.RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("got err %v (%T), want an *ai.RetryError", err, err)
	}
	if retry.Message == "" {
		t.Fatalf("got an empty RetryError message")
	}
	return retry.Message
}

func TestRunCodeExpressionResult(t *testing.T) {
	got := run(t, "40 + 2", nil)
	n, ok := got.(int64)
	if !ok || n != 42 {
		t.Fatalf("got %#v, want int64(42)", got)
	}
}

func TestRunCodeListAndDictResult(t *testing.T) {
	got := run(t, `[1, 2, {"a": 1}]`, nil)
	list, ok := got.([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("got %#v, want a 3-element []any", got)
	}
	if list[0].(int64) != 1 || list[1].(int64) != 2 {
		t.Fatalf("got %#v, want [1, 2, ...]", got)
	}
	dict, ok := list[2].(map[string]any)
	if !ok {
		t.Fatalf("got %#v for element 2, want map[string]any", list[2])
	}
	if dict["a"].(int64) != 1 {
		t.Fatalf("got %#v, want {\"a\": 1}", dict)
	}
}

func TestRunCodePrintOnly(t *testing.T) {
	got := run(t, `print("hi")`, nil)
	m, ok := got.(map[string]any)
	if !ok || m["output"] != "hi\n" {
		t.Fatalf("got %#v, want {\"output\": \"hi\\n\"}", got)
	}
	if _, hasResult := m["result"]; hasResult {
		t.Fatalf("got %#v, print()-only output shouldn't carry a result key", got)
	}
}

func TestRunCodeAssignmentOnlyResult(t *testing.T) {
	got := run(t, "x = 1", nil)
	m, ok := got.(map[string]any)
	if !ok || len(m) != 0 {
		t.Fatalf("got %#v, want an empty map (final value is None)", got)
	}
}

func TestRunCodeOutputAndResult(t *testing.T) {
	got := run(t, "print(\"hi\")\n21 * 2", nil)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %#v, want a map with output and result", got)
	}
	if m["output"] != "hi\n" {
		t.Fatalf("got output %#v, want \"hi\\n\"", m["output"])
	}
	if m["result"].(int64) != 42 {
		t.Fatalf("got result %#v, want 42", m["result"])
	}
}

func TestRunCodeSyntaxError(t *testing.T) {
	runExpectingRetry(t, "def broken(:", nil)
}

func TestRunCodeDisallowedImport(t *testing.T) {
	runExpectingRetry(t, "import requests", nil)
}

func TestRunCodeExceedsMaxDuration(t *testing.T) {
	limits := &monty.ResourceLimits{
		MaxDuration:       50 * time.Millisecond,
		MaxMemory:         64 << 20,
		MaxRecursionDepth: 1000,
	}
	type outcome struct {
		result any
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runRaw("while True:\n    pass", limits)
		done <- outcome{result, err}
	}()
	select {
	case o := <-done:
		checkRetry(t, o.result, o.err)
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return within 5s of a 50ms MaxDuration limit — the run is hanging")
	}
}

func TestRunCodeExceedsMaxMemory(t *testing.T) {
	limits := &monty.ResourceLimits{
		MaxDuration:       5 * time.Second,
		MaxMemory:         1 << 20, // 1 MiB
		MaxRecursionDepth: 1000,
	}
	runExpectingRetry(t, "[0] * 100_000_000", limits)
}

// TestRunCodeDefaultFlattenFallback exercises flattenValue's default branch:
// any kind not explicitly cased (class_instance, function, exception,
// date/datetime/timedelta/timezone/time, not_implemented, file_handle, and
// any future Monty value kind gomonty's wire format doesn't decode into a
// richer Go shape yet — see the version-drift note in MONTY-PLAN.md) falls
// back to Value.String() rather than a Go panic or a lossy zero value.
// NotImplemented is the simplest of these to produce via a real script.
func TestRunCodeDefaultFlattenFallback(t *testing.T) {
	got := run(t, "NotImplemented", nil)
	s, ok := got.(string)
	if !ok || s == "" {
		t.Fatalf("got %#v, want a non-empty string (Value.String() fallback)", got)
	}
}
