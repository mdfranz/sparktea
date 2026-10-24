package main

import (
	"context"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/mdfranz/sparktea/codemode"
)

// requireCodeTool makes the first request in a supported Code Mode turn call run_code.
// Later requests must allow text so the model can answer after the tool result.
// OpenAI Responses accepts a named tool_choice through ExtraBody. OpenRouter
// routes some thinking models to providers that reject forced tool choices.
func requireCodeTool(option modelOption) (ai.RunOption, bool) {
	if option.provider != providerOpenAI {
		return nil, false
	}
	return ai.WithRunModelSettingsFunc[struct{}](func(_ context.Context, rc *ai.RunContext[struct{}]) (ai.ModelSettings, error) {
		if rc.RunStep != 1 {
			return ai.ModelSettings{}, nil
		}
		return ai.ModelSettings{ExtraBody: map[string]any{
			"tool_choice": map[string]any{"type": "function", "name": codemode.ToolName},
		}}, nil
	}), true
}
