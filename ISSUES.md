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

## Verifying a fix

Once either issue closes, `go get github.com/Kludex/pydantic-ai-go/ai@main
... && go mod tidy` (see README's "Updating pydantic-ai-go") and re-run the
matching repro above. No sparktea-side code should need to change either
way — both bugs are in the provider adapters' request serialization, not in
how sparktea builds or replays `m.history`.
