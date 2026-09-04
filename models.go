package main

import (
	"fmt"
	"os"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/google"
	"github.com/Kludex/pydantic-ai-go/ai/models/openrouter"
)

// provider identifies which pydantic-ai-go model package builds a modelOption.
type provider string

const (
	providerOpenRouter provider = "openrouter"
	providerGoogle     provider = "google"
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
// support more OpenRouter model IDs or Gemini models.
var modelCatalog = []modelOption{
	{"DeepSeek V4 Flash (latest)", providerOpenRouter, "~deepseek/deepseek-v4-flash-latest"},
	{"Claude Sonnet 5", providerOpenRouter, "anthropic/claude-sonnet-5"},
	{"GPT-5.1", providerOpenRouter, "openai/gpt-5.1"},
	{"Llama 4 Maverick", providerOpenRouter, "meta-llama/llama-4-maverick"},
	{"Gemini 3.8 Flash", providerGoogle, "gemini-3.8-flash"},
	{"Gemini 2.5 Pro", providerGoogle, "gemini-2.5-pro"},
	{"Gemini 2.0 Flash", providerGoogle, "gemini-2.0-flash"},
}

// apiKeyPresent reports whether the environment has credentials for this
// option's provider.
func (m modelOption) apiKeyPresent() bool {
	switch m.provider {
	case providerOpenRouter:
		return os.Getenv("OPENROUTER_API_KEY") != ""
	case providerGoogle:
		return os.Getenv("GOOGLE_API_KEY") != "" || os.Getenv("GEMINI_API_KEY") != ""
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
	default:
		panic("openrouter-agent: unknown provider " + string(m.provider))
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
