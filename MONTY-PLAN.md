# MVP: Sandboxed Code Execution via Monty (gomonty)

## Status: implemented (2026-09-05)

Built as designed below, with one deliberate deviation from the original
dependency plan: pinned to the fork's
`mdfranz/upstream-refresh-monty-33556ba` branch (commit `2a387f1`) instead of
the plain-fork `v0.0.14` tag. See "Dependency / build risk" for what that
branch changes and the new risk it introduces (native libs rebuilt for only
one of six platforms). `codemode/codemode.go` and `codemode/flatten.go`
(split out during implementation — see "Result composition") plus
`codemode/codemode_test.go` are in place; `cmd/sparktea/chat.go` has the
`/code` toggle. `go build ./...`, `go vet ./...`, and
`CGO_ENABLED=0 go test ./codemode/...` all pass.

## Context

Upstream PydanticAI (Python) has a feature called **Code Mode**: instead of the
model making one native tool call at a time, the agent exposes a single
`run_code` tool. The model writes a Python script that calls real tools as
plain functions (loops, conditionals, `asyncio.gather`) in one round trip.
The script runs inside **Monty** (https://github.com/pydantic/monty), a
sandboxed Python interpreter written in Rust with no filesystem/network/env
access except what's explicitly passed in — no Docker, ~5ms to start a
sandbox. **gomonty** (https://github.com/ewhauser/gomonty) is a Go binding
for Monty (cgo-free, via `purego`, bundled native libs per platform). We
depend on a personal fork, https://github.com/mdfranz/gomonty (a plain fork
as of 2026-09-05, 0 commits ahead of upstream), pinned via a `replace`
directive — see "Dependency / build risk" below for why and how.

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
        Print:  monty.WriterPrintCallback(&stdout), // PrintCallback is func(stream, text string); this helper ignores stream
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

`monty.Value` doesn't give a one-call path to plain JSON: `.MarshalJSON()`
always wraps scalars in a `{"kind":"int","value":42}` envelope, and `.Raw()`
on a `list`/`dict` returns `[]Value`/`Dict` — still-wrapped elements, not
native Go types — so marshaling that re-triggers the same envelope on every
element. `codemode.go` needs a small recursive unwrapper,
`flattenValue(v monty.Value) any`, that switches on `v.Kind()`:
- scalars (`none`, `bool`, `int`, `big_int`, `float`, `string`, `bytes`) →
  return `v.Raw()` directly — already a plain Go type for these kinds.
- `list`/`tuple`/`set` → type-assert `v.Raw()` to `[]Value` and map each
  element through `flattenValue` into a `[]any`.
- `dict`/`named_tuple` → type-assert to `Dict`/its pairs and map each
  `Pair.Value` through `flattenValue` into a `map[string]any`; dict keys
  aren't guaranteed to be strings, so a non-string-key dict needs a fallback
  shape, e.g. `[[key, value], ...]`, rather than a Go map.
- anything else (dataclass, stat-result, function, exception, date/time
  types) → fall back to `v.String()` for the MVP rather than modeling every
  payload shape.

Verify this against a real `dict`/`list`-returning script during
implementation, not just `"40 + 2"` — that's the one case upstream's own
README example exercises, and it hides this issue because a scalar's
`.Raw()` is already a plain value.

Composition rules (unchanged):
- Final expression non-`None` → return `flattenValue(value)`.
- Final statement is an assignment / value is `None` → return `{}`.
- `print()` output captured, non-empty → wrap as `{"output": "<text>"}`.
- Both print output and a non-`None` value →
  `{"output": ..., "result": flattenValue(value)}`.

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

- We depend on our fork, not upstream directly, and — a deliberate change
  from this plan's original draft — on an **unreleased, untagged branch**
  of that fork rather than the plain `v0.0.14` tag:

  ```
  require github.com/ewhauser/gomonty v0.0.14
  replace github.com/ewhauser/gomonty => github.com/mdfranz/gomonty v0.0.0-20260905152908-2a387f1a0338
  ```

  The pseudo-version encodes commit `2a387f1a0338fdbbfeae30453d16a4048b4d0726`
  on branch `mdfranz/upstream-refresh-monty-33556ba` — Go doesn't accept a
  raw commit hash in a `replace` version field, so resolve it once (`go mod
  edit -replace github.com/ewhauser/gomonty=github.com/mdfranz/gomonty`,
  then `go mod download github.com/ewhauser/gomonty` with `GOPROXY=direct
  GOSUMDB=off` if the target commit isn't yet cached by proxy.golang.org)
  and let Go write the exact pseudo-version into `go.mod`/`go.sum` rather
  than typing one by hand. The fork's `go.mod` still declares `module
  github.com/ewhauser/gomonty`, unchanged from upstream — that's *why* the
  `replace` directive works with no import-path changes in `codemode.go`.
- Rationale for the fork itself is unchanged from the original plan:
  upstream is explicitly experimental and may lag Monty's own Rust-side
  development, so the fork gives us a place to carry a patch without
  waiting on an upstream PR merge.
- **Why this branch specifically, not the tag**: it's exactly the
  version-drift remediation this plan originally deferred (see below) — the
  fork's own `UPSTREAM-REFRESH-PLAN.md` on that branch bumps the pinned
  Monty rev from `c9802b5` to `33556ba` (2026-09-05) and updates the Go
  bindings for the wire-format changes that pin bump requires. It is 4
  commits ahead of the `v0.0.14` tag, committed but — per that file's own
  status note — not yet merged, tagged, or released; consuming it means
  consuming a branch tip, not a stable release.
- When the branch is superseded (merged, tagged, or upstream Monty moves
  again): re-resolve the pseudo-version the same way and bump the
  `replace` line. No changes needed in `codemode.go` unless the new commit
  changes the public Go API surface described below.

**API surface actually consumed (verified against this branch, not just
inferred from gomonty's README)**:
- `monty.ValueKind`'s specific values (`valueKindNone`, `valueKindList`, …)
  are **unexported** — `codemode`'s `flattenValue` switches on `v.Kind()`
  against untyped string literals (`"none"`, `"list"`, `"dict"`, …) rather
  than named constants, since none are exported to switch on directly.
- `monty.ResourceLimits` on this branch is `{MaxDuration, MaxMemory,
  GCInterval, MaxRecursionDepth, MaxSuspensions}` — no `MaxAllocations`
  (upstream removed it; the original plan didn't reference it, so no
  impact). Added `MaxSuspensions` to `codemode`'s defaults as a backstop
  even though the MVP sets `RunOptions.Functions` to nil (nothing for a
  script to suspend on yet).
- Four new `ValueKind`s exist beyond what the original plan enumerated:
  `not_implemented`, `time` (`datetime.time`), `class_instance` (replaces
  the pre-refresh `dataclass` kind), `file_handle` (result of `open()`).
  All four fall through `flattenValue`'s `default:` case to `v.String()`,
  same as any other not-explicitly-modeled kind — no special-casing needed
  for the MVP's value surface (`int`/`float`/`str`/`bool`/`None`/`list`/
  `dict`), which is unaffected by any of this branch's changes.
- `monty.New(code, monty.CompileOptions{ScriptName: ...})` and
  `Runner.Run(ctx, monty.RunOptions{Print, Limits})` match the original
  plan's sketch exactly — confirmed against this branch's own
  `example_test.go`, unchanged from upstream's `README.md` example.

**New risk introduced by using this branch (not present with the `v0.0.14`
tag)**: the branch's own completion record flags that only the **macOS
arm64** prebuilt native library
(`internal/ffi/lib/darwin_arm64/libmonty_go_ffi.dylib`) was rebuilt against
the new Rust wire format and committed; the other five
(`linux_amd64`/`linux_arm64` glibc+musl, `windows_amd64`) are still the
**stale, pre-refresh** binaries — mismatched with the branch's Go-side wire
code. Practical read:
- sparktea's stated dev/build target for this MVP is macOS arm64 (see
  Verification below), which is the one platform this branch is
  internally consistent on — fine for that target today.
- Building sparktea for any other platform against this branch would load a
  native library that doesn't speak the wire format the Go bindings emit.
  Whether that fails loudly (symbol/version mismatch at load time) or
  silently misdecodes hasn't been tested here — treat any non-macOS-arm64
  build as unverified until the fork's `make release` step rebuilds all six
  platform libraries (tracked on the fork, not in this plan).
- This is strictly worse cross-platform coverage than the `v0.0.14` tag
  (all six libs consistent with each other, just older) — an explicit
  trade made here for a resolved version-drift risk on the one platform
  this MVP is being built and verified on.

**The version drift this branch resolves (context for why the tradeoff was
worth making)**: `gomonty` `v0.0.14` pinned upstream Monty at rev `c9802b5`
(2026-03-28); `pydantic/monty` `main` had moved 347 commits (about 5 months)
ahead of that by 2026-09-05. `gomonty`'s own `.agents/skills/
upstream-refresh` runbook flags any change to `object.rs`, `run_progress.rs`,
or the `convert.rs` converters (new `MontyObject` variants, changed fields)
as FFI-affecting, requiring the Rust wire format
(`crates/monty-go-ffi/src/wire.rs`) and the Go types (`wire.go`/`types.go`)
to move in lockstep. The `mdfranz/upstream-refresh-monty-33556ba` branch is
that lockstep update, done and committed (see the branch's
`UPSTREAM-REFRESH-PLAN.md` for the full narrative: crate restructuring, a
resource-tracker redesign, `max_allocations` removal, four new value kinds,
`OsCall`/`FunctionCall` reshaping, and a known unrelated gap in
dataclass-method dispatch that predates this refresh and doesn't affect the
MVP since it registers no host callbacks).
- go.mod already declares `go 1.27.0` (gomonty needs 1.25+), fine.
- gomonty is explicitly experimental ("Hack Monty Round 3"-flavored). The
  package is cgo-free by construction (loads its native library via
  `purego`), so it builds whether or not `CGO_ENABLED` is set.
- No `go.sum`-breaking concerns beyond the pseudo-version resolution above —
  it's a normal Go module, just with embedded binaries (module zip ~40MB
  uncompressed).

### Testing

Unit tests in `codemode/codemode_test.go` (no HTTP/VCR fixtures needed,
since Monty is fully local/deterministic):
- `"40 + 2"` → result `42`.
- A script returning a list/dict (e.g. `[1, 2, {"a": 1}]`) → flattened to
  plain JSON via `flattenValue`, not `{"kind": ..., "value": ...}`
  envelopes — the case `"40 + 2"` doesn't exercise.
- A `print("hi")`-only script → `{"output": "hi"}`.
- `flattenValue`'s `default:` branch (unrecognized `ValueKind`, covering a
  future upstream Monty type gomonty's wire format doesn't decode yet) is
  exercised, even if only by unit-testing the branch directly — see the
  Monty↔gomonty version drift note above.
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
 
- `codemode/codemode.go`, `codemode/flatten.go`, `codemode/codemode_test.go`
  (all new)
- `go.mod` / `go.sum` — add `github.com/ewhauser/gomonty` plus a `replace`
  directive pinning it to our fork, `github.com/mdfranz/gomonty`
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
