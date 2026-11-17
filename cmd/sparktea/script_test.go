package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/mdfranz/sparktea/codemode"
)

func TestParseScript(t *testing.T) {
	src := `
# a comment, and a blank line above are both ignored
Work through this step by step: what's 17% of 340?

/search on
Thanks, that's helpful.
/search off
/code on
/model anthropic:claude-opus-5
/clear
One more, on a fresh history.
`
	steps, err := parseScript(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []scriptStep{
		{kind: "prompt", arg: "Work through this step by step: what's 17% of 340?"},
		{kind: "search", arg: "on"},
		{kind: "prompt", arg: "Thanks, that's helpful."},
		{kind: "search", arg: "off"},
		{kind: "code", arg: "on"},
		{kind: "model", arg: "anthropic:claude-opus-5"},
		{kind: "clear"},
		{kind: "prompt", arg: "One more, on a fresh history."},
	}
	if len(steps) != len(want) {
		t.Fatalf("parseScript() = %d steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i, w := range want {
		if steps[i] != w {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], w)
		}
	}
}

func TestParseScriptErrors(t *testing.T) {
	for name, src := range map[string]string{
		"/model no arg":     "/model",
		"/model extra arg":  "/model a b",
		"/search no arg":    "/search",
		"/search bad value": "/search maybe",
		"/code bad value":   "/code sometimes",
		"/clear with arg":   "/clear now",
		"unknown command":   "/frobnicate",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseScript(strings.NewReader(src)); err == nil {
				t.Errorf("parseScript(%q) succeeded, want an error", src)
			}
		})
	}
}

func TestPromptAndScriptMutuallyExclusive(t *testing.T) {
	if _, err := parseCLIFlags([]string{"-prompt", "hi", "-script", "steps.txt"}); err == nil {
		t.Error("-prompt and -script together succeeded, want an error")
	}
}

func TestCodeBackendFlag(t *testing.T) {
	opts, err := parseCLIFlags([]string{"-prompt", "hi"})
	if err != nil || opts.codeBackend != codemode.BackendMonty {
		t.Fatalf("default backend = %q, %v; want monty", opts.codeBackend, err)
	}
	opts, err = parseCLIFlags([]string{"-prompt", "hi", "-code", "-code-backend", "cpython"})
	if err != nil || opts.codeBackend != codemode.BackendCPython {
		t.Fatalf("-code-backend cpython = %q, %v", opts.codeBackend, err)
	}
	if _, err := parseCLIFlags([]string{"-code-backend", "python"}); err == nil {
		t.Error("-code-backend python succeeded, want an error")
	}
}

func TestMontyRemoteFlagAndSecretHygiene(t *testing.T) {
	t.Setenv(codemode.EnvMontyRemoteEndpoint, "")
	t.Setenv(codemode.EnvMontyRemoteToken, "")
	t.Setenv(codemode.EnvMontyRemoteInsecure, "")

	opts, err := parseCLIFlags([]string{"-prompt", "hi", "-monty-remote", " 127.0.0.1:50051 "})
	if err != nil {
		t.Fatalf("parseCLIFlags(-monty-remote): %v", err)
	}
	if opts.montyRemote != "127.0.0.1:50051" {
		t.Fatalf("montyRemote = %q, want 127.0.0.1:50051", opts.montyRemote)
	}
	if _, err := parseCLIFlags([]string{"-code", "-code-backend", "cpython", "-monty-remote", "127.0.0.1:50051"}); err == nil {
		t.Fatal("-monty-remote with -code-backend cpython succeeded, want an error")
	}

	const secretToken = "monty-secret-token-should-never-persist"
	t.Setenv(codemode.EnvMontyRemoteEndpoint, "worker.example.com:443")
	t.Setenv(codemode.EnvMontyRemoteToken, secretToken)

	montyCM := newCodeMode(codemode.BackendMonty, "")
	if !montyCM.IsRemote() {
		t.Fatal("newCodeMode(monty) did not pick up MONTY_REMOTE_ENDPOINT")
	}
	cpythonCM := newCodeMode(codemode.BackendCPython, "")
	if cpythonCM.IsRemote() {
		t.Fatal("newCodeMode(cpython) unexpectedly reported IsRemote() == true")
	}

	// Verify local logs and /save session files never contain the auth token or remote config.
	home := t.TempDir()
	t.Setenv("HOME", home)

	logDir := filepath.Join(home, "logs")
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.Local)
	logger, err := newLocalLogger(logDir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newLocalLogger: %v", err)
	}
	logger.event(slog.LevelInfo, "turn_started", "mode", "one_shot", "code_mode", true, "code_backend", string(codemode.BackendMonty), "monty_remote", montyCM.IsRemote())
	if err := logger.close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	logBytes, err := os.ReadFile(filepath.Join(logDir, "sparktea-2026-11-13.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	if strings.Contains(string(logBytes), secretToken) {
		t.Fatalf("local log leaked %s: %s", codemode.EnvMontyRemoteToken, logBytes)
	}

	sessionPath, err := writeSessionFile("remote_check", sessionSnapshot{
		Messages:    []ai.ModelMessage{ai.ModelRequest{Parts: []ai.RequestPart{ai.UserPromptPart{Content: "hi"}}}},
		Option:      modelOption{label: "Test", provider: providerOpenRouter, modelID: "test/model"},
		CodeEnabled: true,
	})
	if err != nil {
		t.Fatalf("writeSessionFile: %v", err)
	}
	sessionBytes, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("ReadFile(session): %v", err)
	}
	if strings.Contains(string(sessionBytes), secretToken) || strings.Contains(string(sessionBytes), "worker.example.com:443") {
		t.Fatalf("session file leaked remote config/token: %s", sessionBytes)
	}
}
