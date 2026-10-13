# Code Mode implementation

Code Mode gives the model one tool, `run_code`: the model writes Python,
sparktea runs it, and the result goes back to the model. Two backends execute
the code:

- **Monty** (default): a sandboxed Python interpreter written in Rust,
  embedded through gomonty.
- **CPython** (`-code-backend cpython`, CLI only): local `python3`, run
  through pydantic-ai-go's local workspace. Not a sandbox.

This document describes how both are built on pydantic-ai-go. For usage, see
"Code Mode" in the [README](README.md#code-mode). For the original Monty
design, see [MONTY-PLAN.md](MONTY-PLAN.md). For a benchmark of the two
backends, see
[results/monty-vs-cpython.md](results/monty-vs-cpython.md).

Versions:
- pydantic-ai-go v0.5.0
- gomonty v0.0.18 (Monty v1.0.1)
- CPython: whatever `python3` is on `PATH` (3.11.2 in the benchmarks)

## Overview

```
sparktea turn (cmd/sparktea: runTurn / TUI stream loop)
 ├─ stall watchdog: cancels the turn after 120 s with no agent event
 └─ agent.StartRun(..., ai.WithRunCapabilities(codemode.New(...)))
     ├─ chat <model>                        model request (pydantic-ai-go)
     ├─ execute_tool run_code               tool span (pydantic-ai-go)
     │   └─ handler, by backend:
     │       ├─ monty:   monty.New → runner.Run   → span monty.run   (gomonty/otelmonty)
     │       └─ cpython: LocalWorkspaceBackend.Run → span python.run (sparktea)
     └─ chat <model>                        next request, with the tool result or retry prompt
```

Both backends register the same tool name and argument schema
(`{"code": string}`), so the model sees the same interface either way. They
differ in four places:
- the tool description;
- whether the Monty instruction is added;
- what executes the code;
- the interpreter span.

## pydantic-ai-go integration

### The capability

`codemode.CodeMode` implements `ai.Capability`, pydantic-ai-go's interface
for adding tools and instructions to an agent:

```go
type Capability interface {
	Setup(reg *CapabilityRegistry) error
}
```

`Setup` runs once before the run's first model request. CodeMode uses two
registry methods:

- **`reg.AddTool(def, fn)`** registers `run_code`.
  - `def` is an `ai.ToolDefinition`: name, description and JSON schema.
  - `fn` has the signature `func(ctx context.Context, rawArgs json.RawMessage) (any, error)`.
  - pydantic-ai-go turns the returned value into the tool result the model
    sees (JSON for maps, lists and numbers).
- **`reg.AddInstructions(text)`** adds the Monty instruction to the system
  prompt. Only the Monty backend calls it.

sparktea attaches the capability per run, not per agent:

```go
runOpts = append(runOpts, ai.WithRunCapabilities(codemode.New(...)))
```

That's why `/code` can be toggled between TUI turns without rebuilding the
agent, and why plain chats carry neither the tool nor the instruction.

### Errors and retries

pydantic-ai-go treats a tool's error in one of two ways:

| Returned error | Effect |
|---|---|
| `*ai.RetryError` (from `ai.Retryf`) | Becomes a `RetryPromptPart`. The message goes back to the model as corrective feedback, and the run continues. |
| Any other error | Stops the run. |

A failing script is normal in Code Mode, so both backends report every
script problem through `ai.Retryf`. That covers syntax errors, exceptions,
timeouts, memory limits and unsupported features. Plain errors are kept for
problems in sparktea itself, such as undecodable arguments or a failure to
start Python.

Retries draw on a per-tool budget, which counts failures across the whole run
and never resets. The default of 1 is far too tight for writing code, so the
tool definition raises it:

```go
ai.WithToolMaxRetries(runCodeMaxRetries)(&def) // 20
```

After 20 failed `run_code` calls in one run, pydantic-ai-go ends the run with
`ErrMaxRetriesExceeded`.

pydantic-ai-go also has `WithToolTimeout`, which turns a slow tool call into
a retry prompt. Code Mode doesn't use it: each backend enforces its own
5-second limit, and the CPython backend needs to know about a timeout itself
so it can record it on its span.

### Streaming and events

sparktea runs turns with `agent.StartRun(...)` and reads `run.Events()`. It
doesn't use `RunStream`, which ends a turn at the first text output; see
[ISSUES.md](ISSUES.md).

`run_code` shows up in the event stream as:
- `FunctionToolCallEvent`, with the fully assembled `code` argument;
- `FunctionToolResultEvent`, carrying either a `ToolReturnPart` (success) or a
  `RetryPromptPart` (failure).

From these, `-prompt` and `-script` print the `[tool call]`, `[tool result]`
and `[tool error]` lines on stderr, and `monty_codegen.sh` scores runs from
those lines.

Every event also resets the stall watchdog (`cmd/sparktea/watchdog.go`).
Tools finish well within its 120 s limit, because both backends stop a script
after 5 seconds.

### Telemetry

pydantic-ai-go instruments the run itself, producing these spans:
- `invoke_agent sparktea`;
- `chat <model>` for each model request, with the tool definitions, messages,
  usage and `operation.cost`;
- `execute_tool run_code`, with the call's arguments and result.

sparktea adds:
- **The `sparktea turn` span** at the root, with these attributes:
  - `sparktea.provider` and `sparktea.model`;
  - `sparktea.code_mode`;
  - `sparktea.code_backend`, when code mode is on;
  - `sparktea.web_search`.

  It also carries `logfire.tags`: `monty` or `cpython`, plus `web_search`
  when search is on.
- **An interpreter span** under `execute_tool run_code`: `monty.run` or
  `python.run`.

`LOGFIRE_SEND_CONTENT=0` removes script output from the interpreter spans.
The pydantic-ai-go spans follow pydantic-ai-go's own content settings.

## Monty backend

Code: `codemode/codemode.go` and `codemode/flatten.go`.

**Execution:**
1. `monty.New(code, monty.CompileOptions{ScriptName: "run_code.py"})`
   compiles the script. A syntax error becomes `ai.Retryf`.
2. `runner.Run(ctx, monty.RunOptions{...})` executes it in-process, with no
   subprocess:
   - `Print: monty.WriterPrintCallback(&stdout)` captures `print()` output;
   - `Limits`: 5 s wall clock, 64 MiB memory, 100 frames of recursion, 10
     suspensions;
   - `Telemetry` and `TelemetryOptions` connect gomonty's OpenTelemetry
     bridge (`otelmonty`), which emits `monty.run`.

   A runtime error or a limit violation becomes `ai.Retryf`, with Monty's own
   message, for example `NotImplementedError: ...` or `LookupError: unknown
   encoding: latin-1`.
3. **The result:**
   - The script's last expression comes back as a `monty.Value`.
   - `flattenValue` converts it to plain Go and JSON types. gomonty's own
     JSON form wraps every value as `{"kind": ..., "value": ...}`.
   - Kinds with no natural JSON form, such as dates or class instances, fall
     back to Monty's `repr`.

**Result shape**, shared by both backends (`composeResult`):

| Script produced | Tool result |
|---|---|
| output and a value | `{"output": "...", "result": value}` |
| output only | `{"output": "..."}` |
| a value only | `value` |
| neither | `{}` |

**What the model is told:**
- **Tool description:** about 2,200 characters, listing what Monty lacks:
  - missing modules (`statistics`, `enum`, `hashlib`, `heapq` and others);
  - missing builtins;
  - missing names in present modules, such as `dataclasses.field` and
    `functools.lru_cache`;
  - unsupported syntax: inheritance, decorators on methods, generators,
    `match`;
  - the exception-argument rule;
  - the 100-frame recursion limit.
- **System instruction:** about 320 characters, added through
  `AddInstructions`. It names the features models most often try anyway.

Every model request in a code-mode turn carries both, so they account for
most of Monty's extra input tokens in the benchmark.

**Properties:**
- Isolation comes from the interpreter itself: there's no filesystem,
  network or environment access, and OS calls raise `NotImplementedError`.
- Calls are fast: a typical `monty.run` takes under 1 ms.
- The language and standard library are narrower than CPython's, so models
  spend retries working around missing features.

## CPython backend

Code:
- `codemode/python.go`: the handler, result decoding and the `python.run`
  span;
- `codemode/python_runner.py`: the runner script, embedded with `go:embed`.

### pydantic-ai-go local workspace

The backend runs each call through pydantic-ai-go's workspace API rather than
calling `os/exec` directly:

```go
backend, _ := ai.NewLocalWorkspaceBackend(dir, map[string]string{
	"HOME":                dir,
	"SPARKTEA_MAX_MEMORY": "536870912",
})
result, err := backend.Run(ctx, ai.WorkspaceCommand{
	Args:    []string{"python3", "-I", "-c", pythonRunner},
	Timeout: 5 * time.Second,
})
```

`LocalWorkspaceBackend.Run` provides, per pydantic-ai-go's workspace
contract:

| Contract | What it gives the backend |
|---|---|
| Environment | Only `PATH`, `HOME`, `LANG`, `LC_ALL` and `LC_CTYPE` pass through, with constructor values taking precedence. Provider API keys and `LOGFIRE_TOKEN` never reach the script. |
| Timeout or cancellation | Kills the process group and returns an error wrapping `ai.ErrWorkspaceTimeout`. |
| Output | Captures stdout and stderr separately, replaces invalid UTF-8, and stops a command whose combined output exceeds 10 MiB. |
| Exit status | Returned as `CommandResult.ExitCode`, not as an error. A killed process reports 128 plus the signal number. |
| Input | Standard input starts at EOF. |

The backend is used on its own. It isn't attached to the agent as a workspace
capability (`ai.NewLocalWorkspace`), because Code Mode needs one command per
tool call, not a workspace the model can see. pydantic-ai-go's docs warn that
"a local workspace isolates nothing": the directory only sets where commands
start.

### Per-call steps

1. Create a fresh scratch directory (`os.MkdirTemp`) and write the code to
   `run_code.py` in it.
2. Run `python3 -I -c <runner>` there, with `HOME` set to the scratch
   directory. `-I` is isolated mode: it ignores `PYTHON*` environment
   variables and user site-packages, and doesn't add the working directory
   to the import path.
3. Delete the scratch directory afterwards, so nothing carries over between
   calls.

### The runner

`python_runner.py` makes CPython behave like the Monty backend, as far as the
model can tell:

1. **Memory cap:** sets `RLIMIT_AS` from `SPARKTEA_MAX_MEMORY` (512 MiB).
   CPython needs much more address space than Monty's 64 MiB heap just to
   start.
2. **Parsing:** parses `run_code.py` with `ast`. If the last statement is an
   expression, it's split off and evaluated separately, so its value comes
   back automatically, as it does in Monty.
3. **Running:** runs the script with `stdout` redirected to a buffer, so
   `print()` output is captured rather than mixed into the result.
4. **Success:** writes one JSON line, `{"output": ..., "value": ...,
   "exec_ms": ...}`. Values that JSON can't represent come back as `repr`
   strings, and tuples and sets become lists.
5. **Failure:** writes to stderr, and exits 1:
   - the exception line first (`ValueError: bad`), matching Monty's format;
   - then any `SyntaxError` location;
   - then a traceback limited to `run_code.py` frames;
   - then any output printed before the error.

   Errors from parsing are reported the same way, including `ValueError` for
   source containing NUL bytes.

### Turning results into what the model sees

| Outcome | Tool result |
|---|---|
| Exit 0 | The JSON line is decoded with `json.Decoder.UseNumber`, so large integers such as `2**100` keep every digit, and shaped like Monty's results. |
| Exit 1 | `ai.Retryf(stderr)` |
| `ai.ErrWorkspaceTimeout` | `ai.Retryf("TimeoutError: script exceeded 5s")` |
| Any other exit code | `ai.Retryf("RuntimeError: python exited with status N ...")` |
| Workspace error that isn't a timeout | A plain error, which stops the run |

### Tool description

About 570 characters. It says the code runs in CPython 3 with the full
standard library, that each call gets a fresh process and directory, and
that calls stop after 5 seconds. No system instruction is added.

### The `python.run` span

The span records:
- `python.script_name`, `python.interpreter` and `python.code_bytes`;
- `python.exit_code` and `python.timed_out`;
- `python.exec_ms`: the script's own run time. The span's duration minus this
  is roughly the process startup cost.
- with content on, `python.stderr` and `python.output`, each truncated to
  4 KiB.

A failure sets the span status to the exception line. With content off, the
status keeps only the exception type.

### Properties

- **Full language and standard library:** inheritance, `@property`,
  generators, `match`, `enum`, `hashlib`, `dataclasses.field` and the latin-1
  codec all work.
- **Not a sandbox:** scripts run as your user, with network access and the
  host filesystem reachable. The scratch directory and the filtered
  environment limit accidents, not attacks. Use this backend for
  benchmarking with prompts you trust.
- **Call time:** each call pays Python's startup cost, about 50 ms at the
  median.
- **Available packages:** only what's installed for `python3`. A model that
  tries `sympy` gets a `ModuleNotFoundError`.

## Side by side

| | Monty | CPython |
|---|---|---|
| Selected by | default; `/code` in the TUI | `-code-backend cpython` (CLI only) |
| Runs in | the sparktea process (gomonty) | a `python3 -I` subprocess, through `ai.LocalWorkspaceBackend` |
| Isolation | interpreter-level sandbox | none beyond a scratch directory and filtered environment |
| Language and standard library | Monty's subset | full CPython |
| Limits | 5 s, 64 MiB heap, 100-frame recursion | 5 s, 512 MiB address space, 1000-frame recursion |
| Typical call (median) | 0.4 ms | 51 ms (0.2 ms of it running the script) |
| Tool description | ~2,200 chars, plus a ~320-char instruction | ~570 chars, no instruction |
| Interpreter span | `monty.run` (otelmonty) | `python.run` (sparktea) |
| Errors back to the model | `ai.Retryf`, Monty's message | `ai.Retryf`, exception line first, traceback limited to the script |

The call timings come from 273 `monty.run` spans and 238 `python.run` spans
recorded on October 12, 2026.

## Python pydantic-ai comparison

[`pyrunner/monty_codegen.py`](pyrunner/monty_codegen.py) compares the same
cases through pydantic-ai 2.55.0 and pydantic-ai-harness 0.55.0. It has two
matched configurations and three upstream or alternative configurations:

- **`monty-stock`** uses harness `CodeMode()` defaults: 30 s, 256 MiB and
  1,000 suspensions, with three retries. It has a shorter tool description
  and no additional Monty instruction.
- **`monty-sparktea`** sets 5 s and 64 MiB, allows 20 retries, and shares
  sparktea's tool description and Monty instruction. This aligns the prompt
  and most limits, while leaving the harness behavior intact: its `run_code`
  accepts `restart` and keeps REPL state between calls. It also uses Monty
  1.1.0, while sparktea's gomonty embeds Monty 1.0.1. It is therefore a close
  comparison, not the same implementation.
- **`cpython-sparktea`** uses a custom stateless `run_code` tool and reuses
  [`python_runner.py`](codemode/python_runner.py), with the same description,
  trailing-expression behavior and 5 s timeout as sparktea. It runs a local
  `python3 -I` process with a 512 MiB address-space limit and filtered
  environment. The scratch directory does not make it a sandbox.
- **`shell`** runs harness `Shell` with a `LocalWorkspace` and allows Python
  commands. This is an upstream alternative: the model sees `run_command`,
  starts Python itself, and gets shell output and exit codes. It allows 20
  retries for disallowed commands; a non-zero exit is a normal result, which
  the runner logs as a tool error typed by the Python exception. The local
  workspace only sets a working directory and environment; it isolates
  nothing.
- **`shell-bwrap`** wraps that workspace in `BubblewrapSandbox`. Commands can
  write only in the scratch area and have no network, while host files remain
  readable. It requires `bwrap` and working unprivileged user namespaces.

Pydantic-ai's `LocalWorkspace` and pydantic-ai-go's `ai.NewLocalWorkspace`
have the same local-workspace model: each provides a working directory and
controlled environment but no security boundary. pydantic-ai-go has no
counterpart for harness `Shell` or `BubblewrapSandbox`. A Bubblewrap-wrapped
`WorkspaceBackend` could provide a comparable isolated CPython configuration
for sparktea as a follow-up.

The Python runner tags one `sparktea turn` root span per case with
`sparktea.implementation=python`. Its Logfire tags include the same broad
`monty` or `cpython` tags as sparktea, the selected runner backend (such as
`monty-sparktea` or `cpython-sparktea`), and `pydantic-ai`; Shell runs use
`shell` plus their selected backend. Its `chat <model>` spans provide request,
token and cost data. The local tool spans (`monty.run` and `python.run`) record
outcome and code size. `python.exec_ms` is interpreter time, as in sparktea;
`monty.call_ms` covers the whole harness tool call. Go's `monty.run` has no
timing attribute, so compare Monty spans by duration. Setting `LOGFIRE_SEND_CONTENT=0`
suppresses prompts, code and results while retaining the trace structure and
usage metadata; content is sent by default. The runner's default Logfire service is `sparktea-runner`,
separate from sparktea's `sparktea` service.

The default [`monty_cases.json`](monty_cases.json) is deliberately weighted
toward unsupported syntax and standard-library gaps. It measures how models
adapt to Monty, including retries and replacement code. The separate
[`monty_representative_cases.json`](monty_representative_cases.json) contains
portable workloads with short, idiomatic solutions in both Monty 1.1.0 and
CPython. Use the representative suite for end-to-end cost and latency
comparisons, and interpreter child spans for execution-speed comparisons.

## Known gaps

- The CPython backend isn't in the TUI; `/code` always uses Monty.
- Neither backend can call sparktea's other tools, such as web search, from
  inside a script.
- **pydantic-ai-go:** a parallel tool call with empty arguments fails to
  decode. pydantic-ai-go then sends it back to Anthropic with no `input`
  field, and Anthropic rejects the request with HTTP 400. Haiku 5.5 hit
  this once on October 12, 2026.
- **Retries:** provider rate limits (HTTP 429) and stalled streams end the
  turn; neither is retried (issue #11). The stall watchdog only makes a stall
  fail fast.
