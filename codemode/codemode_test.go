package codemode

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	monty "github.com/ewhauser/gomonty"
)

// run is a small helper mirroring handleRunCode's args-decoding shape,
// against a fresh CodeMode with the given limits (defaultLimits() if nil).
func run(t *testing.T, code string, limits *monty.ResourceLimits) any {
	t.Helper()
	c := New()
	if limits != nil {
		c.limits = limits
	}
	args, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	result, err := c.handleRunCode(context.Background(), args)
	if err != nil {
		t.Fatalf("handleRunCode returned a Go error (want tool content, even for a bad script): %v", err)
	}
	return result
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
	got := run(t, "def broken(:", nil)
	m, ok := got.(map[string]any)
	if !ok || m["error"] == nil || m["error"] == "" {
		t.Fatalf("got %#v, want a non-empty {\"error\": ...} map, not a panic or Go error", got)
	}
}

func TestRunCodeDisallowedImport(t *testing.T) {
	got := run(t, "import requests", nil)
	m, ok := got.(map[string]any)
	if !ok || m["error"] == nil || m["error"] == "" {
		t.Fatalf("got %#v, want a non-empty {\"error\": ...} map for a disallowed import", got)
	}
}

func TestRunCodeExceedsMaxDuration(t *testing.T) {
	limits := &monty.ResourceLimits{
		MaxDuration:       50 * time.Millisecond,
		MaxMemory:         64 << 20,
		MaxRecursionDepth: 1000,
	}
	done := make(chan any, 1)
	go func() { done <- run(t, "while True:\n    pass", limits) }()
	select {
	case got := <-done:
		m, ok := got.(map[string]any)
		if !ok || m["error"] == nil {
			t.Fatalf("got %#v, want a non-empty {\"error\": ...} map", got)
		}
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
	got := run(t, "[0] * 100_000_000", limits)
	m, ok := got.(map[string]any)
	if !ok || m["error"] == nil {
		t.Fatalf("got %#v, want a non-empty {\"error\": ...} map", got)
	}
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
