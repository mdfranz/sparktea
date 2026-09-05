package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
)

const defaultSessionName = "default"

var sessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// sessionsDir is where /save and /load keep conversation history, one JSON
// file per named session.
func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sparktea", "sessions"), nil
}

func sessionPath(name string) (string, error) {
	if name == "" {
		name = defaultSessionName
	}
	if !sessionNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid session name %q (use letters, digits, - or _)", name)
	}
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// writeSessionFile serializes messages with pydantic-ai-go's own message
// codec and writes it to the named session file.
func writeSessionFile(name string, messages []ai.ModelMessage) (string, error) {
	path, err := sessionPath(name)
	if err != nil {
		return "", err
	}
	data, err := ai.MarshalMessages(messages)
	if err != nil {
		return "", fmt.Errorf("encode session: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// readSessionFile loads a named session file back into message history.
func readSessionFile(name string) ([]ai.ModelMessage, string, error) {
	path, err := sessionPath(name)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("no saved session named %q", strings.TrimSuffix(filepath.Base(path), ".json"))
		}
		return nil, "", err
	}
	messages, err := ai.UnmarshalMessages(data)
	if err != nil {
		return nil, "", fmt.Errorf("decode session: %w", err)
	}
	return messages, path, nil
}

// transcriptFromMessages rebuilds display transcript entries from loaded
// message history, so /load shows prior turns instead of an empty screen.
func transcriptFromMessages(messages []ai.ModelMessage) []transcriptEntry {
	var out []transcriptEntry
	for _, msg := range messages {
		switch m := msg.(type) {
		case ai.ModelRequest:
			for _, part := range m.Parts {
				up, ok := part.(ai.UserPromptPart)
				if !ok {
					continue
				}
				text := up.Content
				if text == "" && len(up.Contents) > 0 {
					text = "[attachment]"
				}
				if text != "" {
					out = append(out, transcriptEntry{role: "user", text: text})
				}
			}
		case ai.ModelResponse:
			var b strings.Builder
			for _, part := range m.Parts {
				if tp, ok := part.(ai.TextPart); ok {
					b.WriteString(tp.Content)
				}
			}
			if b.Len() > 0 {
				out = append(out, transcriptEntry{role: "assistant", text: b.String()})
			}
		}
	}
	return out
}
