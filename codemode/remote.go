package codemode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Kludex/pydantic-ai-go/ai"
	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
)

// Environment variables configuring optional remote Monty worker execution.
const (
	EnvMontyRemoteEndpoint = "MONTY_REMOTE_ENDPOINT"
	EnvMontyRemoteToken    = "MONTY_REMOTE_TOKEN"
	EnvMontyRemoteInsecure = "MONTY_REMOTE_INSECURE"
)

// RemoteConfig configures optional remote Monty worker execution for BackendMonty.
// Token is read from the environment and must never be logged or persisted.
type RemoteConfig struct {
	Endpoint string
	Token    string
	Insecure bool
}

// LogValue implements slog.LogValuer so Token is never emitted to structured logs.
func (c RemoteConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("endpoint", c.Endpoint),
		slog.Bool("insecure", c.Insecure),
		slog.Bool("has_token", c.Token != ""),
	)
}

// String redacts Token if RemoteConfig is formatted for display or debugging.
func (c RemoteConfig) String() string {
	if c.Endpoint == "" {
		return "RemoteConfig{}"
	}
	return fmt.Sprintf("RemoteConfig{Endpoint:%q, Insecure:%t, HasToken:%t}", c.Endpoint, c.Insecure, c.Token != "")
}

// Format implements fmt.Formatter so %+v and %#v also redact Token.
func (c RemoteConfig) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, c.String())
}

// RemoteConfigFromEnv resolves remote worker settings from endpointFlag (falling
// back to MONTY_REMOTE_ENDPOINT) and MONTY_REMOTE_TOKEN / MONTY_REMOTE_INSECURE.
func RemoteConfigFromEnv(endpointFlag string) RemoteConfig {
	endpoint := strings.TrimSpace(endpointFlag)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv(EnvMontyRemoteEndpoint))
	}
	return RemoteConfig{
		Endpoint: endpoint,
		Token:    os.Getenv(EnvMontyRemoteToken),
		Insecure: os.Getenv(EnvMontyRemoteInsecure) == "1",
	}
}

// WithRemote configures BackendMonty to run code on a remote Monty worker
// instead of the in-process interpreter. An empty Endpoint disables it.
func WithRemote(cfg RemoteConfig) Option {
	return func(c *CodeMode) {
		cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
		c.remote = cfg
	}
}

// WithMontyBackend injects an explicit monty.Backend (for example a fake worker
// in tests) to execute BackendMonty snippets over NewRepl/FeedRun.
func WithMontyBackend(b monty.Backend) Option {
	return func(c *CodeMode) {
		c.montyBackend = b
	}
}

// IsRemote reports whether BackendMonty is configured to use a remote worker
// or an explicit monty.Backend instead of the local in-process interpreter.
func (c *CodeMode) IsRemote() bool {
	return c.backend == BackendMonty && (c.remote.Endpoint != "" || c.montyBackend != nil)
}

// replTrackingBackend wraps a monty.Backend so runRemote can close the latest
// unconsumed ReplHandle deterministically when the tool call ends.
type replTrackingBackend struct {
	monty.Backend
	nextID    uint64
	currentID uint64
	current   monty.ReplHandle
}

func (b *replTrackingBackend) setCurrent(h monty.ReplHandle) monty.ReplHandle {
	if h == nil {
		return nil
	}
	b.nextID++
	id := b.nextID
	b.currentID = id
	b.current = h
	return &trackedReplHandle{ReplHandle: h, tracker: b, id: id}
}

func (b *replTrackingBackend) clearID(id uint64) {
	if b.currentID == id {
		b.currentID = 0
		b.current = nil
	}
}

func (b *replTrackingBackend) wrapStep(st monty.Step) monty.Step {
	if st.Repl != nil {
		st.Repl = b.setCurrent(st.Repl)
	}
	if st.Progress != nil {
		st.Progress = &trackedProgressHandle{ProgressHandle: st.Progress, tracker: b}
	}
	return st
}

func (b *replTrackingBackend) NewRepl(opts monty.ReplOptions) (monty.ReplHandle, error) {
	h, err := b.Backend.NewRepl(opts)
	if err != nil {
		return nil, err
	}
	return b.setCurrent(h), nil
}

func (b *replTrackingBackend) close() {
	if b.current != nil {
		b.current.Close()
		b.current = nil
		b.currentID = 0
	}
}

type trackedReplHandle struct {
	monty.ReplHandle
	tracker *replTrackingBackend
	id      uint64
}

func (h *trackedReplHandle) Close() {
	h.tracker.clearID(h.id)
	h.ReplHandle.Close()
}

func (h *trackedReplHandle) FeedStart(ctx context.Context, code string, opts monty.FeedStartOptions) monty.Step {
	st := h.ReplHandle.FeedStart(ctx, code, opts)
	if !st.Retained {
		h.tracker.clearID(h.id)
	}
	return h.tracker.wrapStep(st)
}

type trackedProgressHandle struct {
	monty.ProgressHandle
	tracker *replTrackingBackend
}

func (p *trackedProgressHandle) TakeRepl() (monty.ReplHandle, error) {
	r, err := p.ProgressHandle.TakeRepl()
	if err != nil {
		return nil, err
	}
	return p.tracker.setCurrent(r), nil
}

func (p *trackedProgressHandle) ResumeCall(ctx context.Context, result monty.Result) monty.Step {
	return p.tracker.wrapStep(p.ProgressHandle.ResumeCall(ctx, result))
}

func (p *trackedProgressHandle) ResumeLookup(ctx context.Context, value monty.Value, found bool) monty.Step {
	return p.tracker.wrapStep(p.ProgressHandle.ResumeLookup(ctx, value, found))
}

func (p *trackedProgressHandle) ResumeFutures(ctx context.Context, results map[uint32]monty.Result) monty.Step {
	return p.tracker.wrapStep(p.ProgressHandle.ResumeFutures(ctx, results))
}

func (c *CodeMode) runRemote(ctx context.Context, code string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("codemode: remote worker: %w", err)
	}

	backend := c.montyBackend
	if backend == nil {
		transport, err := remote.DialGRPC(c.remote.Endpoint, remote.GRPCConfig{
			Token:    c.remote.Token,
			Insecure: c.remote.Insecure,
		})
		if err != nil {
			return nil, fmt.Errorf("codemode: remote worker: %w", err)
		}
		defer func() { _ = transport.Close() }()

		var opts []remote.Option
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining > 0 {
				opts = append(opts, remote.WithOpenTimeout(remaining))
			}
		}
		backend = remote.NewBackend(transport, opts...)
	}

	tracker := &replTrackingBackend{Backend: backend}
	defer tracker.close()

	repl, err := monty.NewRepl(monty.ReplOptions{
		ScriptName: "run_code.py",
		Limits:     c.limits,
		Backend:    tracker,
	})
	if err != nil {
		return nil, fmt.Errorf("codemode: remote worker: %w", err)
	}

	var stdout bytes.Buffer
	value, err := repl.FeedRun(ctx, code, monty.FeedOptions{
		SkipTypeCheck:    true,
		Print:            monty.WriterPrintCallback(&stdout),
		Telemetry:        c.telemetry,
		TelemetryOptions: c.telemetryOptions,
	})
	if err != nil {
		if isRemoteScriptError(err) {
			return nil, ai.Retryf("%s", err.Error())
		}
		return nil, fmt.Errorf("codemode: remote worker: %w", err)
	}
	return composeResult(stdout.String(), value), nil
}

// isRemoteScriptError reports errors raised by the model's Python script.
// Transport, auth, and session-refusal failures return false so they abort the
// turn rather than retrying as script errors or falling back locally.
func isRemoteScriptError(err error) bool {
	var (
		rPy    *remote.PythonError
		rType  *remote.TypingError
		lRT    *monty.RuntimeError
		lSyn   *monty.SyntaxError
		lType  *monty.TypingError
	)
	return errors.As(err, &rPy) ||
		errors.As(err, &rType) ||
		errors.As(err, &lRT) ||
		errors.As(err, &lSyn) ||
		errors.As(err, &lType)
}
