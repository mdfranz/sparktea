# MVP: Sandboxed Code Execution via Monty (gomonty)

## Context

Upstream PydanticAI (Python) has a feature called **Code Mode**: instead of the
model making one native tool call at a time, the agent exposes a single
`run_code` tool. The model writes a Python script that calls real tools as
plain functions (loops, conditionals, `asyncio.gather`) in one round trip.
The script runs inside **Monty** (https://github.com/pydantic/monty), a
sandboxed Python interpreter written in Rust with no filesystem/network/env
access except what's explicitly passed in — no Docker, ~5ms to start a
sandbox. **gomonty** (https://github.com/ewhauser/gomonty) is a Go binding
for Monty (cgo-free, via `purego`, bundled native libs per platform).

sparktea is a thin bubbletea TUI over `github.com/Kludex/pydantic-ai-go`
(pydantic-ai-go), which has **no existing Monty/Code Mode support at all** —
confirmed via a repo-wide grep, zero hits. This would be new, from-scratch
work, done entirely in sparktea (no need to fork/PR pydantic-ai-go).

**Important correction from the initial framing**: Monty is *not* an LLM
model/provider — it's a tool-execution sandbox. It does not fit sparktea's
existing `models.go` provider-catalog pattern (that pattern is for `ai.Model`
implementations talking to inference APIs). Instead it plugs into
pydantic-ai-go's separate **`ai.Capability`** extension point, the same
mechanism sparktea already uses for Logfire instrumentation
(`ai.NewInstrumentation` in `logfire.go`).

**MVP scope decision**: sparktea currently registers **zero** custom function
tools (only the native web-search tool). So the MVP is a **self-contained
sandboxed code-execution tool** — the model can write and run Python for
math/string/data-wrangling tasks, with no host callbacks. This mirrors
`mcp-run-python` (a well-known sibling Pydantic project) and is independently
useful. Wiring real Go tool calls into the sandbox as callables ("full Code
Mode") is a natural fast-follow once sparktea has other tools worth
composing — it needs no rework of this MVP, just adding entries to the
callback registry described below.

## Design

### New package: `codemode/`

sparktea's CLI entry point and TUI are in `cmd/sparktea/`.
`codemode/` is the project's library package for sandboxed execution —
a self-contained unit that wraps the external dependency and keeps
`gomonty`'s package name `monty` cleanly isolated from `cmd/sparktea`.

`codemode/codemode.go`:

```go
package codemode

import (
    monty "github.com/ewhauser/gomonty"
    "github.com/Kludex/pydantic-ai-go/ai"
)

type CodeMode struct {
    limits *monty.ResourceLimits
}

type Option func(*CodeMode)

func WithLimits(l monty.ResourceLimits) Option { ... }

func New(opts ...Option) *CodeMode {
    c := &CodeMode{limits: defaultLimits()} // see Resource limits below
    for _, o := range opts { o(c) }
    return c
}

// Setup implements ai.Capability.
func (c *CodeMode) Setup(reg *ai.CapabilityRegistry) error {
    reg.AddTool(runCodeDefinition(), c.handleRunCode)
    return nil
}

func (c *CodeMode) handleRunCode(ctx context.Context, rawArgs json.RawMessage) (any, error) {
    var args struct{ Code string `json:"code"` }
    if err := json.Unmarshal(rawArgs, &args); err != nil { return nil, err }

    runner, err := monty.New(args.Code, monty.CompileOptions{ScriptName: "run_code.py"})
    if err != nil {
        return formatMontyError(err), nil // syntax error -> feed back to model, don't hard-fail the run
    }

    var stdout strings.Builder
    value, err := runner.Run(ctx, monty.RunOptions{
        Print:  func(s string) { stdout.WriteString(s) },
        Limits: c.limits,
        // Functions: nil in the MVP — no host callbacks yet.
    })
    if err != nil {
        return formatMontyError(err), nil // runtime error -> feed back to model
    }
    return composeResult(stdout.String(), value), nil
}
```

Registered per-run in `cmd/sparktea/chat.go`'s `startStream()` via `ai.WithRunCapabilities(...)`:

```go
if m.codeEnabled {
    runOpts = append(runOpts, ai.WithRunCapabilities(m.codeModeCapability))
}
```

`ai.Capability.Setup` is purely *additive* (confirmed by exploration) — it
can only add tools/instructions/settings, never hide existing ones — which
is exactly what's needed here since there's nothing to hide. Instrumentation
already pins itself outermost via `CapabilityOrderingProvider`, so it will
transparently trace the single `run_code` call with no changes on its side.

### Tool definition (`run_code`)

One string parameter (`code`), JSON-schema-described, with a description
explaining: write Python, the last expression's value is returned
automatically (no need to `print()`), only a small stdlib subset is
available (`sys, typing, math, json, re, unicodedata, datetime, pathlib`),
no imports of third-party packages, no filesystem/network/env access.

### Result composition (mirrors upstream Code Mode semantics)

- Final expression non-`None` → return the value (JSON-marshaled via
  `monty.Value.MarshalJSON`/`.Raw()`).
- Final statement is an assignment / value is `None` → return `{}`.
- `print()` output captured, non-empty → wrap as `{"output": "<text>"}`.
- Both print output and a non-`None` value → `{"output": ..., "result": ...}`.

### Error handling

`gomonty` surfaces three typed errors: `*monty.SyntaxError`,
`*monty.RuntimeError` (has `.Frames` for a traceback), `*monty.TypingError`.
For the MVP, all three are formatted into a message and returned as the
tool's **successful** result content (not a Go `error`) so the model sees
what went wrong and can retry by writing corrected code in a follow-up
`run_code` call, without the framework treating it as a hard run failure.
*(Verify during implementation whether pydantic-ai-go's tool-execution-error
path already does something equivalent/better — e.g. a retry-prompt
conversion on returned `error` — and prefer that if so; otherwise the
explicit "return error text as content" approach above is the fallback.)*

### Resource limits (defaults, MVP)

Conservative defaults so a runaway script can't stall the TUI:
`MaxDuration: 5s`, `MaxMemory: 64 MiB`, `MaxRecursionDepth: 100`. Exposed via
`codemode.WithLimits(...)` for later tuning; not user-configurable in the
MVP.

### Enabling it in the TUI

Mirrors the existing `/search` toggle: add a `/code` slash command in
`cmd/sparktea/chat.go` that toggles `m.codeEnabled`. When enabled, `startStream` appends
`ai.WithRunCapabilities(m.codeModeCapability)` to `runOpts`.
**Default: off** — this is new, experimental, and changes what the model can do;
opt-in avoids surprising existing sessions. Since Code Mode needs no API key
and works identically regardless of which model/provider is active, no changes
to `cmd/sparktea/models.go`'s provider catalog, `apiKeyPresent()`, or picker are needed at all.

### Dependency / build risk (call out explicitly, not blocking)

- `go get github.com/ewhauser/gomonty@latest` — go.mod already declares
  `go 1.27.0` (gomonty needs 1.25+), fine.
- gomonty is explicitly experimental/unreleased-feeling ("Hack Monty Round
  3"), bundles prebuilt native shared libraries per platform/arch inside the
  module (macOS arm64, Linux x86-64/arm64 glibc+musl, Windows x86-64) loaded
  via `purego`, requires `CGO_ENABLED=0`. sparktea's `Makefile` does a plain
  native `go build`/`go install`, so this should work out of the box for the
  common desktop platforms; cross-compiling to a platform without a bundled
  lib would need `scripts/build-go-ffi.sh`-style local rebuild — out of
  scope for the MVP, note as a known limitation.
- No `go.sum`-breaking concerns expected (it's a normal Go module, just with
  embedded binaries), but pin an exact version rather than tracking `main`.

### Testing

Unit tests in `codemode/codemode_test.go` (no HTTP/VCR fixtures needed,
since Monty is fully local/deterministic):
- `"40 + 2"` → result `42`.
- A `print("hi")`-only script → `{"output": "hi"}`.
- A script exceeding `MaxDuration`/`MaxMemory` → error surfaced as content,
  run doesn't hang.
- A syntax error → formatted message returned, no Go-level panic/error.
- A script attempting a disallowed import (e.g. `import requests`) →
  rejected cleanly.

### Docs

Per `AGENTS.md` convention (keep README current each commit): add a new
**"Code Mode"** section to `README.md` describing what `/code` does, its
sandboxing guarantees, and current limitation (no host tool callbacks yet).

## Files touched
 
- `codemode/codemode.go` (new), `codemode/codemode_test.go` (new)
- `go.mod` / `go.sum` — add `github.com/ewhauser/gomonty`
- `cmd/sparktea/chat.go` — add `/code` toggle command, pass `ai.WithRunCapabilities`, handle `ai.ToolCallPart` events in stream for UI feedback
- `README.md` — new "Code Mode" section

## Verification

1. `go build ./...` succeeds on the dev machine (macOS arm64) with
   `CGO_ENABLED=0`.
2. `go test ./codemode/...` passes the cases listed above.
3. Manual TUI check: run sparktea, `/code` to enable, ask the model to do a
   multi-step calculation ("compute the 20th Fibonacci number") and confirm
   it uses `run_code` and returns the right answer; then ask it to try
   something disallowed (e.g. read a file) and confirm a clean sandboxed
   rejection rather than a crash.
4. Confirm Logfire traces (if `LOGFIRE_TOKEN` set) show the `run_code` tool
   call same as any other tool — no special-casing needed.
