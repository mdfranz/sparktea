// Command sparktea is a bubbletea chat TUI over pydantic-ai-go,
// supporting multiple model providers (OpenRouter, Google Gemini). Pick a
// model at startup, then chat; responses stream in as they're generated.
package main

import (
	"context"
	"errors"
	"flag"
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
	opts, err := parseCLIFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	options := availableModels()
	if len(options) == 0 {
		fmt.Fprintln(os.Stderr, "sparktea: no API keys found.")
		fmt.Fprintln(os.Stderr, "Set OPENROUTER_API_KEY, GEMINI_API_KEY (or GOOGLE_API_KEY), ANTHROPIC_API_KEY, and/or MISTRAL_API_KEY.")
		return 1
	}

	if opts.listModels {
		for _, m := range options {
			fmt.Printf("%s:%s\t%s\n", m.provider, m.modelID, m.label)
		}
		return 0
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

	if opts.prompt != "" {
		option, err := resolveModel(options, opts.model)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sparktea:", err)
			return 1
		}
		if err := runOnce(ctx, option, opts); err != nil {
			fmt.Fprintln(os.Stderr, "sparktea:", err)
			return 1
		}
		return 0
	}

	p := tea.NewProgram(newAppModel(options), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "sparktea:", err)
		return 1
	}
	return 0
}
