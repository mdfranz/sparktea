package main

import (
	"strings"
	"testing"

	ai "github.com/Kludex/pydantic-ai-go/ai"
)

func TestFormatCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{12345, "12.3k"},
		{999999, "1000.0k"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}
	for _, c := range cases {
		if got := formatCount(c.n); got != c.want {
			t.Errorf("formatCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestFormatUsageCompact(t *testing.T) {
	if got := formatUsageCompact(ai.Usage{}); got != "" {
		t.Errorf("zero-value Usage should render empty for the header before any turn completes, got %q", got)
	}

	got := formatUsageCompact(ai.Usage{Requests: 1, InputTokens: 100, OutputTokens: 23})
	if want := "123 tok"; got != want {
		t.Errorf("formatUsageCompact() = %q, want %q", got, want)
	}

	cost := 0.041234
	got = formatUsageCompact(ai.Usage{Requests: 2, InputTokens: 5000, OutputTokens: 1200, CostUSD: &cost})
	if want := "6.2k tok · $0.0412"; got != want {
		t.Errorf("formatUsageCompact() = %q, want %q", got, want)
	}

	// Some provider adapters report tokens/cost without ever setting
	// Requests; the header must still show up for those.
	got = formatUsageCompact(ai.Usage{InputTokens: 10, OutputTokens: 5})
	if want := "15 tok"; got != want {
		t.Errorf("formatUsageCompact() with Requests unset = %q, want %q", got, want)
	}
}

// TestHeaderShowsUsageAfterCompletedTurn drives the actual Update/View path
// a real completed turn takes — streamDoneMsg carrying usage, same as
// startStream sends — rather than just the pure formatting helper, to catch
// any wiring bug between sessionUsage and the rendered header.
func TestHeaderShowsUsageAfterCompletedTurn(t *testing.T) {
	option := modelOption{label: "Test Model", provider: providerAnthropic, modelID: "claude-haiku-4-5-20251001"}
	m, _ := newChatModel(option, 80, 24)

	if got := m.View(); strings.Contains(got, "tok") {
		t.Fatalf("View() before any turn already mentions usage:\n%s", got)
	}

	updated, _ := m.Update(streamDoneMsg{usage: ai.Usage{Requests: 1, InputTokens: 10, OutputTokens: 5}})
	m = updated.(*chatModel)

	got := m.View()
	if !strings.Contains(got, "15 tok") {
		t.Errorf("View() after streamDoneMsg should show the usage total, got:\n%s", got)
	}
}
