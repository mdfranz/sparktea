# openrouter-agent

A minimal [pydantic-ai-go](https://github.com/Kludex/pydantic-ai-go) agent running through OpenRouter.

## Setup

```console
export OPENROUTER_API_KEY="sk-or-..."
go run .
```

`pydantic-ai-go` is pulled from its public GitHub repo as a normal Go module
dependency (no tagged releases yet, so `go.mod` pins a pseudo-version off the
latest commit on `main`). To pick up upstream changes:

```console
go get github.com/Kludex/pydantic-ai-go/ai@main github.com/Kludex/pydantic-ai-go/ai/models/openrouter@main
go mod tidy
```
