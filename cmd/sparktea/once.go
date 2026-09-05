package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/mdfranz/sparktea/codemode"
)

// cliOptions holds sparktea's non-interactive flags: run a single prompt to
// completion and exit, skipping the TUI. Meant for scripting — e.g. driving
// many run_code prompts through Code Mode for testing without hand-typing
// each one into the chat screen.
type cliOptions struct {
	prompt     string
	model      string
	code       bool
	search     bool
	listModels bool
}

// parseCLIFlags parses args (os.Args[1:]). flag.ErrHelp is returned as-is on
// -h/-help so run() can treat it as a clean exit rather than a usage error.
func parseCLIFlags(args []string) (cliOptions, error) {
	fs := flag.NewFlagSet("sparktea", flag.ContinueOnError)
	var opts cliOptions
	fs.StringVar(&opts.prompt, "prompt", "", "Run this prompt once, non-interactively, and exit (skips the TUI).")
	fs.StringVar(&opts.model, "model", "", "Model ID to use with -prompt, e.g. claude-haiku-4-5-20251001, or provider:model_id (e.g. anthropic:claude-haiku-4-5-20251001) to disambiguate (see -list-models). Defaults to the first available model.")
	fs.BoolVar(&opts.code, "code", false, "Enable Code Mode's run_code tool for this run.")
	fs.BoolVar(&opts.search, "search", false, "Enable native web search for this run, if the model supports it.")
	fs.BoolVar(&opts.listModels, "list-models", false, "List available models as provider:model_id (API key present) and exit.")
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	return opts, nil
}

// resolveModel finds the catalog entry matching spec. Two forms:
//
//   - "provider:model_id" (e.g. "anthropic:claude-haiku-4-5-20251001") is an
//     exact, unambiguous pin — needed because a bare model_id isn't
//     guaranteed unique across providers (OpenRouter's own IDs are already
//     slash-namespaced by upstream provider, e.g. "openai/gpt-5.1", which
//     could collide with a same-named model added under a different
//     sparktea provider later; ':' can't appear in a catalog model_id today,
//     so it's an unambiguous separator).
//   - a bare model_id or label (or an unambiguous substring of either) —
//     the normal case for scripting, matched case-insensitively.
//
// An empty spec picks the first available option, matching the interactive
// picker's default.
func resolveModel(options []modelOption, spec string) (modelOption, error) {
	if spec == "" {
		if len(options) == 0 {
			return modelOption{}, errors.New("no models available")
		}
		return options[0], nil
	}

	if p, id, ok := strings.Cut(spec, ":"); ok {
		lowerProvider, lowerID := strings.ToLower(p), strings.ToLower(id)
		for _, m := range options {
			if strings.ToLower(string(m.provider)) == lowerProvider && strings.ToLower(m.modelID) == lowerID {
				return m, nil
			}
		}
		return modelOption{}, fmt.Errorf("no available model matches %q (see -list-models)", spec)
	}

	lower := strings.ToLower(spec)
	for _, m := range options {
		if strings.ToLower(m.modelID) == lower || strings.ToLower(m.label) == lower {
			return m, nil
		}
	}
	var matches []modelOption
	for _, m := range options {
		if strings.Contains(strings.ToLower(m.modelID), lower) || strings.Contains(strings.ToLower(m.label), lower) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return modelOption{}, fmt.Errorf("no available model matches %q (see -list-models)", spec)
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = fmt.Sprintf("%s:%s", m.provider, m.modelID)
		}
		return modelOption{}, fmt.Errorf("%q matches multiple models: %s", spec, strings.Join(ids, ", "))
	}
}

// runOnce runs a single prompt against option to completion, with no TUI.
// The model's answer streams to stdout (so output is pipeable/scriptable);
// thinking, tool calls, tool results, and a final usage line go to stderr —
// exactly the detail Logfire redacts by default (see LOGFIRE_SEND_CONTENT
// in logfire.go), useful for exercising Code Mode's run_code tool and
// seeing the code and result it produced without touching telemetry.
func runOnce(ctx context.Context, option modelOption, opts cliOptions) error {
	agent := newAgentFor(option)

	var runOpts []ai.RunOption
	if opts.code {
		runOpts = append(runOpts, ai.WithRunCapabilities(codemode.New()))
	}
	if opts.search {
		if !option.supportsNativeWebSearch() {
			fmt.Fprintf(os.Stderr, "sparktea: %s has no native web search — ignored\n", option.provider)
		} else {
			runOpts = append(runOpts, ai.WithRunNativeTools(ai.WebSearchTool{Optional: true}))
		}
	}

	runTracer, runCtx := startRunTracer(ctx, "sparktea turn")

	run := agent.RunStream(runCtx, opts.prompt, struct{}{}, runOpts...)
	for event, err := range run.Events() {
		if err != nil {
			runTracer.end(err)
			return err
		}
		switch e := event.(type) {
		case ai.PartStartEvent:
			runTracer.observe(e.Part)
			switch part := e.Part.(type) {
			case ai.TextPart:
				fmt.Print(part.Content)
			case ai.ThinkingPart:
				fmt.Fprint(os.Stderr, part.Content)
			case ai.NativeToolCallPart:
				fmt.Fprintf(os.Stderr, "\n[native tool call] %s\n", part.ToolName)
			}
		case ai.PartDeltaEvent:
			switch delta := e.Delta.(type) {
			case ai.TextPartDelta:
				fmt.Print(delta.ContentDelta)
			case ai.ThinkingPartDelta:
				fmt.Fprint(os.Stderr, delta.ContentDelta)
			}
		case ai.FunctionToolCallEvent:
			// Args is fully assembled by the time this fires (right before
			// execution), unlike the same ToolCallPart seen earlier via
			// PartStartEvent, whose Args streams in over ToolCallPartDelta
			// events this loop doesn't otherwise accumulate.
			fmt.Fprintf(os.Stderr, "\n[tool call] %s %s\n", e.Part.ToolName, e.Part.Args)
		case ai.FunctionToolResultEvent:
			switch part := e.Part.(type) {
			case ai.ToolReturnPart:
				content, _ := json.Marshal(part.Content)
				fmt.Fprintf(os.Stderr, "[tool result] %s %s\n", part.ToolName, content)
			case ai.RetryPromptPart:
				fmt.Fprintf(os.Stderr, "[tool error] %s %s\n", part.ToolName, part.Content)
			}
		}
	}
	runTracer.end(nil)
	fmt.Println()

	if result := run.Result(); result != nil {
		u := result.Usage()
		cost := "unknown"
		if u.CostUSD != nil {
			cost = fmt.Sprintf("$%.4f", *u.CostUSD)
		}
		fmt.Fprintf(os.Stderr, "usage: requests=%d input_tokens=%d output_tokens=%d tool_calls=%d cost=%s\n",
			u.Requests, u.InputTokens, u.OutputTokens, u.ToolCalls, cost)
	}
	return nil
}
