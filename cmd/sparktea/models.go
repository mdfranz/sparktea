package main

import (
	"fmt"
	"os"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/anthropic"
	"github.com/Kludex/pydantic-ai-go/ai/models/google"
	"github.com/Kludex/pydantic-ai-go/ai/models/mistral"
	"github.com/Kludex/pydantic-ai-go/ai/models/openai"
	"github.com/Kludex/pydantic-ai-go/ai/models/openrouter"
)

// provider identifies which pydantic-ai-go model package builds a modelOption.
type provider string

const (
	providerOpenRouter provider = "openrouter"
	providerGoogle     provider = "google"
	providerAnthropic  provider = "anthropic"
	providerMistral    provider = "mistral"
	providerOpenAI     provider = "openai"
)

// modelOption is one selectable entry in the startup picker. It implements
// list.Item (Title/Description/FilterValue) so it can be used directly as a
// bubbles/list row.
type modelOption struct {
	label    string
	provider provider
	modelID  string
}

func (m modelOption) Title() string       { return m.label }
func (m modelOption) Description() string { return fmt.Sprintf("%s · %s", m.provider, m.modelID) }
func (m modelOption) FilterValue() string { return m.label }

// modelCatalog lists the models offered at startup. Add entries here to
// support more OpenRouter, Gemini, Anthropic, or Mistral model IDs.
var modelCatalog = []modelOption{
	{"DeepSeek V4 Flash (latest)", providerOpenRouter, "~deepseek/deepseek-v4-flash-latest"},
	{"DeepSeek V4 Pro", providerOpenRouter, "deepseek/deepseek-v4-pro"},
	{"Kimi (latest)", providerOpenRouter, "~moonshotai/kimi-latest"},
	{"Qwen 3.8 Flash", providerOpenRouter, "qwen/qwen3.8-flash"},
	{"GLM Flash (latest)", providerOpenRouter, "~z-ai/glm-flash-latest"},
	{"GLM (latest)", providerOpenRouter, "~z-ai/glm-latest"},
	{"GPT-5.1", providerOpenRouter, "openai/gpt-5.1"},
	{"Llama 4 Maverick", providerOpenRouter, "meta-llama/llama-4-maverick"},
	{"Gemini 3.8 Flash", providerGoogle, "gemini-3.8-flash"},
	{"Gemini 2.5 Pro", providerGoogle, "gemini-2.5-pro"},
	{"Gemini 3.6 Flash", providerGoogle, "gemini-3.6-flash"},
	{"Claude Opus 5", providerAnthropic, "claude-opus-5"},
	{"Claude Sonnet 5", providerAnthropic, "claude-sonnet-5"},
	{"Claude Haiku 4.5", providerAnthropic, "claude-haiku-4-5-20251001"},
	{"Mistral Large", providerMistral, "mistral-large-latest"},
	{"Mistral Medium", providerMistral, "mistral-medium-latest"},
	{"Mistral Small", providerMistral, "mistral-small-latest"},
	{"GPT-5.6 Sol", providerOpenAI, "gpt-5.6-sol"},
	{"GPT-5.6 Terra", providerOpenAI, "gpt-5.6-terra"},
	{"GPT-5.6 Luna", providerOpenAI, "gpt-5.6-luna"},
}

// apiKeyPresent reports whether the environment has credentials for this
// option's provider.
func (m modelOption) apiKeyPresent() bool {
	switch m.provider {
	case providerOpenRouter:
		return os.Getenv("OPENROUTER_API_KEY") != ""
	case providerGoogle:
		return os.Getenv("GOOGLE_API_KEY") != "" || os.Getenv("GEMINI_API_KEY") != ""
	case providerAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY") != ""
	case providerMistral:
		return os.Getenv("MISTRAL_API_KEY") != ""
	case providerOpenAI:
		return os.Getenv("OPENAI_API_KEY") != ""
	default:
		return false
	}
}

// newModel builds the pydantic-ai-go model for this option.
func (m modelOption) newModel() ai.Model {
	switch m.provider {
	case providerOpenRouter:
		// OpenRouter only returns token usage and cost when a request opts
		// in via its own "usage: {include: true}" extension — the generic
		// OpenAI-compatible stream_options.include_usage pydantic-ai-go
		// already sends isn't enough for every model OpenRouter routes to.
		// Without this, sessionUsage (and the header total) stays at zero
		// even on a fully successful turn.
		settings, _ := openrouter.Settings{Usage: &openrouter.UsageConfig{Include: true}}.Build()
		return openrouter.NewModel(m.modelID, openrouter.WithDefaultSettings(settings))
	case providerGoogle:
		return google.NewModel(m.modelID)
	case providerAnthropic:
		return anthropic.NewModel(m.modelID)
	case providerMistral:
		return mistral.NewModel(m.modelID)
	case providerOpenAI:
		// Responses, not Chat Completions: native web search only works on
		// Chat models whose name contains "-search-preview" (none of ours
		// do), while Responses grants it unconditionally for every model —
		// see pydantic-ai-go's docs/native-tools.md. Responses is also what
		// upstream's own docs use for the gpt-5.6 family specifically.
		return openai.NewResponsesModel(m.modelID)
	default:
		panic("sparktea: unknown provider " + string(m.provider))
	}
}

// supportsNativeWebSearch reports whether pydantic-ai-go's provider adapter
// for this option implements NativeToolSupportModel for ai.WebSearchTool.
// It gates /search: a provider that doesn't implement the interface at all
// (Mistral, as of this writing) has any ai.NativeTool in the request rejected
// outright by the transport, even one marked Optional — Optional only takes
// effect when SupportsNativeTool exists to be consulted.
//
// OpenAI is excluded too, despite openai.ResponsesModel implementing the
// interface: pydantic-ai-go's Responses stream parser doesn't recognize the
// response.web_search_call.* progress events OpenAI actually streams back,
// so a real web search call crashes the turn outright rather than degrading
// — confirmed live via Logfire 2026-09-05, filed as
// github.com/Kludex/pydantic-ai-go#4. Re-enable once that's fixed upstream.
func (m modelOption) supportsNativeWebSearch() bool {
	switch m.provider {
	case providerOpenRouter, providerGoogle, providerAnthropic:
		return true
	default:
		return false
	}
}

// availableModels returns the catalog entries whose provider has an API key
// set, so the picker never offers a model that would just fail immediately.
func availableModels() []modelOption {
	var out []modelOption
	for _, m := range modelCatalog {
		if m.apiKeyPresent() {
			out = append(out, m)
		}
	}
	return out
}
