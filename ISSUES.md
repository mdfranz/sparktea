# Upstream issues affecting sparktea

Tracking known bugs in dependencies that affect sparktea but are fixed
upstream, not here. Update the status line when an issue closes, and check
whether sparktea still needs to work around it. All four issues below are
fixed in pydantic-ai-go v0.4.0 (2026-09-14), which sparktea now uses. The
last section is not an upstream bug: it records a sparktea usage problem
found while investigating what first looked like one.

## pydantic-ai-go: thinking-block replay

Both found 2026-09-05 via the same live session: `claude-opus-5` produced
extended-thinking content across two successful turns; the next request
that replayed that history broke, two different ways depending on what
changed about the request. Confirmed via Logfire traces (exact request/response),
not just local logs (which never retain error bodies). Root-caused by
reading the pydantic-ai-go source at `v0.0.0-20260904230829-3c976cdd1116`;
both fixed upstream in v0.4.0.

### [#2](https://github.com/Kludex/pydantic-ai-go/issues/2) — Anthropic: replayed thinking block drops the required `thinking` field when empty

**Status: fixed in v0.4.0 via [PR #7](https://github.com/Kludex/pydantic-ai-go/pull/7), merged 2026-09-14.**
Verified live against the real Anthropic API via sparktea's new `-script`
(see README's "Scripting multi-turn sequences"): a turn with no tools
generated real thinking content, then `/search on` and another turn on that
history completed clean. Confirmed via Logfire's structural span attributes
(content itself is redacted) that the `{"type":"reasoning"}` part from turn
1 was actually present in turn 2's replayed input messages.

Root cause turned out simpler than first suspected: `anthropic.go`'s
`contentBlock.Thinking` was a plain `string` with `json:"thinking,omitempty"`.
Anthropic's default `display: "omitted"` returns thinking blocks with an
*empty* `thinking` field (the real reasoning lives only in the opaque
`signature`) — common, not an edge case — and `omitempty` dropped that empty
field from the request entirely. The Python reference (`pydantic-ai`'s
`anthropic.py`) always passes `thinking=response_part.content` as a keyword
argument, so it's always serialized even when empty; the Go port's
`omitempty` diverged from that.

Repro: a turn with no tools attached generates thinking content; a second
plain turn replays it fine; a third turn on the same history but with
`ai.WithRunNativeTools(...)` attached fails immediately with:

```
anthropic: API returned status 400: {"type":"error","error":{"type":"invalid_request_error","message":"messages.3.content.0.thinking.thinking: Field required"}}
```

**Impact on sparktea:** `/search on` (or any native tool) can break the very
next turn if history contains a prior Claude thinking block — not obviously
tied to search itself, so easy to misdiagnose as a search bug.

### [#3](https://github.com/Kludex/pydantic-ai-go/issues/3) — Google: `ai.ThinkingPart` missing cross-provider guard

**Status: fixed in v0.4.0 via [PR #6](https://github.com/Kludex/pydantic-ai-go/pull/6), merged 2026-09-14.**
Verified live against the real Gemini API via sparktea (Claude turn with
real thinking content, `/model` switch to `gemini-3.8-flash`, another turn
on the same history) — confirmed via local logs and Logfire traces.

`google.go:874-879` forwards any `ai.ThinkingPart` into a Gemini request
unconditionally. Compare `NativeToolCallPart`/`NativeToolReturnPart` a few
lines below in the same function, which both skip a part when
`rp.ProviderName != model.providerName` — `ThinkingPart` has no equivalent
check.

Repro: generate history against an Anthropic model (real thinking content
in a `ThinkingPart`), then `/model` switch to a Gemini model with that same
history — the next request fails immediately:

```
google: API returned status 400: {"error":{"code":400,"message":"Unsupported input part type: go/debugproto  \nthought: true\n","status":"INVALID_ARGUMENT"}}
```

**Impact on sparktea:** `/model` switching mid-conversation — one of
sparktea's headline features — can break the first turn on the new model if
the prior model left a thinking block in history.

## pydantic-ai-go: Anthropic reuses a code-execution container without the tool attached

Found 2026-09-06 via a live sparktea session: `/search on` on `claude-opus-5`
pulls in `code_execution` alongside `web_search` (creating a container),
then `/search off` broke the very next turn. Confirmed via Logfire traces.

### [#8](https://github.com/Kludex/pydantic-ai-go/issues/8) — Anthropic: code-execution container ID reused without the tool attached, 400s

**Status: fixed in v0.4.0 via [PR #9](https://github.com/Kludex/pydantic-ai-go/pull/9), merged 2026-09-14.**
Verified live against the real API via sparktea, confirmed via local logs
and Logfire.

`anthropicContainerFromHistory` reuses the container ID from any prior
Claude response found in history, with no check that the *current*
request's `NativeTools` actually includes `ai.CodeExecutionTool` — unlike
`hasAnthropicMemoryTool`'s equivalent check for the memory tool a few lines
away.

Repro: a turn with `ai.CodeExecutionTool` attached creates a container;
a second turn on the same history *without* the tool attached still sends
the stale container ID and fails immediately:

```
anthropic: API returned status 400: {"type":"error","error":{"type":"invalid_request_error","message":"container: Container identifier can only be provided when using the code execution tool"}}
```

**Impact on sparktea:** toggling `/search` (or `/code`) off after a turn
that used native code execution can break the very next turn — the same
"looks unrelated to what you just toggled" surprise as #2.

## pydantic-ai-go: OpenAI Responses stream doesn't recognize web-search progress events

Found 2026-09-05 via a live session testing sparktea's new OpenAI support.
Confirmed via Logfire traces.

### [#4](https://github.com/Kludex/pydantic-ai-go/issues/4) — OpenAI Responses stream: `response.web_search_call.*` progress events not recognized, crashes the run

**Status: closed upstream 2026-09-14 by [PR #20](https://github.com/Kludex/pydantic-ai-go/pull/20) (v0.4.0 release). Not yet re-verified in sparktea; `/search` is still disabled for OpenAI.**

`responses_stream.go:714-731`'s event-type switch hardcodes an allowlist of
provider progress events safe to ignore per tool family —
`response.code_interpreter_call.*`, `response.image_generation_call.*`,
`response.file_search_call.*`, `response.mcp_call.*`/`mcp_list_tools.*` —
but has no `response.web_search_call.*` entries at all. Any such event falls
through to the `default:` case and aborts the stream with `openai: unknown
Responses stream event type "response.web_search_call.in_progress"`.

Repro: `openai.NewResponsesModel(...)` with `ai.WithRunNativeTools(ai.WebSearchTool{...})`,
a prompt that actually triggers a web search call, streamed (not static)
generation. Fails immediately with the error above; the run is aborted, not
degraded. Unlike #2/#3, this needs no prior turn or history replay — the
*first* streamed native web search on an OpenAI model crashes the turn.

**Impact on sparktea:** `supportsNativeWebSearch()` in `models.go` excludes
`providerOpenAI` entirely as a result, so `/search` is currently unavailable
for OpenAI models (same treatment as Mistral, for an unrelated reason) until
this is fixed upstream.

**Pattern shared with #2/#3:** all three are the same shape of bug — a
hand-maintained enumeration of cases (part types, provider guards, event
types) that covers every variant except one. `ai.ThinkingPart` handling is
the common thread in #2/#3; here it's native-tool event-type coverage in
the Responses stream parser instead.

## Verifying a fix

`go get github.com/Kludex/pydantic-ai-go@<version> && go mod tidy` (see
README's "Updating pydantic-ai-go") and re-run the matching repro above.
#2, #3, and #8 needed no sparktea-side change — all three are in the
provider adapters' request serialization, not in how sparktea builds or
replays `m.history`. #4 does: once verified live on v0.4.0, add
`providerOpenAI` back to `supportsNativeWebSearch()` in `models.go`.

## sparktea: `RunStream` ends the run when text precedes a tool call (OPEN, sparktea-side)

Found 2026-10-10 with `monty_codegen.sh` on pydantic-ai-go v0.5.0. **Status:
cause confirmed; not a pydantic-ai-go bug. Fix belongs in sparktea and is not
applied yet.** An earlier version of this section called it an upstream bug;
that was wrong.

### Symptom

With `-code -prompt` and in the TUI, some models end a run right after the
first `run_code` call, whether it succeeded or failed. No follow-up request
carries the tool result back, no error is raised, and the final text is the
text the model emitted *before* the tool call (often just `"\n\n"`).
`monty_codegen.sh` reports these as `WRONG` with 0 failed calls.

### Measured (Logfire project `tomfoolery`, service `sparktea`, 2026-10-10)

For every `chat <model>` span whose finish reason was `tool_call`: does
`gen_ai.output.messages` have a `text` part before the tool call (a
`reasoning` part does not count), and is there a later `chat` span in the
same trace?

| Model | tool-call turns with text first | run ended after that call | tool-call turns without | run ended |
|---|---|---|---|---|
| deepseek-v4-pro-0813 | 26 / 48 | 26 / 26 | 22 | 0 |
| ~deepseek-v4-flash-latest | 25 / 111 | 25 / 25 | 86 | 0 |
| z-ai/glm-5.3-flash | 6 / 30 | 6 / 6 | 24 | 0 |
| qwen/qwen3.8-flash | 1 / 148 | 1 / 1 | 147 | 0 |

About 340 tool turns from one morning; "text first" is a regex over the
serialized attribute. Hand-checked traces: `a9fb204c47180ca64a9e189eab423c60`
and `fb4a7faff6da5d2ea0aeaab379d672d0` (Pro, text part exactly `"\n\n"`).
Qwen rarely emits text before a tool call, which is why it looked far more
reliable; the cross-model comparison from that day undercounts the others.

### Cause (confirmed by a deterministic test)

`Agent.RunStream` is deliberately a *stream-until-first-output* API, not a
streaming version of the full agent loop. It calls
`runStreamPrompt(..., commitFirstOutput=true)` (`ai/stream_run.go`), and
`streamedOutput` (`ai/stream_commit.go`) commits the first `TextPart` of a
response as the run's final output. Tool calls in the same response are still
executed and settled in history (default graceful end strategy), but the run
then returns instead of sending the results back to the model. This is
intended upstream: `ai/stream_commit_test.go`
`TestRunStreamCommitsTextBeforeToolProcessing` asserts exactly this (output
is the text, tools ran, three messages in history). The one thing upstream
could improve is the doc comment on `RunStream`, which says it "executes the
agent loop like Run", which is not true for this case.

A throwaway test (`fakes.FunctionModel` returning `[text, tool_call]` on the
first request and `"ANSWER: 42"` on the second) gave:

| Entry point | lead text `""` | lead `"\n\n"` | lead `"Let me compute that."` |
|---|---|---|---|
| `RunStream` (what sparktea uses) | 2 requests, `ANSWER: 42` | **1 request, output `"\n\n"`** | **1 request, output is the lead text** |
| `Run` | 2, `ANSWER: 42` | 2, `ANSWER: 42` | 2, `ANSWER: 42` |
| `Run` + per-run `EventListener` capability | 2, `ANSWER: 42` | 2, `ANSWER: 42` | 2, `ANSWER: 42` |
| `StartRun` + `Next()` | 2, `ANSWER: 42` | 2, `ANSWER: 42` | 2, `ANSWER: 42` |

The tool ran exactly once in every case. Note it is *any* leading text, not
just whitespace, so filtering whitespace-only parts would not fix it.

### Fix (not applied)

sparktea calls `RunStream` in `cmd/sparktea/once.go` and `cmd/sparktea/chat.go`
for a multi-step tool loop, which is the wrong entry point. Two library APIs
stream events through the whole loop without committing the first output:

1. `agent.StartRun(...)` and loop on `run.Next()` until `ok == false`, then
   `run.Result()` / `run.Close()`. Closest to the current `for event := range
   run.Events()` loop, so the event `switch` can stay as is.
2. `agent.Run(...)` with a per-run capability implementing `ai.EventListener`
   (`OnEvent`) passed via `ai.WithRunCapabilities`, forwarding events to the
   TUI channel / stdout. `Run` takes the event-streaming path when such a
   capability is present (`ai/loop.go`, `hasEventStreamCapability`).

Before switching, check that the chosen path still emits `PartStartEvent` /
`PartDeltaEvent` token deltas from a real streaming provider (the probe used a
non-streaming fake), and that cancellation, the Logfire run span, and
`Result().Messages()` history behave as they do now. Then rerun the
cross-model `monty_codegen.sh` comparison.
