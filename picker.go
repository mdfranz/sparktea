package main

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var pickerTitleStyle = lipgloss.NewStyle().
	Bold(true).
	Padding(0, 1).
	Background(lipgloss.Color("62")).
	Foreground(lipgloss.Color("230"))

// pickerModel is a scrollable list of models to run the chat against. It's
// used both for the initial startup pick and, when cancellable, for /model
// switching mid-conversation (where esc/q backs out instead of quitting).
type pickerModel struct {
	list        list.Model
	chosen      *modelOption
	quitting    bool
	cancellable bool
	cancelled   bool
}

func newPickerModel(options []modelOption) pickerModel {
	items := make([]list.Item, len(options))
	for i, o := range options {
		items[i] = o
	}
	delegate := list.NewDefaultDelegate()
	// No blank line between entries: the catalog spans several providers
	// now, so keep the list compact rather than one screenful per model.
	delegate.SetSpacing(0)
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select a model"
	l.Styles.Title = pickerTitleStyle
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	return pickerModel{list: l}
}

// newModelSwitchPicker is the /model picker: same list, but esc/q return to
// the chat instead of quitting, and current is pre-selected.
func newModelSwitchPicker(options []modelOption, current modelOption) pickerModel {
	p := newPickerModel(options)
	p.cancellable = true
	p.list.Title = "Switch model"
	for i, item := range p.list.Items() {
		if opt, ok := item.(modelOption); ok && opt == current {
			p.list.Select(i)
			break
		}
	}
	return p
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			m.quitting = true
			return m, tea.Quit
		case "q", "esc":
			if m.cancellable {
				m.cancelled = true
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if item, ok := m.list.SelectedItem().(modelOption); ok {
				m.chosen = &item
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	return "\n" + m.list.View()
}
