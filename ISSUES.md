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

### [#2](https://github.com/Kludex/pydantic-ai-go/issues/2) — Anthropic: replayed thinking block uses flat schema; live API expects nested `content[0].thinking.thinking` once tools are attached

**Status: open, unfixed.**

`anthropic.go:1507-1510` serializes a replayed `ai.ThinkingPart` as a flat
`{"type":"thinking","thinking":"<string>","signature":"..."}`, gated only on
`Signature != ""` — no check for whether the outgoing request also has tools
attached.

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

**Status: open, unfixed.**

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

## pydantic-ai-go: OpenAI Responses stream doesn't recognize web-search progress events

Found 2026-09-05 via a live session testing sparktea's new OpenAI support.
Confirmed via Logfire traces; not yet filed upstream or fixed.

`responses_stream.go`'s event-type switch (around line 714) hardcodes an
allowlist of provider progress events safe to ignore per tool family —
`response.code_interpreter_call.*`, `response.image_generation_call.*`,
`response.file_search_call.*`, `response.mcp_call.*` — but has no
`response.web_search_call.*` entries at all. Any such event falls through to
the `default:` case and aborts the stream with `openai: unknown Responses
stream event type "response.web_search_call.in_progress"`.

Repro: `openai.NewResponsesModel(...)` with `ai.WithRunNativeTools(ai.WebSearchTool{...})`,
a prompt that actually triggers a web search call, streamed (not static)
generation. Fails immediately with the error above; the run is aborted, not
degraded.

**Impact on sparktea:** unlike the two issues above, this doesn't need a
prior turn or history replay — the *first* streamed native web search on an
OpenAI model crashes the turn. `supportsNativeWebSearch()` in `models.go`
excludes `providerOpenAI` entirely as a result, so `/search` is currently
unavailable for OpenAI models (same treatment as Mistral, for an unrelated
reason) until this is fixed upstream.

## Verifying a fix

Once an issue closes, `go get github.com/Kludex/pydantic-ai-go/ai@main
... && go mod tidy` (see README's "Updating pydantic-ai-go") and re-run the
matching repro above. The two thinking-block issues need no sparktea-side
change either way — both are in the provider adapters' request
serialization, not in how sparktea builds or replays `m.history`. The
OpenAI web-search issue does: once fixed, add `providerOpenAI` back to
`supportsNativeWebSearch()` in `models.go`.
