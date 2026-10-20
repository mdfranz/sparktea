package codemode

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kludex/pydantic-ai-go/ai"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Backend selects what executes run_code's Python.
type Backend string

const (
	// BackendMonty runs code in the Monty sandbox (the default).
	BackendMonty Backend = "monty"
	// BackendCPython runs code in local CPython through a pydantic-ai-go
	// LocalWorkspace. It is not a sandbox: scripts run as the sparktea
	// user, with network access and the host filesystem reachable. It
	// exists to compare models' Code Mode behavior without Monty's limits.
	BackendCPython Backend = "cpython"
)

// ParseBackend validates a backend name from a flag or command.
func ParseBackend(s string) (Backend, error) {
	switch b := Backend(s); b {
	case BackendMonty, BackendCPython:
		return b, nil
	default:
		return "", fmt.Errorf("unknown code backend %q (want %q or %q)", s, BackendMonty, BackendCPython)
	}
}

// WithBackend selects the backend that executes run_code.
func WithBackend(b Backend) Option {
	return func(c *CodeMode) { c.backend = b }
}

// WithPython sets the Python interpreter for BackendCPython (default
// "python3", looked up on PATH).
func WithPython(path string) Option {
	return func(c *CodeMode) { c.python = path }
}

const (
	// pythonTimeout and pythonMaxMemory match Monty's default limits so the
	// two backends differ in interpreter, not budget. CPython's own
	// recursion limit (1000) is left alone: that difference is part of what
	// the backend comparison measures.
	pythonTimeout   = 5 * time.Second
	pythonMaxMemory = 512 << 20 // address space; CPython needs more headroom than Monty's 64 MiB heap
	pythonScript    = "run_code.py"
)

// pythonRunner is passed to python3 -I -c. It runs run_code.py from the
// working directory with print output captured, evaluates a trailing
// expression like Monty does, and writes one JSON line to stdout. Errors go
// to stderr with the exception line first, then the traceback for
// run_code.py frames only, and exit status 1.
//
//go:embed python_runner.py
var pythonRunner string

func runCodePythonDefinition() ai.ToolDefinition {
	def := runCodeDefinition()
	def.Description = pythonDescription
	return def
}

func (c *CodeMode) handleRunPython(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("codemode: decode run_code args: %w", err)
	}

	dir, err := os.MkdirTemp("", "sparktea-run-code-")
	if err != nil {
		return nil, fmt.Errorf("codemode: scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, pythonScript), []byte(args.Code), 0o600); err != nil {
		return nil, fmt.Errorf("codemode: write script: %w", err)
	}

	// The local backend passes only PATH, HOME and locale variables through,
	// so provider API keys and LOGFIRE_TOKEN never reach the script.
	backend, err := ai.NewLocalWorkspaceBackend(dir, map[string]string{
		"HOME":                dir,
		"SPARKTEA_MAX_MEMORY": fmt.Sprint(pythonMaxMemory),
	})
	if err != nil {
		return nil, fmt.Errorf("codemode: workspace: %w", err)
	}
	ctx, span := c.tracer.Start(ctx, "python.run", trace.WithAttributes(
		attribute.String("python.script_name", pythonScript),
		attribute.String("python.interpreter", c.python),
		attribute.Int("python.code_bytes", len(args.Code)),
	))
	defer span.End()

	result, err := backend.Run(ctx, ai.WorkspaceCommand{
		Args:    []string{c.python, "-I", "-c", pythonRunner},
		Timeout: pythonTimeout,
	})
	timedOut := errors.Is(err, ai.ErrWorkspaceTimeout)
	span.SetAttributes(attribute.Bool("python.timed_out", timedOut))
	if err == nil {
		span.SetAttributes(attribute.Int("python.exit_code", result.ExitCode))
	}
	switch {
	case timedOut:
		msg := fmt.Sprintf("TimeoutError: script exceeded %s", pythonTimeout)
		span.SetStatus(codes.Error, msg)
		return nil, ai.Retryf("%s", msg)
	case err != nil:
		span.SetStatus(codes.Error, err.Error())
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("codemode: run python: %w", err)
	case result.ExitCode != 0:
		msg := strings.TrimSpace(result.Stderr)
		if result.ExitCode != 1 || msg == "" {
			msg = strings.TrimSpace(fmt.Sprintf("RuntimeError: python exited with status %d\n%s", result.ExitCode, msg))
		}
		// The exception line comes first, so it doubles as the span status;
		// without content, keep only the exception type.
		status, _, _ := strings.Cut(msg, "\n")
		if !c.telemetryOptions.RecordOutputs {
			status, _, _ = strings.Cut(status, ":")
		}
		span.SetStatus(codes.Error, truncate(status, 200))
		if c.telemetryOptions.RecordOutputs {
			span.SetAttributes(attribute.String("python.stderr", truncate(result.Stderr, pythonSpanTextLimit)))
		}
		return nil, ai.Retryf("%s", msg)
	}
	value, output, execMS, err := parsePythonResult(result.Stdout)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	span.SetAttributes(attribute.Float64("python.exec_ms", execMS))
	if c.telemetryOptions.RecordOutputs && output != "" {
		span.SetAttributes(attribute.String("python.output", truncate(output, pythonSpanTextLimit)))
	}
	return value, nil
}

// pythonSpanTextLimit bounds stderr and print output copied onto the
// python.run span.
const pythonSpanTextLimit = 4096

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parsePythonResult decodes the runner's JSON line into run_code's result
// shape (see composeResult), and also returns the print output and the
// script's own execution time for telemetry. Numbers stay json.Number so big
// integers keep every digit.
func parsePythonResult(stdout string) (result any, output string, execMS float64, err error) {
	stdout = strings.TrimRight(stdout, "\n")
	line := stdout[strings.LastIndexByte(stdout, '\n')+1:]
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var payload struct {
		Output string          `json:"output"`
		Value  json.RawMessage `json:"value"`
		ExecMS float64         `json:"exec_ms"`
	}
	if err := dec.Decode(&payload); err != nil {
		return nil, "", 0, ai.Retryf("RuntimeError: unreadable result from python: %.200q", stdout)
	}
	var value any
	if len(payload.Value) > 0 {
		vdec := json.NewDecoder(bytes.NewReader(payload.Value))
		vdec.UseNumber()
		if err := vdec.Decode(&value); err != nil {
			return nil, "", 0, fmt.Errorf("codemode: decode python value: %w", err)
		}
	}
	switch {
	case payload.Output != "" && value != nil:
		result = map[string]any{"output": payload.Output, "result": value}
	case payload.Output != "":
		result = map[string]any{"output": payload.Output}
	case value != nil:
		result = value
	default:
		result = map[string]any{}
	}
	return result, payload.Output, payload.ExecMS, nil
}
