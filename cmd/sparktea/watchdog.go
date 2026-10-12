package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// stallTimeout is how long a turn may go without any agent event (a text,
// thinking or tool-call chunk, a tool call or result) before the watchdog
// cancels it. Providers sometimes stop sending mid-stream without closing
// the connection: OpenRouter keeps it alive with SSE comment lines, which
// produce no events, and the request then hangs for ~10 minutes until the
// connection resets (seen with DeepSeek and GLM, 2026-10-10 and -12). Tools
// are bounded well below this (run_code stops scripts after 5 s). The
// margin is for reasoning models that think without streaming anything.
// SPARKTEA_STALL_TIMEOUT overrides it (a Go duration; 0 disables the
// watchdog). A var so tests can shorten it.
var stallTimeout = stallTimeoutFromEnv(os.Getenv("SPARKTEA_STALL_TIMEOUT"))

const defaultStallTimeout = 120 * time.Second

func stallTimeoutFromEnv(v string) time.Duration {
	if v == "" {
		return defaultStallTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		fmt.Fprintf(os.Stderr, "sparktea: ignoring SPARKTEA_STALL_TIMEOUT=%q (want a duration like 3m, or 0 to disable)\n", v)
		return defaultStallTimeout
	}
	return d
}

// errStalled is the cause recorded when the watchdog cancels a turn.
var errStalled = errors.New("model stream stalled")

// stallWatchdog cancels a turn's context when no event arrives within
// stallTimeout. Call reset on every event and stop when the turn ends.
type stallWatchdog struct {
	ctx     context.Context
	timer   *time.Timer
	timeout time.Duration
	cancel  context.CancelCauseFunc
}

func newStallWatchdog(ctx context.Context) *stallWatchdog {
	ctx, cancel := context.WithCancelCause(ctx)
	w := &stallWatchdog{ctx: ctx, timeout: stallTimeout, cancel: cancel}
	if w.timeout > 0 {
		w.timer = time.AfterFunc(w.timeout, func() {
			cancel(fmt.Errorf("%w: no response from the model for %s", errStalled, w.timeout))
		})
	}
	return w
}

// Context is the context to run the turn with.
func (w *stallWatchdog) Context() context.Context { return w.ctx }

func (w *stallWatchdog) reset() {
	if w.timer != nil {
		w.timer.Reset(w.timeout)
	}
}

func (w *stallWatchdog) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.cancel(nil)
}

// err replaces err with the stall cause when the watchdog fired, so callers
// report "model stream stalled" instead of a bare "context canceled".
func (w *stallWatchdog) err(err error) error {
	if cause := context.Cause(w.ctx); err != nil && errors.Is(cause, errStalled) {
		return cause
	}
	return err
}
