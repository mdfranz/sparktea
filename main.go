// Command openrouter-agent is a bubbletea chat TUI over pydantic-ai-go,
// supporting multiple model providers (OpenRouter, Google Gemini). Pick a
// model at startup, then chat; responses stream in as they're generated.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	options := availableModels()
	if len(options) == 0 {
		fmt.Fprintln(os.Stderr, "openrouter-agent: no API keys found.")
		fmt.Fprintln(os.Stderr, "Set OPENROUTER_API_KEY for OpenRouter models and/or GEMINI_API_KEY (or GOOGLE_API_KEY) for Gemini models.")
		os.Exit(1)
	}

	p := tea.NewProgram(newAppModel(options), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "openrouter-agent:", err)
		os.Exit(1)
	}
}
