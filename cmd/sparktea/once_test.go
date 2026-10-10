package main

import (
	"context"
	"sync/atomic"
	"testing"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/fakes"
)

// TestRunTurnContinuesAfterTextBeforeToolCall guards against going back to
// RunStream, which stops at the first output: a response of [text, tool call]
// would end the turn with the text as the answer and never send the tool
// result back to the model (see ISSUES.md).
func TestRunTurnContinuesAfterTextBeforeToolCall(t *testing.T) {
	for _, lead := range []string{"\n\n", "Let me compute that."} {
		t.Run(lead, func(t *testing.T) {
			var requests atomic.Int32
			model := fakes.NewFunctionModel(func(context.Context, []ai.ModelMessage, ai.ModelRequestParams) (*ai.ModelResponse, error) {
				if requests.Add(1) == 1 {
					return &ai.ModelResponse{Parts: []ai.ResponsePart{
						ai.TextPart{Content: lead},
						ai.ToolCallPart{ToolName: "answer", Args: []byte(`{}`), ToolCallID: "c1"},
					}}, nil
				}
				return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "ANSWER: 42"}}}, nil
			})
			agent := ai.NewAgent[struct{}, string](model)
			ai.AddSimpleTool(agent, "answer", func(context.Context, struct{}) (int, error) {
				return 42, nil
			})

			messages, err := runTurn(context.Background(), agent, modelOption{}, "q", nil, nil, "test")
			if err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("got %d model requests, want 2 (the tool result must go back to the model)", got)
			}
			last, ok := messages[len(messages)-1].(ai.ModelResponse)
			if !ok || last.Text() != "ANSWER: 42" {
				t.Fatalf("last message = %#v, want the model's final response", messages[len(messages)-1])
			}
		})
	}
}
