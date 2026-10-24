package main

import (
	"context"
	"reflect"
	"testing"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/fakes"
)

func TestRequireCodeToolOnlyOnFirstOpenAIRequest(t *testing.T) {
	var choices []any
	model := fakes.NewFunctionModel(func(_ context.Context, _ []ai.ModelMessage, params ai.ModelRequestParams) (*ai.ModelResponse, error) {
		choices = append(choices, params.Settings.ExtraBody["tool_choice"])
		if len(choices) == 1 {
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: "run_code", Args: []byte(`{}`), ToolCallID: "code-1"},
			}}, nil
		}
		return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "42"}}}, nil
	})
	agent := ai.NewAgent[struct{}, string](model)
	ai.AddSimpleTool(agent, "run_code", func(context.Context, struct{}) (int, error) { return 42, nil })

	option, ok := requireCodeTool(modelOption{provider: providerOpenAI})
	if !ok {
		t.Fatal("OpenAI should require run_code")
	}
	if _, err := agent.Run(context.Background(), "calculate", struct{}{}, option); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "function", "name": "run_code"}
	if len(choices) != 2 || !reflect.DeepEqual(choices[0], want) || choices[1] != nil {
		t.Fatalf("tool choices = %#v, want [%#v, nil]", choices, want)
	}
}

func TestRequireCodeToolSkipsProvidersThatRejectForcedChoice(t *testing.T) {
	for _, provider := range []provider{providerOpenRouter, providerGoogle, providerAnthropic, providerMistral} {
		if option, ok := requireCodeTool(modelOption{provider: provider}); ok || option != nil {
			t.Errorf("%s unexpectedly requires a forced tool choice", provider)
		}
	}
}
