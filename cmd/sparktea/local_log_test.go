package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalLoggerWritesPrivateJSONLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	now := time.Date(2026, time.September, 5, 13, 0, 0, 0, time.Local)
	l, err := newLocalLogger(dir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newLocalLogger: %v", err)
	}
	l.event(slog.LevelInfo, "turn_completed", "provider", "openrouter", "model", "example", "input_tokens", 37)
	if err := l.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	file := filepath.Join(dir, "sparktea-2026-09-05.jsonl")
	checkPrivateMode(t, dir, 0o700)
	checkPrivateMode(t, file, 0o600)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("log line has no trailing newline: %q", data)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("invalid JSON line %q: %v", data, err)
	}
	if entry["msg"] != "turn_completed" || entry["provider"] != "openrouter" || entry["input_tokens"] != float64(37) {
		t.Fatalf("unexpected log entry: %#v", entry)
	}
	for _, forbidden := range []string{"prompt", "response", "thinking", "tool_args", "tool_result", "api_key"} {
		if _, ok := entry[forbidden]; ok {
			t.Errorf("log entry contains forbidden field %q: %#v", forbidden, entry)
		}
	}
}

func TestLocalLoggerRotatesByLocalDay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	now := time.Date(2026, time.September, 5, 23, 59, 0, 0, time.Local)
	l, err := newLocalLogger(dir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newLocalLogger: %v", err)
	}
	l.event(slog.LevelInfo, "first")
	now = now.Add(2 * time.Minute)
	l.event(slog.LevelInfo, "second")
	if err := l.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, date := range []string{"2026-09-05", "2026-09-06"} {
		data, err := os.ReadFile(filepath.Join(dir, "sparktea-"+date+".jsonl"))
		if err != nil {
			t.Fatalf("read %s log: %v", date, err)
		}
		if !strings.Contains(string(data), `"msg"`) {
			t.Fatalf("%s log lacks an event: %q", date, data)
		}
	}
}

func checkPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s permissions = %o, want %o", path, got, want)
	}
}
