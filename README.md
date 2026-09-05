# sparktea

A terminal chat UI, built with [bubbletea](https://github.com/charmbracelet/bubbletea),
for talking to any model [**pydantic-ai-go**](https://github.com/Kludex/pydantic-ai-go)
supports — currently OpenRouter, Google Gemini, Anthropic, and Mistral, with
more just an import away.

Launch it, pick a model, and chat. Responses stream in token-by-token,
thinking traces render when a model exposes them, and conversation history
survives switching models — even across providers — mid-chat. Finished
answers render as markdown (headers, lists, syntax-highlighted code) via
[glamour](https://github.com/charmbracelet/glamour); the input box is a
multi-line [textarea](https://github.com/charmbracelet/bubbles) that grows
with pasted or wrapped text.

## Built on pydantic-ai-go

sparktea is a thin terminal skin over
[**pydantic-ai-go**](https://github.com/Kludex/pydantic-ai-go), a Go-native,
idiomatic port of [PydanticAI](https://ai.pydantic.dev/) by
[Marcelo Trylesinski (**@Kludex**)](https://github.com/Kludex). Nearly
everything sparktea does is pydantic-ai-go doing the real work; this repo
just wires it up to a terminal:



pydantic-ai-go is MIT-licensed and under active development, with a much
larger surface than sparktea touches — tool calling, structured output,
MCP servers, multimodal input, durable execution, A2A, and its own
reference terminal and web chat UIs. If sparktea is useful to you, go look
at [the project itself](https://github.com/Kludex/pydantic-ai-go); the
credit for what makes this app work belongs there.

## Setup

Set whichever provider keys you have; the startup picker only offers models
for providers with a key present.

```console
export OPENROUTER_API_KEY="sk-or-..."
export GEMINI_API_KEY="..."   # or GOOGLE_API_KEY
export ANTHROPIC_API_KEY="sk-ant-..."
export MISTRAL_API_KEY="..."
go run ./cmd/sparktea
```

Keys:
- `OPENROUTER_API_KEY` — required for OpenRouter models.
- `GEMINI_API_KEY` or `GOOGLE_API_KEY` — required for Gemini models.
- `ANTHROPIC_API_KEY` — required for direct Anthropic (Claude) models.
- `MISTRAL_API_KEY` — required for Mistral models.

## Building

`go run ./cmd/sparktea` is fine for iterating, but a `Makefile` is included for a real
binary:

```console
make build    # builds ./sparktea in the current directory
make install  # builds, then installs to ~/.local/bin/sparktea
make clean    # removes ./sparktea
```

`make install` expects `~/.local/bin` to be on your `PATH`.

## Usage

- `↑`/`↓` (or `j`/`k`) to move through the model list, `enter` to select.
- Type a message, `enter` to send; `ctrl+j` inserts a newline for
  multi-line/pasted prompts (the input box grows with the text, up to 6
  lines). Reasoning models' thinking traces render dimmed above the answer
  when the provider exposes them; finished answers render as markdown.
- `esc`, `ctrl+c`, or `ctrl+d` (on an empty input line) to quit. Mouse-wheel
  scrolling works in the transcript, at the cost of the terminal's own
  click-drag text selection while sparktea is running.

### Commands

Type these instead of a message:

| Command | Effect |
| --- | --- |
| `/model` | Reopen the model picker mid-conversation; history carries over, even across providers. |
| `/usage` | Show session totals: requests, input/output tokens, tool calls, cost. |
| `/clear` | Discard history and usage totals; start fresh without restarting. |
| `/search` (or `/search on`/`off`) | Toggle native web search grounding (`ai.WebSearchTool`) for models that support it. OpenRouter, Gemini, and Anthropic models pick it up (or silently skip it if the underlying model doesn't do web search); Mistral's adapter doesn't implement pydantic-ai-go's native-tool interface at all, so sparktea leaves the tool out of the request entirely rather than send something the transport would reject — `/search on` on a Mistral model just notes that it's a no-op. |
| `/code` (or `/code on`/`off`) | Toggle Code Mode: gives the model a `run_code` tool that executes Python in a sandbox. Off by default. See "Code Mode" below. |
| `/save [name]` | Write the conversation to `~/.sparktea/sessions/<name>.json` (default name `default`). |
| `/load [name]` | Restore a saved conversation, replaying its transcript and history. |

## Scripting (non-interactive mode)

Pass `-prompt` to run a single turn to completion and exit, skipping the
TUI — useful for testing (Code Mode especially) without hand-typing into
the chat screen:

```console
sparktea -list-models
sparktea -model anthropic:claude-haiku-4-5-20251001 -code \
  -prompt "Use run_code to compute the 20th Fibonacci number."
```

- `-model` takes a model ID (e.g. `claude-haiku-4-5-20251001`), not the
  display label — `-list-models` prints available options as
  `provider:model_id` (tab-separated from the label). A bare model ID or an
  unambiguous substring works too; use the `provider:model_id` form to pin
  one exactly when a bare ID could match more than one provider's catalog
  entry (a real possibility as more providers are added — model IDs aren't
  unique across them). Omitted, `-model` defaults to the first available
  model.
- `-code` / `-search` enable Code Mode / native web search for that one run,
  same as `/code` and `/search` in the TUI.
- The model's answer streams to stdout; thinking, tool calls (including the
  exact `run_code` argument and result), and a final usage line go to
  stderr — redirect it away (`2>/dev/null`) for just the answer, or capture
  it separately to see what a script actually ran.

`./test_sparktea.sh` exercises this CLI end to end: flag parsing and error
paths always run; a live prompt and a live Code Mode `run_code` call run too
if at least one provider API key is set (skipped otherwise). Set
`SPARKTEA_TEST_LIVE=0` to skip the live checks even with a key present, or
`SPARKTEA_TEST_MODEL=provider:model_id` to pin which model they use.

## Local logs

sparktea always writes operational diagnostics to
`~/.sparktea/logs/sparktea-YYYY-MM-DD.jsonl`, one JSON object per line and
one file per local calendar day. Log files and their directory are private to
your user account (`0600` and `0700`, respectively) and are retained until
you remove them.

The logs record lifecycle events, selected provider/model, enabled modes,
usage, tool names/outcomes, and error types. They never include prompts,
responses, thinking, tool inputs/results, session identifiers, or credentials.
For example, filter today's events with:

```console
jq -r '.msg' ~/.sparktea/logs/sparktea-$(date +%F).jsonl
```

## Observability (Logfire)

Set `LOGFIRE_TOKEN` to send OpenTelemetry traces and metrics to
[Logfire](https://pydantic.dev/logfire), Pydantic's own observability
product — the run's agent spans, model requests, token usage, and cost, one
trace per turn. Native (provider-executed) tool calls — web search, code
execution — get their own child span too, since the underlying library only
ever folds those into the chat span's message history otherwise.

```console
export LOGFIRE_TOKEN="your-write-token"
go run ./cmd/sparktea
```

That's it — no other env vars needed. By default it targets Logfire's US
region (`logfire-us.pydantic.dev`); set `LOGFIRE_ENDPOINT` for the EU region
or a self-hosted collector. `OTEL_SERVICE_NAME` overrides the reported
service name (default `sparktea`).

Prompts, completions, and full request parameters are **not** sent by
default, since this telemetry leaves your machine — only trace structure,
token counts, and cost. Set `LOGFIRE_SEND_CONTENT=1` to include them once
you trust the destination project with conversation content.

Without `LOGFIRE_TOKEN` set, none of this runs — no OTel providers are
installed and agents behave exactly as before.

## Code Mode

`/code` (default off) gives the model a single `run_code` tool: it writes
Python, sparktea runs it, the model gets the result back. This is
[**Monty**](https://github.com/pydantic/monty), a sandboxed Python
interpreter written in Rust, via its Go bindings,
[**gomonty**](https://github.com/ewhauser/gomonty) — no Docker, no
subprocess, a few milliseconds to start.

Sandboxing guarantees:
- No filesystem, network, or environment access. `os` and `pathlib` import
  fine, but any real OS call (`os.getenv`, `Path.exists`, file I/O, ...)
  raises `NotImplementedError` — there's nothing to touch.
- Only part of the stdlib exists, each module covering a slice of CPython's
  surface: `sys`, `typing`, `math`, `json`, `re`, `unicodedata`, `datetime`,
  `pathlib`, `os`, `collections`, `itertools`, `functools`, `dataclasses`,
  `asyncio`, `base64`, `binascii`. No third-party imports. Notably **not**
  available: `statistics`, `random`, `time`, `enum`, `copy`, `string`, `io`,
  `struct`, `hashlib`, `uuid`, and anything network/process/thread-related
  (`urllib`, `socket`, `subprocess`, `threading`).
- Also unsupported: class inheritance, `@classmethod`/`@staticmethod`/
  `@property`, user-defined exception classes, `eval`/`exec`, `yield`.
  `%`-style string formatting (`"%.2f" % x`) fails — f-strings and
  `.format()` both work.
- Each run is capped at 5 seconds wall-clock, 64 MiB memory, and 100 stack
  frames of recursion, so a runaway script can't stall the TUI.

(`codemode/codemode_test.go` pins the module/formatting behavior above down
as regression tests against the exact pinned Monty commit, rather than
trusting [Monty's own limitations doc](https://pydantic.dev/docs/monty/limitations/)
blindly — that page and this pin already disagree on one point: `.format()`
works here even though the doc says it doesn't.)

The last expression's value comes back automatically (no `print()` needed);
`print()` output is captured too. A syntax error, a runtime exception, or a
resource-limit violation comes back to the model as a bounded retry prompt
(`*ai.RetryError`, capped at 20 cumulative failures for the run) rather than
a hard failure, so it can see what went wrong and retry with corrected
code — without a broken-code loop running forever.

**Current limitation**: the sandbox is self-contained — the model can't yet
call sparktea's own tools (e.g. web search) as functions from inside a
script. That's a natural next step once sparktea has more tools worth
composing; it needs no rework of the current implementation.

sparktea depends on a personal fork of gomonty
(`github.com/mdfranz/gomonty`), pinned via a `go.mod` `replace` directive,
to carry Monty-version-refresh work ahead of upstream releasing it — see
`MONTY-PLAN.md` for the details and the risk that comes with it (native
libraries are currently only rebuilt/verified for macOS arm64).

## Adding models

The startup list is a static catalog in `models.go`. Add an entry there —
`{label, provider, modelID}` — to offer another OpenRouter, Gemini,
Anthropic, or Mistral model ID. `~`-prefixed OpenRouter IDs are OpenRouter's
alias syntax (e.g. `~deepseek/deepseek-v4-flash-latest` always redirects to
the newest snapshot in that family).

Adding a whole new provider is just as thin: pick one of pydantic-ai-go's
other [model packages](https://github.com/Kludex/pydantic-ai-go/tree/main/ai/models)
(`openai`, `bedrock`, `groq`, `xai`, `cohere`, `ollama`, ...), add a
`provider` constant, a case in `newModel()`, and a case in
`apiKeyPresent()` (`models.go`), and list it in `modelCatalog`. If the new
provider's pydantic-ai-go adapter implements `NativeToolSupportModel` (most
do), also add it to `supportsNativeWebSearch()` so `/search` picks it up —
otherwise leave it out, since an adapter without that interface has any
`ai.NativeTool` rejected outright, `Optional: true` or not.

## Updating pydantic-ai-go

`pydantic-ai-go` is pulled from its public GitHub repo as a normal Go module
dependency (no tagged releases yet, so `go.mod` pins a pseudo-version off the
latest commit on `main`). To pick up upstream changes:

```console
go get github.com/Kludex/pydantic-ai-go/ai@main \
  github.com/Kludex/pydantic-ai-go/ai/models/openrouter@main \
  github.com/Kludex/pydantic-ai-go/ai/models/google@main \
  github.com/Kludex/pydantic-ai-go/ai/models/anthropic@main \
  github.com/Kludex/pydantic-ai-go/ai/models/mistral@main
go mod tidy
```

See [pydantic-ai-go's docs](https://github.com/Kludex/pydantic-ai-go/tree/main/docs)
for everything the library can do beyond what sparktea currently exposes.
