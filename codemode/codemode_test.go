package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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

// TestRunCodeUnsupportedStdlibModules tracks the latest Monty stdlib surface.
// Keep the run_code description in sync when the gomonty pin is refreshed.
func TestRunCodeUnsupportedStdlibModules(t *testing.T) {
	for _, mod := range []string{"statistics", "enum"} {
		t.Run(mod, func(t *testing.T) {
			runExpectingRetry(t, "import "+mod, nil)
		})
	}
}

// TestRunCodeSupportedStdlibModules covers useful stdlib APIs in the latest
// Monty release. random is explicitly seeded so it never needs host entropy;
// time is checked only for its exported name because calls require a host OS
// handler, which CodeMode does not provide.
func TestRunCodeSupportedStdlibModules(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want any
	}{
		{"collections", "import collections\ncollections.Counter([1, 1, 2])[1]", int64(2)},
		{"itertools", "import itertools\nlen(list(itertools.batched(range(5), 2)))", int64(3)},
		{"itertools accumulate", "import itertools\nlist(itertools.accumulate([1, 2, 3]))[-1]", int64(6)},
		{"functools", "import functools\nfunctools.partial(lambda a, b: a + b, 20)(22)", int64(42)},
		{"copy", "import copy\ncopy.deepcopy([1, [2]])[1][0]", int64(2)},
		{"random", "import random\nrng = random.Random(42)\nrng.randint(1, 10)", int64(2)},
		{"time", "import time\nhasattr(time, 'time')", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.code, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestLatestMontyPythonLanguageFeatures is a compatibility matrix for the
// language constructs supported by Monty v1.0.0-beta.2 that are useful for
// model-written calculations and data-wrangling scripts. It intentionally
// tests observable results through CodeMode's flattening path, not just that
// source compiles.
func TestLatestMontyPythonLanguageFeatures(t *testing.T) {
	cases := []struct {
		name string
		code string
		want any
	}{
		{
			name: "closures and lambdas",
			code: "def make_adder(x):\n    return lambda y: x + y\nmake_adder(40)(2)",
			want: int64(42),
		},
		{
			name: "function decorators",
			code: "def label(fn):\n    def wrapped():\n        return 'answer=' + str(fn())\n    return wrapped\n@label\ndef answer():\n    return 42\nanswer()",
			want: "answer=42",
		},
		{
			name: "frozen dataclass",
			code: "from dataclasses import dataclass\n@dataclass(frozen=True)\nclass Point:\n    x: int\n    y: int\np = Point(19, 23)\np.x + p.y",
			want: int64(42),
		},
		{
			name: "comprehensions",
			code: "values = [x * x for x in range(6) if x % 2 == 0]\nsum(values)",
			want: int64(20),
		},
		{
			name: "comprehension preserves outer variable",
			code: "x = 42\nvalues = [x for x in [1, 2]]\nx",
			want: int64(42),
		},
		{
			name: "exception handling and finally",
			code: "events = []\ntry:\n    raise ValueError('bad')\nexcept ValueError as exc:\n    events.append(str(exc))\nelse:\n    events.append('else')\nfinally:\n    events.append('finally')\nlen(events)",
			want: int64(2),
		},
		{
			name: "user context manager",
			code: "class Context:\n    def __enter__(self):\n        return 40\n    def __exit__(self, exc_type, exc, tb):\n        return False\nwith Context() as value:\n    result = value + 2\nresult",
			want: int64(42),
		},
		{
			name: "f-string debug form",
			code: "value = 3.14159\nf'{value=:.2f}'",
			want: "value=3.14",
		},
		{
			name: "percent formatting",
			code: `"%.2f" % 3.14159`,
			want: "3.14",
		},
		{
			name: "nested format fields",
			code: `"{value:{width}.{precision}f}".format(value=3.14159, width=0, precision=2)`,
			want: "3.14",
		},
		{
			name: "async gather and await",
			code: "import asyncio\nasync def value(number):\n    return number\nasync def main():\n    return sum(await asyncio.gather(value(20), value(22)))\nasyncio.run(main())",
			want: int64(42),
		},
		{
			name: "runtime generic annotations and unions",
			code: "def total(values: list[int] | None) -> int:\n    return sum(values) if values is not None else 0\ntotal([20, 22])",
			want: int64(42),
		},
		{
			name: "starred unpacking",
			code: "first, *middle, last = [1, 2, 3, 4]\nfirst + sum(middle) + last",
			want: int64(10),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.code, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestLatestMontyRejectedLanguageFeatures records syntax the latest Monty
// parser intentionally rejects before execution. These are not CPython
// compatibility gaps to work around inside sparktea.
func TestLatestMontyRejectedLanguageFeatures(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"match statement", "match 1:\n    case 1:\n        result = 1"},
		{"generator function", "def values():\n    yield 1\nvalues()"},
		{"class inheritance", "class Child(Parent):\n    pass"},
		{"del statement", "value = 1\ndel value"},
		{"PEP 695 type alias", "type Number = int | float"},
		{"async with", "async def main():\n    async with manager():\n        pass\nmain()"},
		{"async for", "async def main():\n    async for value in source():\n        pass\nmain()"},
		{"template string", `t"value: {42}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runExpectingRetry(t, tc.code, nil)
		})
	}
}

// TestRunCodeOSCallsFailCleanly confirms a real OS-touching call (the
// sandbox has no filesystem/env, and this package wires no OS handler)
// reports a normal, immediate error rather than hanging until MaxDuration —
// the description promises NotImplementedError, not a stall.
func TestRunCodeOSCallsFailCleanly(t *testing.T) {
	limits := &monty.ResourceLimits{MaxDuration: 2 * time.Second, MaxMemory: 64 << 20, MaxRecursionDepth: 100}
	msg := runExpectingRetry(t, "import os\nos.getenv(\"HOME\")", limits)
	if !strings.Contains(msg, "NotImplementedError") {
		t.Fatalf("got retry message %q, want it to mention NotImplementedError", msg)
	}
}

func TestRunCodeFormatMethodWorks(t *testing.T) {
	got := run(t, `"{:.2f}".format(3.14159)`, nil)
	if got != "3.14" {
		t.Fatalf("got %#v, want \"3.14\"", got)
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
