package main

import (
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
}
