# Upstream issues affecting sparktea

Tracking known bugs in dependencies that affect sparktea but are fixed
upstream, not here. Update the status line when an issue closes, and check
whether sparktea still needs to work around it.

## pydantic-ai-go: thinking-block replay

Both found 2026-09-05 via the same live session: `claude-opus-5` produced
extended-thinking content across two successful turns; the next request
that replayed that history broke, two different ways depending on what
changed about the request. Confirmed via Logfire traces (exact request/response),
not just local logs (which never retain error bodies). Root-caused by
reading the pinned pydantic-ai-go source (`v0.0.0-20260904230829-3c976cdd1116`);
not yet fixed upstream.

### [#2](https://github.com/Kludex/pydantic-ai-go/issues/2) — Anthropic: replayed thinking block drops the required `thinking` field when empty

**Status: fix filed, [PR #7](https://github.com/Kludex/pydantic-ai-go/pull/7), pending merge.**
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

**Status: fix filed, [PR #6](https://github.com/Kludex/pydantic-ai-go/pull/6), pending merge.**
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

**Status: fix filed, [PR #9](https://github.com/Kludex/pydantic-ai-go/pull/9), pending merge.**
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

**Status: open, unfixed.**

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

Once an issue closes, `go get github.com/Kludex/pydantic-ai-go/ai@main
... && go mod tidy` (see README's "Updating pydantic-ai-go") and re-run the
matching repro above. #2, #3, and #8 need no sparktea-side change either
way — all three are in the provider adapters' request serialization, not in
how sparktea builds or replays `m.history`. #4 does: once fixed, add
`providerOpenAI` back to `supportsNativeWebSearch()` in `models.go`.

**Current temporary state:** while #2/#3/#8 are pending merge, `go.mod`
`replace`s `github.com/Kludex/pydantic-ai-go` with a branch on
`github.com/mdfranz/pydantic-ai-go` (a fork) combining all three fixes, so
sparktea itself isn't blocked on any of them merging. Once each PR merges
upstream, drop it from that combined branch (or once all three have merged,
drop the `replace` entirely and bump the pinned version in `require`
instead) — see README's "Updating pydantic-ai-go".
