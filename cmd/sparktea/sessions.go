package main

import (
	"bytes"
	"encoding/json"
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

const sessionFileVersion = 1

// sessionSnapshot is the state needed to continue and display a conversation
// after loading it, not just the provider message history.
type sessionSnapshot struct {
	Messages        []ai.ModelMessage
	Transcript      []transcriptEntry
	Activity        []transcriptEntry
	Option          modelOption
	Usage           ai.Usage
	SearchEnabled   bool
	CodeEnabled     bool
	ActivityEnabled bool
	HasState        bool
}

type sessionFile struct {
	Version         int               `json:"version"`
	History         json.RawMessage   `json:"history"`
	Transcript      []transcriptEntry `json:"transcript"`
	Activity        []transcriptEntry `json:"activity"`
	Model           sessionModel      `json:"model"`
	Usage           ai.Usage          `json:"usage"`
	SearchEnabled   bool              `json:"search_enabled"`
	CodeEnabled     bool              `json:"code_enabled"`
	ActivityEnabled bool              `json:"activity_enabled"`
}

type sessionModel struct {
	Label    string   `json:"label"`
	Provider provider `json:"provider"`
	ModelID  string   `json:"model_id"`
}

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

// writeSessionFile serializes the provider history with pydantic-ai-go's
// message codec and stores sparktea's display and session state alongside it.
func writeSessionFile(name string, snapshot sessionSnapshot) (string, error) {
	path, err := sessionPath(name)
	if err != nil {
		return "", err
	}
	history, err := ai.MarshalMessages(snapshot.Messages)
	if err != nil {
		return "", fmt.Errorf("encode session: %w", err)
	}
	doc := sessionFile{
		Version:         sessionFileVersion,
		History:         history,
		Transcript:      snapshot.Transcript,
		Activity:        snapshot.Activity,
		Model:           sessionModel{Label: snapshot.Option.label, Provider: snapshot.Option.provider, ModelID: snapshot.Option.modelID},
		Usage:           snapshot.Usage,
		SearchEnabled:   snapshot.SearchEnabled,
		CodeEnabled:     snapshot.CodeEnabled,
		ActivityEnabled: snapshot.ActivityEnabled,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
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

// readSessionFile loads a session file. It also accepts the legacy raw message
// arrays written before sparktea session metadata was added.
func readSessionFile(name string) (sessionSnapshot, string, error) {
	path, err := sessionPath(name)
	if err != nil {
		return sessionSnapshot{}, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sessionSnapshot{}, "", fmt.Errorf("no saved session named %q", strings.TrimSuffix(filepath.Base(path), ".json"))
		}
		return sessionSnapshot{}, "", err
	}
	if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '[' {
		messages, err := ai.UnmarshalMessages(data)
		if err != nil {
			return sessionSnapshot{}, "", fmt.Errorf("decode session: %w", err)
		}
		return sessionSnapshot{Messages: messages, Transcript: transcriptFromMessages(messages)}, path, nil
	}
	var doc sessionFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return sessionSnapshot{}, "", fmt.Errorf("decode session: %w", err)
	}
	if doc.Version != sessionFileVersion {
		return sessionSnapshot{}, "", fmt.Errorf("unsupported session version %d", doc.Version)
	}
	if len(doc.History) == 0 {
		return sessionSnapshot{}, "", errors.New("session has no message history")
	}
	messages, err := ai.UnmarshalMessages(doc.History)
	if err != nil {
		return sessionSnapshot{}, "", fmt.Errorf("decode session history: %w", err)
	}
	return sessionSnapshot{
		Messages:        messages,
		Transcript:      doc.Transcript,
		Activity:        doc.Activity,
		Option:          modelOption{label: doc.Model.Label, provider: doc.Model.Provider, modelID: doc.Model.ModelID},
		Usage:           doc.Usage,
		SearchEnabled:   doc.SearchEnabled,
		CodeEnabled:     doc.CodeEnabled,
		ActivityEnabled: doc.ActivityEnabled,
		HasState:        true,
	}, path, nil
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
