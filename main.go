// Command sparktea is a bubbletea chat TUI over pydantic-ai-go,
// supporting multiple model providers (OpenRouter, Google Gemini). Pick a
// model at startup, then chat; responses stream in as they're generated.
package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	os.Exit(run())
}

// run holds the body of main so deferred cleanup (flushing telemetry) always
// runs — os.Exit in main itself would skip it.
func run() int {
	options := availableModels()
	if len(options) == 0 {
		fmt.Fprintln(os.Stderr, "sparktea: no API keys found.")
		fmt.Fprintln(os.Stderr, "Set OPENROUTER_API_KEY for OpenRouter models and/or GEMINI_API_KEY (or GOOGLE_API_KEY) for Gemini models.")
		return 1
	}

	ctx := context.Background()
	shutdownLogfire, err := initLogfire(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sparktea: logfire disabled:", err)
	} else if logfireCapability != nil {
		fmt.Fprintln(os.Stderr, "sparktea: sending traces to Logfire ("+logfireEndpoint()+")")
	}
	defer func() {
		if err := shutdownLogfire(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "sparktea: flush telemetry:", err)
		}
	}()

	p := tea.NewProgram(newAppModel(options), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "sparktea:", err)
		return 1
	}
	return 0
}
