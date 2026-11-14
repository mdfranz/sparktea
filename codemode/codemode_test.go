package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/fakes"
	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/otelmonty"
	"github.com/mdfranz/gomonty/remote"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func recordedSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	return nil
}

func spanHasAttribute(span sdktrace.ReadOnlySpan, key string) bool {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return true
		}
	}
	return false
}

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

func TestRunCodeMontyTelemetryJoinsParentTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("sparktea-test")

	c := New()
	c.telemetry = otelmonty.Handler{Tracer: tracer}
	ctx, parent := tracer.Start(context.Background(), "run_code tool")
	args, err := json.Marshal(map[string]string{"code": "print('hello')\n40 + 2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.handleRunCode(ctx, args); err != nil {
		t.Fatalf("handleRunCode: %v", err)
	}
	parent.End()

	spans := recorder.Ended()
	montyRun := recordedSpan(spans, "monty.run")
	tool := recordedSpan(spans, "run_code tool")
	if montyRun == nil || tool == nil {
		t.Fatalf("expected tool and monty.run spans, got %d spans", len(spans))
	}
	if montyRun.Parent().SpanID() != tool.SpanContext().SpanID() {
		t.Errorf("monty.run parent = %v, want tool span %v", montyRun.Parent().SpanID(), tool.SpanContext().SpanID())
	}
	if !spanHasAttribute(montyRun, "monty.output") {
		t.Error("monty.run has no monty.output attribute with content enabled")
	}
	foundPrint := false
	for _, event := range montyRun.Events() {
		foundPrint = foundPrint || event.Name == "monty.print"
	}
	if !foundPrint {
		t.Error("monty.run has no monty.print event with content enabled")
	}
}

func TestRunCodeMontyTelemetryContentOptOut(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	c := New(WithTelemetryContent(false))
	c.telemetry = otelmonty.Handler{Tracer: tp.Tracer("sparktea-test")}
	args, err := json.Marshal(map[string]string{"code": "print('secret')\n42"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.handleRunCode(context.Background(), args); err != nil {
		t.Fatalf("handleRunCode: %v", err)
	}

	montyRun := recordedSpan(recorder.Ended(), "monty.run")
	if montyRun == nil {
		t.Fatal("no monty.run span recorded")
	}
	if spanHasAttribute(montyRun, "monty.output") {
		t.Error("monty.run contains monty.output despite content opt-out")
	}
	for _, event := range montyRun.Events() {
		if event.Name == "monty.print" {
			t.Error("monty.run contains monty.print despite content opt-out")
		}
	}
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
	msg := runExpectingRetry(t, "import requests", nil)
	if !strings.Contains(msg, "ModuleNotFoundError") || !strings.Contains(msg, "requests") {
		t.Fatalf("got retry message %q, want ModuleNotFoundError mentioning requests", msg)
	}
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
	for _, mod := range []string{"statistics", "enum", "struct", "contextlib", "operator", "string", "io"} {
		t.Run(mod, func(t *testing.T) {
			runExpectingRetry(t, "import "+mod, nil)
		})
	}
}

func TestModelFacingPromptAssets(t *testing.T) {
	if got := runCodeDefinition().Description; got != montyDescription {
		t.Fatal("Monty tool definition does not use its shared prompt asset")
	}
	if got := runCodePythonDefinition().Description; got != pythonDescription {
		t.Fatal("CPython tool definition does not use its shared prompt asset")
	}
	if !strings.Contains(instructions, "restricted \"monty\" interpreter") {
		t.Fatal("Monty instruction asset is missing its expected opening text")
	}
	for name, value := range map[string]string{
		"Monty description":   montyDescription,
		"CPython description": pythonDescription,
		"Monty instruction":   instructions,
	} {
		if strings.HasSuffix(value, "\n") {
			t.Errorf("%s has an unexpected trailing newline", name)
		}
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
		{"functools.lru_cache", "import functools\n@functools.lru_cache\ndef f(n):\n    return n if n < 2 else f(n - 1) + f(n - 2)\nf(10)", int64(55)},
		{"functools.cache", "import functools\n@functools.cache\ndef f(n):\n    return n * 2\nf(21)", int64(42)},
		{"copy", "import copy\ncopy.deepcopy([1, [2]])[1][0]", int64(2)},
		{"random", "import random\nrng = random.Random(42)\nrng.randint(1, 10)", int64(2)},
		{"time", "import time\nhasattr(time, 'time')", true},
		{"hashlib", "import hashlib\nhashlib.sha256(b'hello').hexdigest()", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"bisect", "import bisect\nbisect.bisect_left([1, 3, 5], 4)", int64(2)},
		{"decimal", "from decimal import Decimal\nstr(Decimal('1.20') + Decimal('2.30'))", "3.50"},
		{"dataclasses.field", "from dataclasses import dataclass, field\n@dataclass\nclass P:\n    x: int = field(default=42)\nP().x", int64(42)},
		{"complex", "complex(1, 2)", "(1+2j)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.code, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestLatestMontyPythonLanguageFeatures is a compatibility matrix for the
// language constructs supported by Monty v1.6.0 that are useful for
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
			name: "match statement",
			code: "match 1:\n    case 1:\n        result = 42\nresult",
			want: int64(42),
		},
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
		{"generator function", "def values():\n    yield 1\nvalues()"},
		{"class inheritance", "class Child(Parent):\n    pass"},
		{"del statement", "value = 1\ndel value"},
		{"PEP 695 type alias", "type Number = int | float"},
		{"async with", "async def main():\n    async with manager():\n        pass\nmain()"},
		{"async for", "async def main():\n    async for value in source():\n        pass\nmain()"},
		{"template string", `t"value: {42}"`},
		{"method decorator", "class T:\n    @property\n    def f(self):\n        return 1\nT().f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runExpectingRetry(t, tc.code, nil)
		})
	}
}

// TestRunCodeDescribedMissingNames covers the names the run_code description
// says are missing from the latest Monty release. Keep the two in sync.
func TestRunCodeDescribedMissingNames(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"dataclasses.asdict", "from dataclasses import asdict"},
		{"re.VERBOSE", "import re\nre.VERBOSE"},
		{"sys.setrecursionlimit", "import sys\nsys.setrecursionlimit(500)"},
		{"latin-1 codec", "'é'.encode('latin-1')"},
		{"super", "class T:\n    def __init__(self):\n        super().__init__()\nT()"},
		{"bytearray", "bytearray(2)"},
		{"function __name__", "def f():\n    pass\nf.__name__"},
		{"dir builtin", "dir([])"},
		{"vars builtin", "vars()"},
		{"compile builtin", "compile('1', 'x', 'eval')"},
		{"dataclasses.fields", "from dataclasses import fields"},
		{"date.toordinal", "import datetime\ndatetime.date(2024, 1, 1).toordinal()"},
		{"instance __dict__", "class P:\n    def __init__(s):\n        s.x = 1\nP().__dict__"},
		{"property callable", "property(lambda s: 1)"},
		{"exception non-string arg", "raise ValueError(1)"},
		{"exception multiple args", "raise ValueError('a', 'b')"},
		{"type() custom exception", "type('E', (Exception,), {})"},
		{"callable builtin", "callable(len)"},
		{"heapq", "import heapq"},
		{"textwrap", "import textwrap"},
		{"functools.cmp_to_key", "import functools\nfunctools.cmp_to_key"},
		{"dataclasses.replace", "from dataclasses import replace"},
		{"dataclass order", "from dataclasses import dataclass\n@dataclass(order=True)\nclass P:\n    x: int\nP(1) < P(2)"},
		{"asyncio.create_task", "import asyncio\nasyncio.create_task"},
		{"int.to_bytes", "(1).to_bytes(2, 'big')"},
		{"str.format_map", "'{a}'.format_map({'a': 1})"},
		{"__lt__ ordering", "class P:\n    def __init__(s, x):\n        s.x = x\n    def __lt__(s, o):\n        return s.x < o.x\nsorted([P(2), P(1)])"},
		{"recursion depth", "def f(n):\n    return 0 if n == 0 else 1 + f(n - 1)\nf(150)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runExpectingRetry(t, tc.code, defaultLimits())
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

type fakeMontyBackend struct {
	onNewRepl   func(opts monty.ReplOptions) (monty.ReplHandle, error)
	replOpts    monty.ReplOptions
	replClosed  atomic.Int32
	feedCalls   atomic.Int32
	lastCode    string
}

func (b *fakeMontyBackend) NewRunner(string, monty.CompileOptions) (monty.RunnerHandle, error) {
	return nil, errors.New("fake backend supports REPL sessions only")
}

func (b *fakeMontyBackend) LoadRunner([]byte) (monty.RunnerHandle, error) {
	return nil, errors.New("fake backend supports REPL sessions only")
}

func (b *fakeMontyBackend) NewRepl(opts monty.ReplOptions) (monty.ReplHandle, error) {
	b.replOpts = opts
	return b.onNewRepl(opts)
}

func (b *fakeMontyBackend) LoadProgress([]byte) monty.Step {
	return monty.Step{Err: errors.New("unsupported")}
}

type fakeReplHandle struct {
	backend *fakeMontyBackend
	onFeed  func(ctx context.Context, code string, opts monty.FeedStartOptions) monty.Step
	closed  atomic.Bool
}

func (r *fakeReplHandle) Close() {
	if r.closed.CompareAndSwap(false, true) {
		r.backend.replClosed.Add(1)
	}
}

func (r *fakeReplHandle) Dump() ([]byte, error) { return nil, errors.New("unsupported") }

func (r *fakeReplHandle) FeedStart(ctx context.Context, code string, opts monty.FeedStartOptions) monty.Step {
	r.backend.feedCalls.Add(1)
	r.backend.lastCode = code
	return r.onFeed(ctx, code, opts)
}

type fakeCompleteProgress struct {
	next monty.ReplHandle
}

func (*fakeCompleteProgress) Close()                {}
func (*fakeCompleteProgress) Dump() ([]byte, error) { return nil, errors.New("unsupported") }
func (p *fakeCompleteProgress) TakeRepl() (monty.ReplHandle, error) {
	return p.next, nil
}
func (*fakeCompleteProgress) ResumeCall(context.Context, monty.Result) monty.Step {
	return monty.Step{Err: errors.New("already completed")}
}
func (*fakeCompleteProgress) ResumeLookup(context.Context, monty.Value, bool) monty.Step {
	return monty.Step{Err: errors.New("already completed")}
}
func (*fakeCompleteProgress) ResumeFutures(context.Context, map[uint32]monty.Result) monty.Step {
	return monty.Step{Err: errors.New("already completed")}
}

func TestRemoteConfigFromEnvAndRedaction(t *testing.T) {
	const secret = "monty-secret-token-xyz"
	t.Setenv(EnvMontyRemoteEndpoint, "worker.internal:9443")
	t.Setenv(EnvMontyRemoteToken, secret)
	t.Setenv(EnvMontyRemoteInsecure, "1")

	cfg := RemoteConfigFromEnv("")
	if cfg.Endpoint != "worker.internal:9443" || cfg.Token != secret || !cfg.Insecure {
		t.Fatalf("RemoteConfigFromEnv(\"\") = %+v", cfg)
	}

	override := RemoteConfigFromEnv(" override.internal:443 ")
	if override.Endpoint != "override.internal:443" || override.Token != secret || !override.Insecure {
		t.Fatalf("RemoteConfigFromEnv(override) = %+v", override)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	logger.Info("remote_config", "cfg", cfg)

	for name, rendered := range map[string]string{
		"String()": cfg.String(),
		"%+v":      fmt.Sprintf("%+v", cfg),
		"%#v":      fmt.Sprintf("%#v", cfg),
		"slog":     logBuf.String(),
	} {
		if strings.Contains(rendered, secret) {
			t.Errorf("%s leaked secret token: %s", name, rendered)
		}
		if !strings.Contains(rendered, "worker.internal:9443") {
			t.Errorf("%s omitted endpoint: %s", name, rendered)
		}
	}
}

func TestRunCodeRemoteFakeWorkerSuccess(t *testing.T) {
	var backend *fakeMontyBackend
	backend = &fakeMontyBackend{
		onNewRepl: func(opts monty.ReplOptions) (monty.ReplHandle, error) {
			return &fakeReplHandle{
				backend: backend,
				onFeed: func(ctx context.Context, code string, _ monty.FeedStartOptions) monty.Step {
					if sink := monty.PrintSinkFromContext(ctx); sink != nil {
						sink("stdout", "hi\n")
					}
					next := &fakeReplHandle{backend: backend}
					return monty.Step{
						Progress:        &fakeCompleteProgress{next: next},
						Prints:          "hi\n",
						PrintsDelivered: true,
						Desc: monty.ProgressDesc{
							Kind:       monty.ProgressComplete,
							ScriptName: opts.ScriptName,
							IsRepl:     true,
							Output:     monty.Int(42),
						},
					}
				},
			}, nil
		},
	}

	cm := New(WithMontyBackend(backend))
	if !cm.IsRemote() {
		t.Fatal("IsRemote() = false with WithMontyBackend")
	}

	var reqCount atomic.Int32
	model := fakes.NewFunctionModel(func(_ context.Context, messages []ai.ModelMessage, _ ai.ModelRequestParams) (*ai.ModelResponse, error) {
		if reqCount.Add(1) == 1 {
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: ToolName, Args: []byte(`{"code":"print('hi')\n40 + 2"}`), ToolCallID: "call-1"},
			}}, nil
		}
		lastReq := messages[len(messages)-1].(ai.ModelRequest)
		tr, ok := lastReq.Parts[0].(ai.ToolReturnPart)
		if !ok {
			t.Fatalf("expected ToolReturnPart, got %T", lastReq.Parts[0])
		}
		gotMap, ok := tr.Content.(map[string]any)
		if !ok || gotMap["output"] != "hi\n" || gotMap["result"] != int64(42) {
			t.Fatalf("tool return Content = %#v, want output+result 42", tr.Content)
		}
		return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "42"}}}, nil
	})

	agent := ai.NewAgent[struct{}, string](model)
	res, err := agent.Run(context.Background(), "compute", struct{}{}, ai.WithRunCapabilities(cm))
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if res.Output != "42" {
		t.Fatalf("Output = %q, want 42", res.Output)
	}
	if backend.replOpts.ScriptName != "run_code.py" || backend.replOpts.Limits == nil {
		t.Fatalf("unexpected NewRepl opts: %+v", backend.replOpts)
	}
	if backend.lastCode != "print('hi')\n40 + 2" {
		t.Fatalf("worker saw code %q", backend.lastCode)
	}
	if backend.replClosed.Load() < 2 {
		t.Fatalf("expected initial and final ReplHandles to be closed, closed count = %d", backend.replClosed.Load())
	}
}

func TestRunCodeRemoteFakeWorkerPythonErrorRetries(t *testing.T) {
	var attempt atomic.Int32
	var backend *fakeMontyBackend
	backend = &fakeMontyBackend{
		onNewRepl: func(opts monty.ReplOptions) (monty.ReplHandle, error) {
			return &fakeReplHandle{
				backend: backend,
				onFeed: func(_ context.Context, _ string, _ monty.FeedStartOptions) monty.Step {
					next := &fakeReplHandle{backend: backend}
					if attempt.Add(1) == 1 {
						return monty.Step{
							Err:  &remote.PythonError{Type: "ZeroDivisionError", Message: "division by zero"},
							Repl: next,
						}
					}
					return monty.Step{
						Progress: &fakeCompleteProgress{next: next},
						Desc: monty.ProgressDesc{
							Kind:       monty.ProgressComplete,
							ScriptName: opts.ScriptName,
							IsRepl:     true,
							Output:     monty.Int(7),
						},
					}
				},
			}, nil
		},
	}

	var reqCount atomic.Int32
	model := fakes.NewFunctionModel(func(_ context.Context, messages []ai.ModelMessage, _ ai.ModelRequestParams) (*ai.ModelResponse, error) {
		switch reqCount.Add(1) {
		case 1:
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: ToolName, Args: []byte(`{"code":"1 / 0"}`), ToolCallID: "c1"},
			}}, nil
		case 2:
			lastReq := messages[len(messages)-1].(ai.ModelRequest)
			retry, ok := lastReq.Parts[0].(ai.RetryPromptPart)
			if !ok {
				t.Fatalf("expected RetryPromptPart after PythonError, got %T", lastReq.Parts[0])
			}
			if !strings.Contains(retry.Content, "ZeroDivisionError: division by zero") {
				t.Fatalf("retry prompt = %q, want ZeroDivisionError", retry.Content)
			}
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: ToolName, Args: []byte(`{"code":"3 + 4"}`), ToolCallID: "c2"},
			}}, nil
		default:
			return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "7"}}}, nil
		}
	})

	agent := ai.NewAgent[struct{}, string](model)
	res, err := agent.Run(context.Background(), "compute", struct{}{}, ai.WithRunCapabilities(New(WithMontyBackend(backend))))
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if res.Output != "7" {
		t.Fatalf("Output = %q, want 7", res.Output)
	}
	if backend.feedCalls.Load() != 2 {
		t.Fatalf("worker FeedStart calls = %d, want 2", backend.feedCalls.Load())
	}
}

func TestRunCodeRemoteWorkerFailureAbortsTurnWithoutFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()

	cases := []struct {
		name   string
		opt    Option
		wantIn string
	}{
		{
			name: "session refused",
			opt: WithMontyBackend(&fakeMontyBackend{
				onNewRepl: func(monty.ReplOptions) (monty.ReplHandle, error) {
					return nil, &remote.SessionRefusedError{Reason: "worker full"}
				},
			}),
			wantIn: "worker full",
		},
		{
			name: "connection lost during feed",
			opt: WithMontyBackend(func() monty.Backend {
				var b *fakeMontyBackend
				b = &fakeMontyBackend{
					onNewRepl: func(monty.ReplOptions) (monty.ReplHandle, error) {
						return &fakeReplHandle{
							backend: b,
							onFeed: func(context.Context, string, monty.FeedStartOptions) monty.Step {
								return monty.Step{Err: &remote.ConnectionLostError{Op: "receive", Err: io.EOF}}
							},
						}, nil
					},
				}
				return b
			}()),
			wantIn: "connection lost",
		},
		{
			name:   "unreachable gRPC listener",
			opt:    WithRemote(RemoteConfig{Endpoint: closedAddr, Insecure: true}),
			wantIn: "remote worker",
		},
		{
			name:   "refuses token without TLS",
			opt:    WithRemote(RemoteConfig{Endpoint: "127.0.0.1:50051", Token: "secret-token", Insecure: true}),
			wantIn: "without TLS",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reqCount atomic.Int32
			model := fakes.NewFunctionModel(func(context.Context, []ai.ModelMessage, ai.ModelRequestParams) (*ai.ModelResponse, error) {
				reqCount.Add(1)
				return &ai.ModelResponse{Parts: []ai.ResponsePart{
					ai.ToolCallPart{ToolName: ToolName, Args: []byte(`{"code":"1 + 1"}`), ToolCallID: "c1"},
				}}, nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			agent := ai.NewAgent[struct{}, string](model)
			_, err := agent.Run(ctx, "compute 1+1", struct{}{}, ai.WithRunCapabilities(New(tc.opt)))
			if err == nil {
				t.Fatal("agent.Run succeeded, want remote worker failure (no local fallback)")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantIn)
			}
			var retry *ai.RetryError
			if errors.As(err, &retry) {
				t.Fatalf("worker failure was wrapped as RetryError: %v", err)
			}
			if got := reqCount.Load(); got != 1 {
				t.Fatalf("model requests = %d, want 1 (worker error must abort turn, not retry)", got)
			}
		})
	}
}

func TestRemoteMontyMatchesInProcessToolDefinitionAndInstructions(t *testing.T) {
	capture := func(cm *CodeMode) ai.ModelRequestParams {
		var captured ai.ModelRequestParams
		model := fakes.NewFunctionModel(func(_ context.Context, _ []ai.ModelMessage, params ai.ModelRequestParams) (*ai.ModelResponse, error) {
			captured = params
			return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "done"}}}, nil
		})
		agent := ai.NewAgent[struct{}, string](model)
		if _, err := agent.Run(context.Background(), "hi", struct{}{}, ai.WithRunCapabilities(cm)); err != nil {
			t.Fatalf("agent.Run: %v", err)
		}
		return captured
	}

	localParams := capture(New())
	remoteParams := capture(New(WithRemote(RemoteConfig{Endpoint: "worker.example.com:443"})))

	if !reflect.DeepEqual(localParams.Tools, remoteParams.Tools) {
		t.Fatalf("remote tools differ from in-process Monty:\nlocal:  %#v\nremote: %#v", localParams.Tools, remoteParams.Tools)
	}
	if localParams.Instructions != remoteParams.Instructions {
		t.Fatalf("remote instructions differ from in-process Monty:\nlocal:  %q\nremote: %q", localParams.Instructions, remoteParams.Instructions)
	}
}

func TestRunCodeRemoteLiveWorker(t *testing.T) {
	if os.Getenv("SPARKTEA_TEST_REMOTE_LIVE") != "1" {
		t.Skip("set SPARKTEA_TEST_REMOTE_LIVE=1 to run against live MONTY_REMOTE_ENDPOINT")
	}
	cfg := RemoteConfigFromEnv("")
	if cfg.Endpoint == "" {
		t.Skip("MONTY_REMOTE_ENDPOINT not set")
	}

	var step atomic.Int32
	var gotOutput string
	var gotResult any
	model := fakes.NewFunctionModel(func(_ context.Context, messages []ai.ModelMessage, _ ai.ModelRequestParams) (*ai.ModelResponse, error) {
		switch step.Add(1) {
		case 1:
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: ToolName, Args: []byte(`{"code":"1 / 0"}`), ToolCallID: "c1"},
			}}, nil
		case 2:
			lastReq := messages[len(messages)-1].(ai.ModelRequest)
			retry, ok := lastReq.Parts[0].(ai.RetryPromptPart)
			if !ok {
				t.Fatalf("expected RetryPromptPart after 1/0, got %T", lastReq.Parts[0])
			}
			if !strings.Contains(retry.Content, "ZeroDivisionError") {
				t.Fatalf("retry prompt = %q, want ZeroDivisionError", retry.Content)
			}
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: ToolName, Args: []byte(`{"code":"print('remote-ok')\nsum(i * i for i in range(1, 26))"}`), ToolCallID: "c2"},
			}}, nil
		default:
			lastReq := messages[len(messages)-1].(ai.ModelRequest)
			tr, ok := lastReq.Parts[0].(ai.ToolReturnPart)
			if !ok {
				t.Fatalf("expected ToolReturnPart, got %T", lastReq.Parts[0])
			}
			m, ok := tr.Content.(map[string]any)
			if !ok {
				t.Fatalf("tool return Content = %#v (%T), want map[string]any", tr.Content, tr.Content)
			}
			gotOutput, _ = m["output"].(string)
			gotResult = m["result"]
			return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: fmt.Sprintf("%v", gotResult)}}}, nil
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agent := ai.NewAgent[struct{}, string](model)
	res, err := agent.Run(ctx, "compute", struct{}{}, ai.WithRunCapabilities(New(WithRemote(cfg))))
	if err != nil {
		t.Fatalf("agent.Run against remote worker: %v", err)
	}
	if gotOutput != "remote-ok\n" {
		t.Fatalf("remote output = %q, want \"remote-ok\\n\"", gotOutput)
	}
	if gotResult != int64(5525) {
		t.Fatalf("remote result = %#v (%T), want int64(5525)", gotResult, gotResult)
	}
	if res.Output != "5525" {
		t.Fatalf("agent Output = %q, want \"5525\"", res.Output)
	}
}
