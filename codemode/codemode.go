// Package codemode wraps Monty (via the gomonty Go bindings) as a
// pydantic-ai-go ai.Capability. It gives the model a single run_code tool:
// write Python, get the result back, with no filesystem/network/env access
// and no host tool callbacks (that's a fast-follow, not this MVP).
package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Kludex/pydantic-ai-go/ai"
	monty "github.com/ewhauser/gomonty"
)

// defaultLimits are conservative enough that a runaway script can't stall
// the TUI. MaxSuspensions is a backstop against a script looping on host
// calls; it doesn't otherwise matter in the MVP since RunOptions.Functions
// is never set, so nothing exists for a script to suspend on.
func defaultLimits() *monty.ResourceLimits {
	return &monty.ResourceLimits{
		MaxDuration:       5 * time.Second,
		MaxMemory:         64 << 20, // 64 MiB
		MaxRecursionDepth: 100,
		MaxSuspensions:    10,
	}
}

// CodeMode is an ai.Capability adding a run_code tool that executes Python
// in a Monty sandbox.
type CodeMode struct {
	limits *monty.ResourceLimits
}

// Option configures a CodeMode built by New.
type Option func(*CodeMode)

// WithLimits overrides the default resource limits.
func WithLimits(l monty.ResourceLimits) Option {
	return func(c *CodeMode) { c.limits = &l }
}

// New builds a CodeMode capability with default (or overridden) limits.
func New(opts ...Option) *CodeMode {
	c := &CodeMode{limits: defaultLimits()}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Setup implements ai.Capability. It only adds a tool — it never hides or
// replaces anything another capability contributed.
func (c *CodeMode) Setup(reg *ai.CapabilityRegistry) error {
	reg.AddTool(runCodeDefinition(), c.handleRunCode)
	return nil
}

func runCodeDefinition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name: "run_code",
		Description: "Run Python in a sandboxed interpreter (no filesystem, network, " +
			"or environment access) and get back the result. Write plain Python — " +
			"the value of the last expression is returned automatically, no need to " +
			"print() it. Only a small stdlib subset is available: sys, typing, math, " +
			"json, re, unicodedata, datetime, pathlib. Third-party imports are " +
			"rejected. Use this for calculations, loops, or data wrangling that's " +
			"easier to write as code than to reason through step by step.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code": map[string]any{
					"type":        "string",
					"description": "Python source to run.",
				},
			},
			"required":             []string{"code"},
			"additionalProperties": false,
		},
	}
}

func (c *CodeMode) handleRunCode(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("codemode: decode run_code args: %w", err)
	}

	runner, err := monty.New(args.Code, monty.CompileOptions{ScriptName: "run_code.py"})
	if err != nil {
		// Syntax error: feed it back to the model as tool content so it can
		// retry with corrected code, rather than hard-failing the run.
		return formatMontyError(err), nil
	}

	var stdout strings.Builder
	value, err := runner.Run(ctx, monty.RunOptions{
		Print:  monty.WriterPrintCallback(&stdout),
		Limits: c.limits,
	})
	if err != nil {
		// Runtime error (including a resource-limit violation) or a typing
		// error: same treatment as a syntax error above.
		return formatMontyError(err), nil
	}
	return composeResult(stdout.String(), value), nil
}

// formatMontyError turns one of gomonty's typed errors (*monty.SyntaxError,
// *monty.RuntimeError, *monty.TypingError) into tool content the model can
// read and react to.
func formatMontyError(err error) map[string]any {
	return map[string]any{"error": err.Error()}
}

// composeResult mirrors upstream Code Mode's result semantics.
func composeResult(output string, value monty.Value) any {
	isNone := value.Kind() == "none"
	switch {
	case output != "" && !isNone:
		return map[string]any{"output": output, "result": flattenValue(value)}
	case output != "":
		return map[string]any{"output": output}
	case !isNone:
		return flattenValue(value)
	default:
		return map[string]any{}
	}
}
