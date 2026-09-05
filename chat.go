package main

import (
	"context"
	"fmt"
	"strings"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// transcriptEntry is one rendered turn in the chat viewport.
type transcriptEntry struct {
	role string // "user", "assistant", "thinking", or "system"
	text string
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
	input    textinput.Model
	spinner  spinner.Model

	transcript      []transcriptEntry
	streaming       bool
	streamCh        chan tea.Msg
	current         strings.Builder
	currentThinking strings.Builder

	sessionUsage  ai.Usage
	searchEnabled bool

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
			ai.WithAgentName("openrouter-agent"),
			ai.WithCapabilities(logfireCapability),
		)
	}
	return ai.NewAgent[struct{}, string](option.newModel(), opts...)
}

func newChatModel(option modelOption, width, height int) (*chatModel, tea.Cmd) {
	agent := newAgentFor(option)
	ctx, cancel := context.WithCancel(context.Background())

	ti := textinput.New()
	ti.Placeholder = "Ask something…"
	ti.Prompt = "> "
	ti.CharLimit = 4000
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	cm := &chatModel{
		option:   option,
		agent:    agent,
		ctx:      ctx,
		cancel:   cancel,
		input:    ti,
		viewport: viewport.New(width, max(height-5, 1)),
		spinner:  sp,
	}
	cm.setSize(width, height)
	return cm, textinput.Blink
}

func (m *chatModel) setSize(width, height int) {
	m.width, m.height = width, height
	m.input.Width = width - 4
	// header (1) + blank (1) + input (1) + blank (1) + help (1)
	m.viewport.Width = width
	m.viewport.Height = max(height-5, 1)
	m.ready = true
	m.refreshViewport()
}

func (m *chatModel) Init() tea.Cmd { return textinput.Blink }

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
			// let textinput's own ctrl+d binding (delete-forward) apply below.
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
					if cmd := m.runCommand(prompt); cmd != nil {
						return m, cmd
					}
				default:
					m.transcript = append(m.transcript, transcriptEntry{role: "user", text: prompt})
					m.input.Reset()
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
		m.transcript = append(m.transcript, transcriptEntry{role: "system", text: string(msg)})
		m.refreshViewport()
		return m, waitForStream(m.streamCh)

	case streamDoneMsg:
		m.streaming = false
		if msg.messages != nil {
			m.history = msg.messages
		}
		m.sessionUsage.Add(msg.usage)
		m.flushCurrentTurn()
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
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *chatModel) View() string {
	if !m.ready {
		return "initializing…"
	}
	title := fmt.Sprintf("openrouter-agent · %s", m.option.label)
	if logfireCapability != nil {
		title += " · 🔭 logfire"
	}
	header := headerStyle.Render(title)

	status := helpStyle.Render("enter: send · /model /usage /clear /search /save /load · esc/ctrl+c/ctrl+d: quit")
	if m.searchEnabled {
		status = helpStyle.Render("🔎 web search on · ") + status
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

// refreshViewport re-renders the full transcript (plus any in-progress
// assistant response) into the viewport and scrolls to the bottom.
func (m *chatModel) refreshViewport() {
	var b strings.Builder
	for i, entry := range m.transcript {
		if i > 0 {
			b.WriteString("\n\n")
		}
		writeEntry(&b, entry.role, entry.text)
	}
	if m.streaming {
		if len(m.transcript) > 0 {
			b.WriteString("\n\n")
		}
		if m.currentThinking.Len() > 0 {
			writeEntry(&b, "thinking", m.currentThinking.String())
			b.WriteString("\n\n")
		}
		text := m.current.String()
		if text == "" {
			text = "…"
		}
		writeEntry(&b, "assistant", text)
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

// flushCurrentTurn moves any in-progress thinking/answer text into the
// transcript and re-renders. Called when a stream ends, successfully or not.
func (m *chatModel) flushCurrentTurn() {
	if m.currentThinking.Len() > 0 {
		m.transcript = append(m.transcript, transcriptEntry{role: "thinking", text: m.currentThinking.String()})
		m.currentThinking.Reset()
	}
	if m.current.Len() > 0 {
		m.transcript = append(m.transcript, transcriptEntry{role: "assistant", text: m.current.String()})
		m.current.Reset()
	}
	m.refreshViewport()
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
	m.transcript = append(m.transcript, transcriptEntry{role: "system", text: text})
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
		m.sessionUsage = ai.Usage{}
		m.err = nil
		m.note("History cleared.")

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
		}
		m.note("web search: " + state)

	case "/save":
		path, err := writeSessionFile(arg, m.history)
		if err != nil {
			m.note("save failed: " + err.Error())
			break
		}
		m.note("saved session to " + path)

	case "/load":
		messages, path, err := readSessionFile(arg)
		if err != nil {
			m.note("load failed: " + err.Error())
			break
		}
		m.history = messages
		m.transcript = transcriptFromMessages(messages)
		m.sessionUsage = ai.Usage{}
		m.err = nil
		m.note("loaded session from " + path)

	default:
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

	runOpts := []ai.RunOption{ai.WithMessageHistory(history)}
	if m.searchEnabled {
		// Optional: true lets models without native search support just skip
		// it instead of failing the run.
		runOpts = append(runOpts, ai.WithRunNativeTools(ai.WebSearchTool{Optional: true}))
	}

	go func() {
		run := agent.RunStream(ctx, prompt, struct{}{}, runOpts...)
		for event, err := range run.Events() {
			if err != nil {
				ch <- streamErrMsg{err: err}
				return
			}
			switch e := event.(type) {
			case ai.PartStartEvent:
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
			}
		}
		var messages []ai.ModelMessage
		var usage ai.Usage
		if result := run.Result(); result != nil {
			messages = result.Messages()
			usage = result.Usage()
		}
		ch <- streamDoneMsg{messages: messages, usage: usage}
	}()

	return func() tea.Msg {
		return streamStartedMsg{ch: ch}
	}
}
