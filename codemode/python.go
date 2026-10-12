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
)

// Backend selects what executes run_code's Python.
type Backend string

const (
	// BackendMonty runs code in the Monty sandbox (the default).
	BackendMonty Backend = "monty"
	// BackendPython runs code in local CPython through a pydantic-ai-go
	// LocalWorkspace. It is not a sandbox: scripts run as the sparktea
	// user, with network access and the host filesystem reachable. It
	// exists to compare models' Code Mode behavior without Monty's limits.
	BackendPython Backend = "python"
)

// ParseBackend validates a backend name from a flag or command.
func ParseBackend(s string) (Backend, error) {
	switch b := Backend(s); b {
	case BackendMonty, BackendPython:
		return b, nil
	default:
		return "", fmt.Errorf("unknown code backend %q (want %q or %q)", s, BackendMonty, BackendPython)
	}
}

// WithBackend selects the backend that executes run_code.
func WithBackend(b Backend) Option {
	return func(c *CodeMode) { c.backend = b }
}

// WithPython sets the Python interpreter for BackendPython (default
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
	def.Description = "Run Python (CPython 3) locally and get back the result. The last " +
		"expression's value returns automatically — no need to print() it.\n\n" +
		"The full standard library is available; third-party packages may not be. " +
		"Each call runs in a fresh process and a fresh scratch directory, so nothing " +
		"carries over between calls. A call is stopped after 5 seconds.\n\n" +
		"A bad script's error comes back as a message, not a hard failure — read it, " +
		"fix the code, and call run_code again. Good for calculations, loops, or data " +
		"wrangling that's easier to write than to reason through step by step."
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
	defer os.RemoveAll(dir)
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
	result, err := backend.Run(ctx, ai.WorkspaceCommand{
		Args:    []string{c.python, "-I", "-c", pythonRunner},
		Timeout: pythonTimeout,
	})
	switch {
	case errors.Is(err, ai.ErrWorkspaceTimeout):
		return nil, ai.Retryf("TimeoutError: script exceeded %s", pythonTimeout)
	case err != nil:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("codemode: run python: %w", err)
	case result.ExitCode != 0:
		msg := strings.TrimSpace(result.Stderr)
		if result.ExitCode != 1 || msg == "" {
			msg = strings.TrimSpace(fmt.Sprintf("RuntimeError: python exited with status %d\n%s", result.ExitCode, msg))
		}
		return nil, ai.Retryf("%s", msg)
	}
	return parsePythonResult(result.Stdout)
}

// parsePythonResult decodes the runner's JSON line into run_code's result
// shape (see composeResult). Numbers stay json.Number so big integers keep
// every digit.
func parsePythonResult(stdout string) (any, error) {
	stdout = strings.TrimRight(stdout, "\n")
	line := stdout[strings.LastIndexByte(stdout, '\n')+1:]
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var payload struct {
		Output string          `json:"output"`
		Value  json.RawMessage `json:"value"`
	}
	if err := dec.Decode(&payload); err != nil {
		return nil, ai.Retryf("RuntimeError: unreadable result from python: %.200q", stdout)
	}
	var value any
	if len(payload.Value) > 0 {
		vdec := json.NewDecoder(bytes.NewReader(payload.Value))
		vdec.UseNumber()
		if err := vdec.Decode(&value); err != nil {
			return nil, fmt.Errorf("codemode: decode python value: %w", err)
		}
	}
	switch {
	case payload.Output != "" && value != nil:
		return map[string]any{"output": payload.Output, "result": value}, nil
	case payload.Output != "":
		return map[string]any{"output": payload.Output}, nil
	case value != nil:
		return value, nil
	default:
		return map[string]any{}, nil
	}
}
