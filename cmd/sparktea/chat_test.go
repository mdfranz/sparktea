package main

import (
	"strings"
	"testing"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/charmbracelet/x/ansi"
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

func TestGetCommandURL(t *testing.T) {
	for _, rawURL := range []string{
		"https://example.com/article",
		"http://example.com:8080/a?b=c",
	} {
		got, err := getCommandURL([]string{"/get", rawURL})
		if err != nil || got != rawURL {
			t.Errorf("getCommandURL(%q) = %q, %v", rawURL, got, err)
		}
	}

	for _, fields := range [][]string{
		nil,
		{"/get"},
		{"/get", "ftp://example.com/file"},
		{"/get", "example.com/no-scheme"},
		{"/get", "https://example.com", "extra"},
	} {
		if _, err := getCommandURL(fields); err == nil {
			t.Errorf("getCommandURL(%q) succeeded, want usage error", fields)
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

func TestExtractWebSearchResults(t *testing.T) {
	// Google (Gemini): groundingChunks[].web, keyed "uri"/"title".
	google := []map[string]any{
		{"uri": "https://example.com/a", "title": "Example A"},
	}
	if got := extractWebSearchResults(google); len(got) != 1 || got[0].url != "https://example.com/a" || got[0].title != "Example A" {
		t.Errorf("Google shape: got %+v", got)
	}

	// Anthropic: a json-decoded []any of web_search_result blocks, keyed
	// "url"/"title" — decoding a JSON array into `any` always yields []any
	// of map[string]any, never []map[string]any directly.
	anthropic := []any{
		map[string]any{"type": "web_search_result", "url": "https://example.com/b", "title": "Example B"},
	}
	if got := extractWebSearchResults(anthropic); len(got) != 1 || got[0].url != "https://example.com/b" || got[0].title != "Example B" {
		t.Errorf("Anthropic shape: got %+v", got)
	}

	// OpenRouter/OpenAI chat completions: annotations nested one level
	// deeper under "url_citation".
	openrouter := []map[string]any{
		{"type": "url_citation", "url_citation": map[string]any{"url": "https://example.com/c", "title": "Example C"}},
	}
	if got := extractWebSearchResults(openrouter); len(got) != 1 || got[0].url != "https://example.com/c" || got[0].title != "Example C" {
		t.Errorf("OpenRouter shape: got %+v", got)
	}

	// A result with no url is skipped rather than shown blank.
	if got := extractWebSearchResults([]map[string]any{{"title": "no url here"}}); len(got) != 0 {
		t.Errorf("entries without a url should be skipped, got %+v", got)
	}

	// Missing title falls back to the url itself.
	if got := extractWebSearchResults([]map[string]any{{"uri": "https://example.com/d"}}); len(got) != 1 || got[0].title != "https://example.com/d" {
		t.Errorf("missing title should fall back to the url, got %+v", got)
	}

	// Shapes that don't match any provider's format are skipped, not guessed at.
	if got := extractWebSearchResults("not a list"); got != nil {
		t.Errorf("unrecognized shape should yield nil, got %+v", got)
	}
}

// TestHyperlinkSurvivesTruncation exercises the exact claim in
// collectWebSearchSources' comment: lipgloss/viewport truncate long lines
// with ansi.Truncate (see (Style).Render's MaxWidth handling), and that
// truncation is hyperlink-aware -- clipping the *visible* URL text still
// leaves a well-formed, complete OSC 8 sequence pointing at the full URL.
func TestHyperlinkSurvivesTruncation(t *testing.T) {
	const full = "https://example.com/a/very/long/path/that/will/not/fit/on/one/narrow/line"
	link := hyperlink(full, full)

	clipped := ansi.Truncate(link, 20, "")

	if !strings.Contains(clipped, "\x1b]8;;"+full+"\x07") {
		t.Errorf("truncated hyperlink lost its href to the full URL: %q", clipped)
	}
	if !strings.HasSuffix(clipped, "\x1b]8;;\x07") {
		t.Errorf("truncated hyperlink was not properly closed, would leak into later text: %q", clipped)
	}
	if visible := ansi.Strip(clipped); len(visible) >= len(full) {
		t.Errorf("expected the visible text to actually be clipped, got %d chars", len(visible))
	}
}

func TestCollectWebSearchSources(t *testing.T) {
	before := []ai.ModelMessage{ai.ModelRequest{}}

	t.Run("no new messages", func(t *testing.T) {
		if got := collectWebSearchSources(before, before); got != "" {
			t.Errorf("no new messages should yield no sources, got %q", got)
		}
	})

	t.Run("Google NativeToolReturnPart", func(t *testing.T) {
		after := append(before, ai.ModelResponse{
			Parts: []ai.ResponsePart{
				ai.NativeToolReturnPart{
					ToolName: "web_search", ToolKind: ai.ToolPartKindWebSearch,
					Content: []map[string]any{{"uri": "https://example.com/a", "title": "Example A"}},
				},
			},
		})
		got := collectWebSearchSources(before, after)
		if want := "🔗 Sources:\n  · Example A\n    " + hyperlink("https://example.com/a", "https://example.com/a"); got != want {
			t.Errorf("collectWebSearchSources() = %q, want %q", got, want)
		}
	})

	t.Run("OpenRouter annotations, deduplicated", func(t *testing.T) {
		after := append(before, ai.ModelResponse{
			ProviderDetails: map[string]any{
				"annotations": []map[string]any{
					{"url_citation": map[string]any{"url": "https://example.com/b", "title": "Example B"}},
					{"url_citation": map[string]any{"url": "https://example.com/b", "title": "Example B"}},
				},
			},
		})
		got := collectWebSearchSources(before, after)
		if want := "🔗 Sources:\n  · Example B\n    " + hyperlink("https://example.com/b", "https://example.com/b"); got != want {
			t.Errorf("duplicate urls should collapse to one entry, got %q, want %q", got, want)
		}
	})

	t.Run("no search results", func(t *testing.T) {
		after := append(before, ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "hello"}}})
		if got := collectWebSearchSources(before, after); got != "" {
			t.Errorf("a turn with no search results should yield no sources, got %q", got)
		}
	})
}

func TestHasWebFetchResult(t *testing.T) {
	before := []ai.ModelMessage{ai.ModelRequest{}}

	if hasWebFetchResult(before, before) {
		t.Error("unchanged history should not contain a newly fetched page")
	}

	local := append(before, ai.ModelRequest{Parts: []ai.RequestPart{
		ai.ToolReturnPart{ToolName: "web_fetch", Content: ai.WebFetchResult{URL: "https://example.com"}},
	}})
	if !hasWebFetchResult(before, local) {
		t.Error("local web-fetch return was not detected")
	}

	native := append(before, ai.ModelResponse{Parts: []ai.ResponsePart{
		ai.NativeToolReturnPart{ToolName: "web_fetch", ToolKind: ai.ToolPartKindWebFetch},
	}})
	if !hasWebFetchResult(before, native) {
		t.Error("native web-fetch return was not detected")
	}

	other := append(before, ai.ModelRequest{Parts: []ai.RequestPart{
		ai.ToolReturnPart{ToolName: "other"},
	}})
	if hasWebFetchResult(before, other) {
		t.Error("unrelated tool return was reported as a fetch")
	}

	failed := append(before, ai.ModelRequest{Parts: []ai.RequestPart{
		ai.ToolReturnPart{ToolName: "web_fetch", Outcome: ai.ToolReturnOutcomeFailed},
	}})
	if hasWebFetchResult(before, failed) {
		t.Error("failed web fetch was reported as a retained page")
	}
}
