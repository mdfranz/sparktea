# sparktea

A terminal chat UI, built with [bubbletea](https://github.com/charmbracelet/bubbletea),
for talking to any model [**pydantic-ai-go**](https://github.com/Kludex/pydantic-ai-go)
supports — currently OpenRouter and Google Gemini, with more just an import
away.

Launch it, pick a model, and chat. Responses stream in token-by-token,
thinking traces render when a model exposes them, and conversation history
survives switching models — even across providers — mid-chat.

## Built on pydantic-ai-go

sparktea is a thin terminal skin over
[**pydantic-ai-go**](https://github.com/Kludex/pydantic-ai-go), a Go-native,
idiomatic port of [PydanticAI](https://ai.pydantic.dev/) by
[Marcelo Trylesinski (**@Kludex**)](https://github.com/Kludex). Nearly
everything sparktea does is pydantic-ai-go doing the real work; this repo
just wires it up to a terminal:

| sparktea feature | pydantic-ai-go piece behind it |
| --- | --- |
| The whole chat loop, streamed token-by-token | `ai.Agent`, `Agent.RunStream`, `PartStartEvent`/`PartDeltaEvent` |
| Every model provider (OpenRouter, Gemini, and a one-line swap away from OpenAI, Anthropic, Bedrock, Groq, Mistral, xAI, and more) | `ai/models/*` provider adapters |
| Cross-provider history when you `/model` mid-conversation | `ai.ModelMessage`, `ai.WithMessageHistory` — a provider-neutral message format |
| `/search` web grounding | `ai.WebSearchTool` / `ai.WithRunNativeTools` |
| Thinking traces rendered above the answer | `ai.ThinkingPart` / `ai.ThinkingPartDelta` |
| `/save` and `/load` | `ai.MarshalMessages` / `ai.UnmarshalMessages` |
| `/usage` totals, including cost | `ai.Usage` and its `genai-prices` integration |
| Logfire tracing | `ai.NewInstrumentation`, pydantic-ai-go's OpenTelemetry capability |

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
go run .
```

Keys:
- `OPENROUTER_API_KEY` — required for OpenRouter models.
- `GEMINI_API_KEY` or `GOOGLE_API_KEY` — required for Gemini models.

## Usage

- `↑`/`↓` (or `j`/`k`) to move through the model list, `enter` to select.
- Type a message, `enter` to send. Reasoning models' thinking traces render
  dimmed above the answer when the provider exposes them.
- `esc`, `ctrl+c`, or `ctrl+d` (on an empty input line) to quit.

### Commands

Type these instead of a message:

| Command | Effect |
| --- | --- |
| `/model` | Reopen the model picker mid-conversation; history carries over, even across providers. |
| `/usage` | Show session totals: requests, input/output tokens, tool calls, cost. |
| `/clear` | Discard history and usage totals; start fresh without restarting. |
| `/search` (or `/search on`/`off`) | Toggle native web search grounding (`ai.WebSearchTool`) for models that support it. Unsupported models just ignore it. |
| `/save [name]` | Write the conversation to `~/.sparktea/sessions/<name>.json` (default name `default`). |
| `/load [name]` | Restore a saved conversation, replaying its transcript and history. |

## Observability (Logfire)

Set `LOGFIRE_TOKEN` to send OpenTelemetry traces and metrics to
[Logfire](https://pydantic.dev/logfire), Pydantic's own observability
product — the run's agent spans, model requests, token usage, and cost, one
trace per turn:

```console
export LOGFIRE_TOKEN="your-write-token"
go run .
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

## Adding models

The startup list is a static catalog in `models.go`. Add an entry there —
`{label, provider, modelID}` — to offer another OpenRouter model ID or
Gemini model. `~`-prefixed OpenRouter IDs are OpenRouter's alias syntax
(e.g. `~deepseek/deepseek-v4-flash-latest` always redirects to the newest
snapshot in that family).

Adding a whole new provider is just as thin: pick one of pydantic-ai-go's
other [model packages](https://github.com/Kludex/pydantic-ai-go/tree/main/ai/models)
(`anthropic`, `openai`, `bedrock`, `groq`, `mistral`, `xai`, `cohere`,
`ollama`, ...), add a `provider` constant and a case in `newModel()`
(`models.go`), and list it in `modelCatalog`.

## Updating pydantic-ai-go

`pydantic-ai-go` is pulled from its public GitHub repo as a normal Go module
dependency (no tagged releases yet, so `go.mod` pins a pseudo-version off the
latest commit on `main`). To pick up upstream changes:

```console
go get github.com/Kludex/pydantic-ai-go/ai@main \
  github.com/Kludex/pydantic-ai-go/ai/models/openrouter@main \
  github.com/Kludex/pydantic-ai-go/ai/models/google@main
go mod tidy
```

See [pydantic-ai-go's docs](https://github.com/Kludex/pydantic-ai-go/tree/main/docs)
for everything the library can do beyond what sparktea currently exposes.
