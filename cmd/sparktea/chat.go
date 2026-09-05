package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/sparktea/codemode"
)

// minInputHeight/maxInputHeight bound the input textarea's row count: it
// grows with pasted or wrapped text (so multi-line prompts stay visible) but
// never eats the whole screen.
const (
	minInputHeight = 1
	maxInputHeight = 6
)

var (
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	assistantStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	systemStyle    = lipgloss.NewStyle().Italic(true).Faint(true)
	thinkingStyle  = lipgloss.NewStyle().Italic(true).Faint(true).Foreground(lipgloss.Color("141"))
	errorStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	helpStyle      = lipgloss.NewStyle().Faint(true)
	headerStyle    = lipgloss.NewStyle().Bold(true).Padding(0, 1).
			Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230"))
)

// transcriptEntry is one turn in the chat viewport.
type transcriptEntry struct {
	role string // "user", "assistant", "thinking", or "system"
	text string
}

// glamourStyle is fixed rather than auto-detected: glamour's auto-style
// probes the terminal background over stdin/stdout, which races with
// bubbletea's own raw-mode input reader once the program is running.
// sparktea's existing palette (chat.go's *Style vars) already assumes a
// dark terminal, so this matches.
const glamourStyle = "dark"

// newMarkdownRenderer builds the glamour renderer used for assistant
// answers, word-wrapped to the current viewport width. Returns nil on error,
// in which case renderMarkdown falls back to showing text unrendered.
func newMarkdownRenderer(width int) *glamour.TermRenderer {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(max(width-2, 20)),
		glamour.WithEmoji(),
	)
	if err != nil {
		return nil
	}
	return r
}

// requestModelSwitchMsg is sent up to appModel when the user types /model,
// asking it to show the picker again without losing the chat underneath.
type requestModelSwitchMsg struct{}

// Messages fed from the background streaming goroutine into Update via a
// per-turn channel; see waitForStream.
type (
	streamStartedMsg       struct{ ch chan tea.Msg }
	streamDeltaMsg         string
	streamThinkingDeltaMsg string
	streamNoteMsg          string
	streamDoneMsg          struct {
		messages []ai.ModelMessage
		usage    ai.Usage
		sources  string // formatted "🔗 Sources:" note, or "" if the turn cited none
	}
	streamErrMsg struct{ err error }
)

// chatModel is the main conversation screen: a scrolling transcript, a text
// input, and a spinner while a response streams in.
type chatModel struct {
	option modelOption
	agent  *ai.Agent[struct{}, string]

	ctx    context.Context
	cancel context.CancelFunc

	history []ai.ModelMessage

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	md       *glamour.TermRenderer // renders assistant answers as markdown; nil disables rendering
	mdWidth  int                   // word-wrap width m.md was built for; rebuilt on resize

	transcript      []transcriptEntry
	historyRendered string // cached, already-rendered transcript entries joined with blank lines
	streaming       bool
	streamCh        chan tea.Msg
	current         strings.Builder
	currentThinking strings.Builder

	sessionUsage  ai.Usage
	searchEnabled bool

	codeEnabled        bool
	codeModeCapability *codemode.CodeMode

	err           error
	width, height int
	ready         bool
}

// newAgentFor builds the agent for a model option. Called both when a chat
// starts and when /model switches models mid-conversation.
func newAgentFor(option modelOption) *ai.Agent[struct{}, string] {
	opts := []ai.Option{
		ai.WithInstructions("Answer clearly and concisely."),
	}
	if logfireCapability != nil {
		opts = append(opts,
			ai.WithAgentName("sparktea"),
			ai.WithCapabilities(logfireCapability),
		)
	}
	return ai.NewAgent[struct{}, string](option.newModel(), opts...)
}

func newChatModel(option modelOption, width, height int) (*chatModel, tea.Cmd) {
	agent := newAgentFor(option)
	ctx, cancel := context.WithCancel(context.Background())

	ta := textarea.New()
	ta.Placeholder = "Ask something… (ctrl+j for a newline)"
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.CharLimit = 4000
	ta.SetHeight(minInputHeight)
	// Enter is handled by chatModel.Update (send) and never reaches the
	// textarea; ctrl+j is the escape hatch for a literal newline.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	cm := &chatModel{
		option:             option,
		agent:              agent,
		ctx:                ctx,
		cancel:             cancel,
		input:              ta,
		viewport:           viewport.New(width, max(height-5, 1)),
		spinner:            sp,
		codeModeCapability: codemode.New(),
	}
	cm.setSize(width, height)
	return cm, textarea.Blink
}

// setSize applies a new terminal size, recomputing the input box's height
// (it grows with content, up to maxInputHeight) and the viewport's height
// around it. The markdown renderer is rebuilt to the new word-wrap width,
// which also re-renders the whole transcript at that width.
func (m *chatModel) setSize(width, height int) {
	m.width, m.height = width, height
	m.input.SetWidth(width - 4)

	inputHeight := clampInputHeight(m.input.LineCount())
	m.input.SetHeight(inputHeight)

	if m.md == nil || m.mdWidth != width {
		m.md = newMarkdownRenderer(width)
		m.mdWidth = width
	}

	// header (1) + blank (1) + input (inputHeight) + blank (1) + help (1)
	m.viewport.Width = width
	m.viewport.Height = max(height-4-inputHeight, 1)
	m.ready = true
	m.rebuildHistory()
	m.refreshViewport()
}

func clampInputHeight(lines int) int {
	if lines < minInputHeight {
		return minInputHeight
	}
	if lines > maxInputHeight {
		return maxInputHeight
	}
	return lines
}

func (m *chatModel) Init() tea.Cmd { return textarea.Blink }

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "ctrl+d":
			// Mirrors shell/REPL convention: quit on an empty line, otherwise
			// let textarea's own ctrl+d binding (delete-forward) apply below.
			if m.input.Value() == "" {
				m.cancel()
				return m, tea.Quit
			}
		case "esc":
			if !m.streaming {
				m.cancel()
				return m, tea.Quit
			}
		case "enter":
			if !m.streaming {
				prompt := strings.TrimSpace(m.input.Value())
				switch {
				case prompt == "":
				case strings.HasPrefix(prompt, "/"):
					m.input.Reset()
					m.adjustInputHeight()
					if cmd := m.runCommand(prompt); cmd != nil {
						return m, cmd
					}
				default:
					m.appendEntry("user", prompt)
					m.input.Reset()
					m.adjustInputHeight()
					m.streaming = true
					m.err = nil
					m.refreshViewport()
					cmds = append(cmds, m.startStream(prompt), m.spinner.Tick)
				}
			}
			return m, tea.Batch(cmds...)
		}

	case streamStartedMsg:
		m.streamCh = msg.ch
		return m, waitForStream(m.streamCh)

	case streamDeltaMsg:
		m.current.WriteString(string(msg))
		m.refreshViewport()
		return m, waitForStream(m.streamCh)

	case streamThinkingDeltaMsg:
		m.currentThinking.WriteString(string(msg))
		m.refreshViewport()
		return m, waitForStream(m.streamCh)

	case streamNoteMsg:
		m.appendEntry("system", string(msg))
		m.refreshViewport()
		return m, waitForStream(m.streamCh)

	case streamDoneMsg:
		m.streaming = false
		if msg.messages != nil {
			m.history = msg.messages
		}
		m.sessionUsage.Add(msg.usage)
		m.flushCurrentTurn()
		if msg.sources != "" {
			m.note(msg.sources)
		}
		return m, nil

	case streamErrMsg:
		m.streaming = false
		m.err = msg.err
		m.flushCurrentTurn()
		return m, nil

	case spinner.TickMsg:
		if m.streaming {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.adjustInputHeight()
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// adjustInputHeight grows or shrinks the input box to fit its content (up to
// maxInputHeight), giving the rows it doesn't need back to the viewport.
func (m *chatModel) adjustInputHeight() {
	h := clampInputHeight(m.input.LineCount())
	if h == m.input.Height() {
		return
	}
	m.input.SetHeight(h)
	// textarea.Model only re-follows the cursor at the end of its own
	// Update; SetHeight alone leaves its internal scroll offset wherever it
	// last was, which can hide lines that now fit in the taller box.
	// SetValue (via Reset) is the one exported call that re-homes it, and it
	// leaves the cursor at the end of the reinserted text — where a user
	// growing an in-progress prompt already is.
	m.input.SetValue(m.input.Value())
	m.viewport.Height = max(m.height-4-h, 1)
	m.refreshViewport()
}

// formatUsageCompact renders a short running-total for the header — total
// tokens and, when the provider reports it, cost. Empty until the first
// turn completes (u.IsZero()), since View re-renders on every message
// anyway, this is all that's needed for the header to track sessionUsage as
// it grows turn by turn; there's no separate timer. Gating on IsZero rather
// than Requests specifically, since not every provider adapter populates
// that field even when it does report tokens or cost.
func formatUsageCompact(u ai.Usage) string {
	if u.IsZero() {
		return ""
	}
	s := formatCount(u.TotalTokens()) + " tok"
	if u.CostUSD != nil {
		s += fmt.Sprintf(" · $%.4f", *u.CostUSD)
	}
	return s
}

// formatCount abbreviates large token counts (12345 -> "12.3k").
func formatCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (m *chatModel) View() string {
	if !m.ready {
		return "initializing…"
	}
	title := fmt.Sprintf("sparktea · %s", m.option.label)
	if logfireCapability != nil {
		title += " · 🔭 logfire"
	}
	if usage := formatUsageCompact(m.sessionUsage); usage != "" {
		title += " · " + usage
	}
	header := headerStyle.Render(title)

	status := helpStyle.Render("enter: send · ctrl+j: newline · /model /usage /clear /search /code /save /load · esc/ctrl+c/ctrl+d: quit")
	if m.searchEnabled && m.option.supportsNativeWebSearch() {
		status = helpStyle.Render("🔎 web search on · ") + status
	}
	if m.codeEnabled {
		status = helpStyle.Render("🐍 code mode on · ") + status
	}
	if m.streaming {
		status = fmt.Sprintf("%s thinking…", m.spinner.View())
	} else if m.err != nil {
		status = errorStyle.Render("error: " + m.err.Error())
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		m.input.View(),
		status,
	)
}

// refreshViewport renders the viewport from the cached, already-rendered
// history plus any in-progress assistant response, and scrolls to the
// bottom. It does not re-render finalized entries — see appendEntry.
func (m *chatModel) refreshViewport() {
	b := m.historyRendered
	if m.streaming {
		if b != "" {
			b += "\n\n"
		}
		if m.currentThinking.Len() > 0 {
			b += renderPlainEntry("thinking", m.currentThinking.String())
			b += "\n\n"
		}
		text := m.current.String()
		if text == "" {
			text = "…"
		}
		// Streamed text renders raw rather than through glamour: markdown is
		// usually invalid mid-stream (an unclosed code fence, a half-written
		// link), and re-parsing it on every token would be wasted work.
		// renderEntry takes over once the turn lands in history.
		b += renderPlainEntry("assistant", text)
	}
	m.viewport.SetContent(b)
	m.viewport.GotoBottom()
}

// flushCurrentTurn moves any in-progress thinking/answer text into the
// transcript and re-renders. Called when a stream ends, successfully or not.
func (m *chatModel) flushCurrentTurn() {
	if m.currentThinking.Len() > 0 {
		m.appendEntry("thinking", m.currentThinking.String())
		m.currentThinking.Reset()
	}
	if m.current.Len() > 0 {
		m.appendEntry("assistant", m.current.String())
		m.current.Reset()
	}
	m.refreshViewport()
}

// appendEntry adds a finalized entry to the transcript and renders it once,
// appending the result to historyRendered rather than re-rendering the
// whole transcript on every refreshViewport call (which matters once
// rendering means running glamour over markdown, not just concatenation).
func (m *chatModel) appendEntry(role, text string) {
	e := transcriptEntry{role: role, text: text}
	m.transcript = append(m.transcript, e)
	if m.historyRendered != "" {
		m.historyRendered += "\n\n"
	}
	m.historyRendered += m.renderEntry(e)
}

// rebuildHistory re-renders every transcript entry from scratch. Needed
// after a resize, since the word-wrap width baked into m.md changes, and
// after a wholesale transcript replacement (/load), where appendEntry's
// incremental cache doesn't apply.
func (m *chatModel) rebuildHistory() {
	var b strings.Builder
	for i, e := range m.transcript {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.renderEntry(e))
	}
	m.historyRendered = b.String()
}

// renderEntry renders one finalized transcript entry for display. Assistant
// answers go through glamour for markdown formatting; every other role
// renders as plain styled text.
func (m *chatModel) renderEntry(e transcriptEntry) string {
	if e.role != "assistant" {
		return renderPlainEntry(e.role, e.text)
	}
	var b strings.Builder
	b.WriteString(assistantStyle.Render("Assistant"))
	b.WriteString("\n")
	b.WriteString(m.renderMarkdown(e.text))
	return b.String()
}

// renderMarkdown renders text through glamour, falling back to the raw text
// if the renderer isn't available (construction failed) or errors.
func (m *chatModel) renderMarkdown(text string) string {
	if m.md == nil {
		return text
	}
	out, err := m.md.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(out)
}

// webSearchResult is one normalized search hit pulled from a provider's raw
// web-search result data, so sparktea can show sources for an answer that
// providers ground on searched pages without necessarily citing inline.
type webSearchResult struct {
	title string
	url   string
}

// extractWebSearchResults normalizes web-search result/citation data across
// the different shapes sparktea's search-capable providers actually use:
//   - Google (Gemini): a []map[string]any built from groundingChunks[].web,
//     keyed "uri"/"title".
//   - Anthropic: a json-decoded []any of web_search_result blocks, keyed
//     "url"/"title".
//   - OpenRouter (OpenAI-compatible chat completions): a []map[string]any of
//     annotations, each wrapping its fields one level deeper under
//     "url_citation" — OpenAI's own citation format, which OpenRouter passes
//     through.
//
// Anything that doesn't match one of these shapes is skipped rather than
// guessed at.
func extractWebSearchResults(value any) []webSearchResult {
	var items []any
	switch v := value.(type) {
	case []map[string]any:
		for _, m := range v {
			items = append(items, m)
		}
	case []any:
		items = v
	default:
		return nil
	}

	var out []webSearchResult
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if nested, ok := m["url_citation"].(map[string]any); ok {
			m = nested
		}
		url, _ := firstString(m, "url", "uri")
		if url == "" {
			continue
		}
		title, _ := firstString(m, "title", "name")
		if title == "" {
			title = url
		}
		out = append(out, webSearchResult{title: title, url: url})
	}
	return out
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// collectWebSearchSources scans the ModelResponse messages one turn added —
// i.e. after[len(before):] — for web-search results, wherever the provider
// adapter put them (see extractWebSearchResults), and returns a
// deduplicated, formatted "🔗 Sources:" note, or "" if the turn cited none.
func collectWebSearchSources(before, after []ai.ModelMessage) string {
	if len(after) <= len(before) {
		return ""
	}
	var results []webSearchResult
	seen := map[string]bool{}
	add := func(rs []webSearchResult) {
		for _, r := range rs {
			if seen[r.url] {
				continue
			}
			seen[r.url] = true
			results = append(results, r)
		}
	}
	for _, msg := range after[len(before):] {
		resp, ok := msg.(ai.ModelResponse)
		if !ok {
			continue
		}
		for _, part := range resp.Parts {
			ret, ok := part.(ai.NativeToolReturnPart)
			if !ok || ret.ToolKind != ai.ToolPartKindWebSearch {
				continue
			}
			add(extractWebSearchResults(ret.Content))
		}
		if annotations, ok := resp.ProviderDetails["annotations"]; ok {
			add(extractWebSearchResults(annotations))
		}
	}
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("🔗 Sources:")
	for _, r := range results {
		b.WriteString("\n  · ")
		if r.title != r.url {
			b.WriteString(r.title)
			b.WriteString(" — ")
		}
		b.WriteString(r.url)
	}
	return b.String()
}

func renderPlainEntry(role, text string) string {
	var b strings.Builder
	writeEntry(&b, role, text)
	return b.String()
}

func writeEntry(b *strings.Builder, role, text string) {
	switch role {
	case "user":
		b.WriteString(userStyle.Render("You"))
		b.WriteString("\n")
		b.WriteString(text)
	case "system":
		b.WriteString(systemStyle.Render(text))
	case "thinking":
		b.WriteString(thinkingStyle.Render("💭 Thinking"))
		b.WriteString("\n")
		b.WriteString(thinkingStyle.Render(text))
	default:
		b.WriteString(assistantStyle.Render("Assistant"))
		b.WriteString("\n")
		b.WriteString(text)
	}
}

// note appends a system-styled line to the transcript, e.g. for command
// output, and re-renders.
func (m *chatModel) note(text string) {
	m.appendEntry("system", text)
	m.refreshViewport()
}

// switchModel replaces the active model with option, keeping the transcript
// and message history so the new model picks up the same conversation.
func (m *chatModel) switchModel(option modelOption) {
	if option == m.option {
		return
	}
	m.option = option
	m.agent = newAgentFor(option)
	logLocal(slog.LevelInfo, "model_switched", "provider", string(option.provider), "model", option.modelID)
	m.note(fmt.Sprintf("— switched to %s —", option.label))
}

// runCommand handles a "/..." input line. It returns a non-nil tea.Cmd only
// for commands that need appModel's involvement (currently /model);
// everything else is applied directly and returns nil.
func (m *chatModel) runCommand(line string) tea.Cmd {
	fields := strings.Fields(line)
	name := fields[0]
	arg := strings.TrimSpace(strings.TrimPrefix(line, name))

	switch name {
	case "/model":
		return func() tea.Msg { return requestModelSwitchMsg{} }

	case "/usage":
		u := m.sessionUsage
		cost := "unknown"
		if u.CostUSD != nil {
			cost = fmt.Sprintf("$%.4f", *u.CostUSD)
		}
		m.note(fmt.Sprintf(
			"requests=%d input_tokens=%d output_tokens=%d tool_calls=%d cost=%s",
			u.Requests, u.InputTokens, u.OutputTokens, u.ToolCalls, cost,
		))

	case "/clear":
		m.history = nil
		m.transcript = nil
		m.historyRendered = ""
		m.sessionUsage = ai.Usage{}
		m.err = nil
		m.note("History cleared.")
		logLocal(slog.LevelInfo, "history_cleared")

	case "/search":
		switch strings.ToLower(arg) {
		case "on":
			m.searchEnabled = true
		case "off":
			m.searchEnabled = false
		default:
			m.searchEnabled = !m.searchEnabled
		}
		state := "off"
		if m.searchEnabled {
			state = "on"
			if !m.option.supportsNativeWebSearch() {
				state += fmt.Sprintf(" (%s has no native web search — ignored)", m.option.provider)
			}
		}
		m.note("web search: " + state)
		logLocal(slog.LevelInfo, "web_search_toggled", "enabled", m.searchEnabled)

	case "/code":
		switch strings.ToLower(arg) {
		case "on":
			m.codeEnabled = true
		case "off":
			m.codeEnabled = false
		default:
			m.codeEnabled = !m.codeEnabled
		}
		state := "off"
		if m.codeEnabled {
			state = "on"
		}
		m.note("code mode: " + state)
		logLocal(slog.LevelInfo, "code_mode_toggled", "enabled", m.codeEnabled)

	case "/save":
		path, err := writeSessionFile(arg, m.history)
		if err != nil {
			logLocalError("session_save_failed", err)
			m.note("save failed: " + err.Error())
			break
		}
		logLocal(slog.LevelInfo, "session_saved")
		m.note("saved session to " + path)

	case "/load":
		messages, path, err := readSessionFile(arg)
		if err != nil {
			logLocalError("session_load_failed", err)
			m.note("load failed: " + err.Error())
			break
		}
		m.history = messages
		m.transcript = transcriptFromMessages(messages)
		m.rebuildHistory()
		m.sessionUsage = ai.Usage{}
		m.err = nil
		m.note("loaded session from " + path)
		logLocal(slog.LevelInfo, "session_loaded")

	default:
		logLocal(slog.LevelWarn, "unknown_command")
		m.note("unknown command: " + name)
	}
	return nil
}

// waitForStream reads the next bubbletea message produced by the background
// run and re-enters the event loop with it. The producer goroutine (started
// in startStream) closes over ch and stops sending after a terminal message
// (streamDoneMsg or streamErrMsg), so Update only calls this again for
// streamDeltaMsg.
func waitForStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// startStream runs the agent against prompt in the background, translating
// its event stream into bubbletea messages delivered over a channel.
func (m *chatModel) startStream(prompt string) tea.Cmd {
	ch := make(chan tea.Msg, 64)
	agent := m.agent
	ctx := m.ctx
	history := m.history
	option := m.option
	searchEnabled := m.searchEnabled && option.supportsNativeWebSearch()
	codeEnabled := m.codeEnabled

	runOpts := []ai.RunOption{ai.WithMessageHistory(history)}
	if searchEnabled {
		// Optional: true lets models without native search support just skip
		// it instead of failing the run. Providers with no native-tool
		// support at all (see supportsNativeWebSearch) are excluded here
		// entirely, since Optional can't help those — the request errors
		// before the provider ever gets a chance to ignore it.
		runOpts = append(runOpts, ai.WithRunNativeTools(ai.WebSearchTool{Optional: true}))
	}
	if m.codeEnabled {
		runOpts = append(runOpts, ai.WithRunCapabilities(m.codeModeCapability))
	}

	go func() {
		logLocal(slog.LevelInfo, "turn_started", "mode", "interactive", "provider", string(option.provider), "model", option.modelID, "web_search", searchEnabled, "code_mode", codeEnabled)
		runTracer, runCtx := startRunTracer(ctx, "sparktea turn")

		run := agent.RunStream(runCtx, prompt, struct{}{}, runOpts...)
		for event, err := range run.Events() {
			if err != nil {
				runTracer.end(err)
				logLocalError("turn_failed", err, "mode", "interactive", "provider", string(option.provider), "model", option.modelID)
				ch <- streamErrMsg{err: err}
				return
			}
			switch e := event.(type) {
			case ai.PartStartEvent:
				runTracer.observe(e.Part)
				switch part := e.Part.(type) {
				case ai.TextPart:
					if part.Content != "" {
						ch <- streamDeltaMsg(part.Content)
					}
				case ai.ThinkingPart:
					if part.Content != "" {
						ch <- streamThinkingDeltaMsg(part.Content)
					}
				case ai.NativeToolCallPart:
					ch <- streamNoteMsg("🔎 " + part.ToolName)
				case ai.ToolCallPart:
					if part.ToolName == codemode.ToolName {
						ch <- streamNoteMsg("🐍 " + part.ToolName)
					}
				}
			case ai.PartDeltaEvent:
				switch delta := e.Delta.(type) {
				case ai.TextPartDelta:
					if delta.ContentDelta != "" {
						ch <- streamDeltaMsg(delta.ContentDelta)
					}
				case ai.ThinkingPartDelta:
					if delta.ContentDelta != "" {
						ch <- streamThinkingDeltaMsg(delta.ContentDelta)
					}
				}
			case ai.FunctionToolCallEvent:
				logLocal(slog.LevelInfo, "tool_started", "tool", e.Part.ToolName)
			case ai.FunctionToolResultEvent:
				switch part := e.Part.(type) {
				case ai.ToolReturnPart:
					logLocal(slog.LevelInfo, "tool_finished", "tool", part.ToolName, "outcome", "success")
				case ai.RetryPromptPart:
					logLocal(slog.LevelWarn, "tool_finished", "tool", part.ToolName, "outcome", "error")
				}
			}
		}
		runTracer.end(nil)
		var messages []ai.ModelMessage
		var usage ai.Usage
		var sources string
		if result := run.Result(); result != nil {
			messages = result.Messages()
			usage = result.Usage()
			sources = collectWebSearchSources(history, messages)
		}
		args := []any{"mode", "interactive", "provider", string(option.provider), "model", option.modelID}
		args = append(args, usageLogArgs(usage)...)
		logLocal(slog.LevelInfo, "turn_completed", args...)
		ch <- streamDoneMsg{messages: messages, usage: usage, sources: sources}
	}()

	return func() tea.Msg {
		return streamStartedMsg{ch: ch}
	}
}
