// Command sparktea is a bubbletea chat TUI over pydantic-ai-go,
// supporting multiple model providers (OpenRouter, Google Gemini). Pick a
// model at startup, then chat; responses stream in as they're generated.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	os.Exit(run())
}

// run holds the body of main so deferred cleanup (flushing telemetry) always
// runs — os.Exit in main itself would skip it.
func run() (exitCode int) {
	opts, err := parseCLIFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	shutdownLocalLog, err := initLocalLog()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sparktea: local logging disabled:", err)
	} else {
		defer func() {
			logLocal(slog.LevelInfo, "process_finished", "exit_code", exitCode)
			if err := shutdownLocalLog(); err != nil {
				fmt.Fprintln(os.Stderr, "sparktea: close local log:", err)
			}
		}()
	}
	mode := "interactive"
	if opts.prompt != "" {
		mode = "one_shot"
	}
	logLocal(slog.LevelInfo, "process_started", "mode", mode)

	options := availableModels()
	if len(options) == 0 {
		logLocal(slog.LevelWarn, "no_provider_keys")
		fmt.Fprintln(os.Stderr, "sparktea: no API keys found.")
		fmt.Fprintln(os.Stderr, "Set OPENROUTER_API_KEY, GEMINI_API_KEY (or GOOGLE_API_KEY), ANTHROPIC_API_KEY, and/or MISTRAL_API_KEY.")
		return 1
	}

	if opts.listModels {
		logLocal(slog.LevelInfo, "models_listed", "count", len(options))
		for _, m := range options {
			fmt.Printf("%s:%s\t%s\n", m.provider, m.modelID, m.label)
		}
		return 0
	}

	ctx := context.Background()
	shutdownLogfire, err := initLogfire(ctx)
	if err != nil {
		logLocalError("logfire_initialization_failed", err)
		fmt.Fprintln(os.Stderr, "sparktea: logfire disabled:", err)
	} else if logfireCapability != nil {
		logLocal(slog.LevelInfo, "logfire_enabled", "endpoint", logfireEndpoint())
		fmt.Fprintln(os.Stderr, "sparktea: sending traces to Logfire ("+logfireEndpoint()+")")
	} else {
		logLocal(slog.LevelInfo, "logfire_disabled")
	}
	defer func() {
		if err := shutdownLogfire(context.Background()); err != nil {
			logLocalError("logfire_shutdown_failed", err)
			fmt.Fprintln(os.Stderr, "sparktea: flush telemetry:", err)
		}
	}()

	if opts.prompt != "" {
		option, err := resolveModel(options, opts.model)
		if err != nil {
			logLocalError("model_resolution_failed", err)
			fmt.Fprintln(os.Stderr, "sparktea:", err)
			return 1
		}
		if err := runOnce(ctx, option, opts); err != nil {
			logLocalError("one_shot_failed", err, "provider", string(option.provider), "model", option.modelID)
			fmt.Fprintln(os.Stderr, "sparktea:", err)
			return 1
		}
		return 0
	}

	p := tea.NewProgram(newAppModel(options), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		logLocalError("interactive_failed", err)
		fmt.Fprintln(os.Stderr, "sparktea:", err)
		return 1
	}
	return 0
}
