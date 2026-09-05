# openrouter-agent

A [bubbletea](https://github.com/charmbracelet/bubbletea) chat TUI built on
[pydantic-ai-go](https://github.com/Kludex/pydantic-ai-go), supporting
multiple model providers — currently OpenRouter and Google Gemini.

Launch it, pick a model from the list, and chat. Responses stream in
token-by-token, and conversation history is kept for the life of the run.

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
| `/save [name]` | Write the conversation to `~/.openrouter-agent/sessions/<name>.json` (default name `default`). |
| `/load [name]` | Restore a saved conversation, replaying its transcript and history. |

## Adding models

The startup list is a static catalog in `models.go`. Add an entry there —
`{label, provider, modelID}` — to offer another OpenRouter model ID or
Gemini model. `~`-prefixed OpenRouter IDs are OpenRouter's alias syntax
(e.g. `~deepseek/deepseek-v4-flash-latest` always redirects to the newest
snapshot in that family).

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
