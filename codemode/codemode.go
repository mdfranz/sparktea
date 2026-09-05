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

// ToolName is run_code's model-facing tool name, exported so callers (e.g.
// cmd/sparktea/chat.go's stream-event handler) can match on it without
// duplicating the literal.
const ToolName = "run_code"

// runCodeMaxRetries overrides pydantic-ai-go's tool-wide default retry
// budget (1, cumulative for the whole run — see WithToolMaxRetries — never
// reset by a later success). A run_code failure is routine (a script's own
// bug), not the model misunderstanding the tool's schema, so the default is
// far too tight for legitimate iterative debugging. This is generous enough
// not to get in the way of that, while still bounding a model that's
// genuinely stuck in a broken-code loop: past this many cumulative
// failures, pydantic-ai-go hard-fails the run with ErrMaxRetriesExceeded
// instead of feeding the error back forever.
const runCodeMaxRetries = 20

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
	def := ai.ToolDefinition{
		Name: ToolName,
		Description: "Run Python in a sandboxed interpreter (Monty, not CPython) and get " +
			"back the result. The last expression's value returns automatically — no " +
			"need to print() it.\n\n" +
			"No filesystem, network, or environment access. os and pathlib import fine, " +
			"but any real OS call (os.getenv, Path.exists, file I/O, ...) raises " +
			"NotImplementedError — there's nothing to touch.\n\n" +
			"Only part of the stdlib exists, each module covering a slice of CPython's " +
			"surface: sys, typing, math, json, re, unicodedata, datetime, pathlib, os, " +
			"collections, itertools, functools, dataclasses, asyncio, base64, binascii. " +
			"No third-party imports. Notably NOT available: statistics, random, time, " +
			"enum, copy, string, io, struct, hashlib, uuid, and anything " +
			"network/process/thread-related (urllib, socket, subprocess, threading) — " +
			"compute statistics and pick values with plain arithmetic/math instead of " +
			"importing statistics/random.\n\n" +
			"Also unsupported: class inheritance, @classmethod/@staticmethod/@property, " +
			"user-defined exception classes, eval/exec, yield. '%'-style string " +
			"formatting fails — use an f-string or .format() instead.\n\n" +
			"A bad script's error comes back as a message, not a hard failure — read it, " +
			"fix the code, and call run_code again. Good for calculations, loops, or data " +
			"wrangling that's easier to write than to reason through step by step.",
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
	ai.WithToolMaxRetries(runCodeMaxRetries)(&def)
	return def
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
		// Syntax error: report it as an ai.RetryError rather than a plain Go
		// error. pydantic-ai-go converts that into a retry prompt fed back
		// to the model — same "let it see the mistake and retry" intent as
		// this package's own tool content, but through the framework's own
		// mechanism, so runCodeMaxRetries actually bounds it instead of the
		// model being able to retry unboundedly.
		return nil, ai.Retryf("%s", err.Error())
	}

	var stdout strings.Builder
	value, err := runner.Run(ctx, monty.RunOptions{
		Print:  monty.WriterPrintCallback(&stdout),
		Limits: c.limits,
	})
	if err != nil {
		// Runtime error (including a resource-limit violation) or a typing
		// error: same treatment as a syntax error above.
		return nil, ai.Retryf("%s", err.Error())
	}
	return composeResult(stdout.String(), value), nil
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
