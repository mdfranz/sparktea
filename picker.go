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

// pickerModel is the startup screen: a scrollable list of models to run the
// chat against.
type pickerModel struct {
	list     list.Model
	chosen   *modelOption
	quitting bool
}

func newPickerModel(options []modelOption) pickerModel {
	items := make([]list.Item, len(options))
	for i, o := range options {
		items[i] = o
	}
	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select a model"
	l.Styles.Title = pickerTitleStyle
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	return pickerModel{list: l}
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
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
