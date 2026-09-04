// Command openrouter-agent runs a minimal agent through OpenRouter.
package main

import (
	"context"
	"fmt"
	"log"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/openrouter"
)

func main() {
	// "~" is OpenRouter's alias syntax: this always redirects to the newest
	// snapshot in the DeepSeek V4 Flash family. The API key is read from
	// OPENROUTER_API_KEY unless overridden with openrouter.WithAPIKey.
	model := openrouter.NewModel("~deepseek/deepseek-v4-flash-latest")
	agent := ai.NewAgent[struct{}, string](model,
		ai.WithInstructions("Answer in one short sentence."),
	)

	result, err := agent.Run(context.Background(), "Why is the sky blue?", struct{}{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Output)
}
