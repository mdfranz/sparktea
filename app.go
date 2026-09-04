package main

import tea "github.com/charmbracelet/bubbletea"

// appState selects which screen appModel currently delegates to.
type appState int

const (
	stateModelPicker appState = iota
	stateChat
)

// appModel is the root bubbletea model. It shows the model picker first,
// then hands off to the chat screen once a model is chosen.
type appModel struct {
	state  appState
	picker pickerModel
	chat   *chatModel

	width, height int
}

func newAppModel(options []modelOption) appModel {
	return appModel{
		state:  stateModelPicker,
		picker: newPickerModel(options),
	}
}

func (m appModel) Init() tea.Cmd { return m.picker.Init() }

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = wsMsg.Width, wsMsg.Height
	}

	switch m.state {
	case stateModelPicker:
		updated, cmd := m.picker.Update(msg)
		m.picker = updated.(pickerModel)
		if m.picker.chosen != nil {
			chat, initCmd := newChatModel(*m.picker.chosen, m.width, m.height)
			m.chat = chat
			m.state = stateChat
			return m, initCmd
		}
		return m, cmd

	case stateChat:
		updated, cmd := m.chat.Update(msg)
		m.chat = updated.(*chatModel)
		return m, cmd
	}

	return m, nil
}

func (m appModel) View() string {
	switch m.state {
	case stateChat:
		return m.chat.View()
	default:
		return m.picker.View()
	}
}
