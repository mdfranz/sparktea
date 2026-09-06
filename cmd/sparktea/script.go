package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/mdfranz/sparktea/codemode"
)

// scriptStep is one parsed line of a -script file: either a command
// ("model", "search", "code", "clear") with its argument, or a "prompt" line
// to send as the next turn.
type scriptStep struct {
	kind string
	arg  string
}

// parseScript reads a -script file. The format is line-oriented and
// deliberately small — just enough to reproduce a specific bug-triggering
// sequence (e.g. "a plain turn, then /search on, then another turn")
// deterministically instead of hand-typing it into the TUI each time:
//
//   - Blank lines and lines starting with # are ignored.
//   - "/model <spec>" switches models mid-script, same spec syntax as
//     resolveModel (bare model_id/label or "provider:model_id").
//   - "/search on|off" and "/code on|off" toggle native web search and Code
//     Mode for turns from that point on, same as the TUI's commands.
//   - "/clear" discards history, starting a fresh conversation.
//   - Any other line is a prompt, run as one full turn against the current
//     model, toggles, and history. One line is one prompt — there's no
//     continuation syntax for multi-line prompts.
func parseScript(r io.Reader) ([]scriptStep, error) {
	var steps []scriptStep
	scanner := bufio.NewScanner(r)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "/") {
			steps = append(steps, scriptStep{kind: "prompt", arg: line})
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "/model":
			if len(fields) != 2 {
				return nil, fmt.Errorf("line %d: usage: /model <spec>", lineNum)
			}
			steps = append(steps, scriptStep{kind: "model", arg: fields[1]})
		case "/search", "/code":
			if len(fields) != 2 || (fields[1] != "on" && fields[1] != "off") {
				return nil, fmt.Errorf("line %d: usage: %s on|off", lineNum, fields[0])
			}
			steps = append(steps, scriptStep{kind: strings.TrimPrefix(fields[0], "/"), arg: fields[1]})
		case "/clear":
			if len(fields) != 1 {
				return nil, fmt.Errorf("line %d: usage: /clear", lineNum)
			}
			steps = append(steps, scriptStep{kind: "clear"})
		default:
			return nil, fmt.Errorf("line %d: unknown script command %q", lineNum, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read script: %w", err)
	}
	return steps, nil
}

// runScript drives a parsed script against one growing conversation,
// non-interactively — the same per-turn machinery -prompt uses (runTurn),
// but with /model, /search, /code, and /clear between turns so a scripted
// sequence can be rerun deterministically. Each prompt line's answer streams
// to stdout, same as -prompt; everything else (which turn is running,
// thinking, tool activity, model switches) goes to stderr.
func runScript(ctx context.Context, options []modelOption, opts cliOptions) error {
	f, err := os.Open(opts.script)
	if err != nil {
		return fmt.Errorf("open script: %w", err)
	}
	defer f.Close()
	steps, err := parseScript(f)
	if err != nil {
		return fmt.Errorf("parse script: %w", err)
	}

	option, err := resolveModel(options, opts.model)
	if err != nil {
		return err
	}
	agent := newAgentFor(option)
	codeModeCapability := codemode.New()

	var history []ai.ModelMessage
	var searchEnabled, codeEnabled bool
	turn := 0

	for _, step := range steps {
		switch step.kind {
		case "model":
			next, err := resolveModel(options, step.arg)
			if err != nil {
				return err
			}
			if next != option {
				option = next
				agent = newAgentFor(option)
				fmt.Fprintf(os.Stderr, "\n— switched to %s:%s —\n", option.provider, option.modelID)
			}

		case "search":
			searchEnabled = step.arg == "on"

		case "code":
			codeEnabled = step.arg == "on"

		case "clear":
			history = nil
			fmt.Fprintln(os.Stderr, "\n— history cleared —")

		case "prompt":
			turn++
			var runOpts []ai.RunOption
			if codeEnabled {
				runOpts = append(runOpts, ai.WithRunCapabilities(codeModeCapability))
			}
			effectiveSearch := searchEnabled && option.supportsNativeWebSearch()
			if searchEnabled && !effectiveSearch {
				fmt.Fprintf(os.Stderr, "sparktea: %s has no native web search — ignored\n", option.provider)
			} else if effectiveSearch {
				runOpts = append(runOpts, ai.WithRunNativeTools(ai.WebSearchTool{Optional: true}))
			}

			fmt.Fprintf(os.Stderr, "\n=== turn %d: %s:%s (search=%v code=%v) ===\n",
				turn, option.provider, option.modelID, effectiveSearch, codeEnabled)
			messages, err := runTurn(ctx, agent, option, step.arg, history, runOpts, "script",
				"web_search", effectiveSearch, "code_mode", codeEnabled, "script_turn", turn)
			if err != nil {
				return fmt.Errorf("turn %d: %w", turn, err)
			}
			history = messages
		}
	}
	return nil
}
