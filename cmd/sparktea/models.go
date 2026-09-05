package main

import (
	"fmt"
	"os"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/anthropic"
	"github.com/Kludex/pydantic-ai-go/ai/models/google"
	"github.com/Kludex/pydantic-ai-go/ai/models/mistral"
	"github.com/Kludex/pydantic-ai-go/ai/models/openrouter"
)

// provider identifies which pydantic-ai-go model package builds a modelOption.
type provider string

const (
	providerOpenRouter provider = "openrouter"
	providerGoogle     provider = "google"
	providerAnthropic  provider = "anthropic"
	providerMistral    provider = "mistral"
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
	{"DeepSeek V4 Pro (latest)", providerOpenRouter, "~deepseek/deepseek-v4-pro-latest"},
	{"Kimi (latest)", providerOpenRouter, "~moonshotai/kimi-latest"},
	{"Qwen 3.8 Flash", providerOpenRouter, "qwen/qwen3.8-flash"},
	{"GLM Flash (latest)", providerOpenRouter, "~z-ai/glm-flash-latest"},
	{"GLM (latest)", providerOpenRouter, "~z-ai/glm-latest"},
	{"GPT-5.1", providerOpenRouter, "openai/gpt-5.1"},
	{"Llama 4 Maverick", providerOpenRouter, "meta-llama/llama-4-maverick"},
	{"Gemini 3.8 Flash", providerGoogle, "gemini-3.8-flash"},
	{"Gemini 2.5 Pro", providerGoogle, "gemini-2.5-pro"},
	{"Gemini 2.0 Flash", providerGoogle, "gemini-2.0-flash"},
	{"Claude Opus 5", providerAnthropic, "claude-opus-5"},
	{"Claude Sonnet 5", providerAnthropic, "claude-sonnet-5"},
	{"Claude Haiku 4.5", providerAnthropic, "claude-haiku-4-5-20251001"},
	{"Mistral Large", providerMistral, "mistral-large-latest"},
	{"Mistral Medium", providerMistral, "mistral-medium-latest"},
	{"Mistral Small", providerMistral, "mistral-small-latest"},
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
	default:
		return false
	}
}

// newModel builds the pydantic-ai-go model for this option.
func (m modelOption) newModel() ai.Model {
	switch m.provider {
	case providerOpenRouter:
		return openrouter.NewModel(m.modelID)
	case providerGoogle:
		return google.NewModel(m.modelID)
	case providerAnthropic:
		return anthropic.NewModel(m.modelID)
	case providerMistral:
		return mistral.NewModel(m.modelID)
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
