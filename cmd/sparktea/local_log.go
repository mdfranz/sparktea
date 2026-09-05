package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	ai "github.com/Kludex/pydantic-ai-go/ai"
)

// localLogger writes operational diagnostics to one private JSON Lines file
// per local calendar day. It deliberately receives only metadata: callers
// must never pass prompts, responses, tool payloads, or credentials.
type localLogger struct {
	mu   sync.Mutex
	dir  string
	now  func() time.Time
	date string
	file *os.File
	log  *slog.Logger
}

func newLocalLogger(dir string, now func() time.Time) (*localLogger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	l := &localLogger{dir: dir, now: now}
	l.mu.Lock()
	err := l.rotateLocked(now())
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return l, nil
}

func newDiscardLocalLogger() *localLogger {
	return &localLogger{log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
}

func (l *localLogger) rotateLocked(now time.Time) error {
	date := now.Format("2006-01-02")
	if l.file != nil && l.date == date {
		return nil
	}
	path := filepath.Join(l.dir, "sparktea-"+date+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	l.file = f
	l.date = date
	l.log = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return nil
}

func (l *localLogger) event(level slog.Level, name string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.now != nil {
		if err := l.rotateLocked(l.now()); err != nil {
			return
		}
	}
	if l.log != nil {
		l.log.Log(context.Background(), level, name, args...)
	}
}

func (l *localLogger) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func localLogsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sparktea", "logs"), nil
}

// localLog is always safe to use. It starts as a discard logger so logging
// never changes application behavior if filesystem setup fails.
var localLog = newDiscardLocalLogger()

func initLocalLog() (func() error, error) {
	dir, err := localLogsDir()
	if err != nil {
		return func() error { return nil }, err
	}
	l, err := newLocalLogger(dir, time.Now)
	if err != nil {
		return func() error { return nil }, err
	}
	localLog = l
	return l.close, nil
}

func logLocal(level slog.Level, name string, args ...any) {
	localLog.event(level, name, args...)
}

func logLocalError(name string, err error, args ...any) {
	args = append(args, "error_type", errorType(err))
	logLocal(slog.LevelError, name, args...)
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

func usageLogArgs(u ai.Usage) []any {
	args := []any{
		"requests", u.Requests,
		"input_tokens", u.InputTokens,
		"output_tokens", u.OutputTokens,
		"reasoning_tokens", u.ReasoningTokens,
		"tool_calls", u.ToolCalls,
	}
	if u.CostUSD != nil {
		args = append(args, "cost_usd", *u.CostUSD)
	}
	return args
}
