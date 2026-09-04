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
	errorStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	helpStyle      = lipgloss.NewStyle().Faint(true)
	headerStyle    = lipgloss.NewStyle().Bold(true).Padding(0, 1).
			Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230"))
)

// transcriptEntry is one rendered turn in the chat viewport.
type transcriptEntry struct {
	role string // "user", "assistant", or "system"
	text string
}

// requestModelSwitchMsg is sent up to appModel when the user types /model,
// asking it to show the picker again without losing the chat underneath.
type requestModelSwitchMsg struct{}

// Messages fed from the background streaming goroutine into Update via a
// per-turn channel; see waitForStream.
type (
	streamStartedMsg struct{ ch chan tea.Msg }
	streamDeltaMsg   string
	streamDoneMsg    struct{ messages []ai.ModelMessage }
	streamErrMsg     struct{ err error }
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

	transcript []transcriptEntry
	streaming  bool
	streamCh   chan tea.Msg
	current    strings.Builder

	err           error
	width, height int
	ready         bool
}

// newAgentFor builds the agent for a model option. Called both when a chat
// starts and when /model switches models mid-conversation.
func newAgentFor(option modelOption) *ai.Agent[struct{}, string] {
	return ai.NewAgent[struct{}, string](option.newModel(),
		ai.WithInstructions("Answer clearly and concisely."),
	)
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
		case "esc":
			if !m.streaming {
				m.cancel()
				return m, tea.Quit
			}
		case "enter":
			if !m.streaming {
				prompt := strings.TrimSpace(m.input.Value())
				switch prompt {
				case "":
				case "/model":
					m.input.Reset()
					return m, func() tea.Msg { return requestModelSwitchMsg{} }
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

	case streamDoneMsg:
		m.streaming = false
		if msg.messages != nil {
			m.history = msg.messages
		}
		if m.current.Len() > 0 {
			m.transcript = append(m.transcript, transcriptEntry{role: "assistant", text: m.current.String()})
			m.current.Reset()
		}
		m.refreshViewport()
		return m, nil

	case streamErrMsg:
		m.streaming = false
		m.err = msg.err
		if m.current.Len() > 0 {
			m.transcript = append(m.transcript, transcriptEntry{role: "assistant", text: m.current.String()})
			m.current.Reset()
		}
		m.refreshViewport()
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
	header := headerStyle.Render(fmt.Sprintf("openrouter-agent · %s", m.option.label))

	status := helpStyle.Render("enter: send · /model: switch model · esc/ctrl+c: quit")
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
		if m.transcript != nil {
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

func writeEntry(b *strings.Builder, role, text string) {
	switch role {
	case "user":
		b.WriteString(userStyle.Render("You"))
		b.WriteString("\n")
		b.WriteString(text)
	case "system":
		b.WriteString(systemStyle.Render(text))
	default:
		b.WriteString(assistantStyle.Render("Assistant"))
		b.WriteString("\n")
		b.WriteString(text)
	}
}

// switchModel replaces the active model with option, keeping the transcript
// and message history so the new model picks up the same conversation.
func (m *chatModel) switchModel(option modelOption) {
	if option == m.option {
		return
	}
	m.option = option
	m.agent = newAgentFor(option)
	m.transcript = append(m.transcript, transcriptEntry{
		role: "system",
		text: fmt.Sprintf("— switched to %s —", option.label),
	})
	m.refreshViewport()
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

	go func() {
		run := agent.RunStream(ctx, prompt, struct{}{}, ai.WithMessageHistory(history))
		textIdx := map[int]bool{}
		for event, err := range run.Events() {
			if err != nil {
				ch <- streamErrMsg{err: err}
				return
			}
			switch e := event.(type) {
			case ai.PartStartEvent:
				if tp, ok := e.Part.(ai.TextPart); ok {
					textIdx[e.Index] = true
					if tp.Content != "" {
						ch <- streamDeltaMsg(tp.Content)
					}
				}
			case ai.PartDeltaEvent:
				if !textIdx[e.Index] {
					continue
				}
				if td, ok := e.Delta.(ai.TextPartDelta); ok && td.ContentDelta != "" {
					ch <- streamDeltaMsg(td.ContentDelta)
				}
			}
		}
		var messages []ai.ModelMessage
		if result := run.Result(); result != nil {
			messages = result.Messages()
		}
		ch <- streamDoneMsg{messages: messages}
	}()

	return func() tea.Msg {
		return streamStartedMsg{ch: ch}
	}
}
