package main

import tea "github.com/charmbracelet/bubbletea"

// appState selects which screen appModel currently delegates to.
type appState int

const (
	stateModelPicker appState = iota
	stateChat
	stateSwitchModel
)

// appModel is the root bubbletea model. It shows the model picker first,
// then hands off to the chat screen once a model is chosen. A /model command
// typed into chat reopens the picker (stateSwitchModel) without discarding
// the chat underneath.
type appModel struct {
	state   appState
	options []modelOption
	picker  pickerModel
	chat    *chatModel

	width, height int
}

func newAppModel(options []modelOption) appModel {
	return appModel{
		state:   stateModelPicker,
		options: options,
		picker:  newPickerModel(options),
	}
}

func (m appModel) Init() tea.Cmd { return m.picker.Init() }

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = wsMsg.Width, wsMsg.Height
	}

	if _, ok := msg.(requestModelSwitchMsg); ok && m.state == stateChat {
		m.picker = newModelSwitchPicker(m.options, m.chat.option)
		m.picker.list.SetSize(m.width, m.height)
		m.state = stateSwitchModel
		return m, m.picker.Init()
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

	case stateSwitchModel:
		updated, cmd := m.picker.Update(msg)
		m.picker = updated.(pickerModel)
		switch {
		case m.picker.cancelled:
			m.state = stateChat
			return m, nil
		case m.picker.chosen != nil:
			m.chat.switchModel(*m.picker.chosen)
			m.state = stateChat
			return m, nil
		default:
			return m, cmd
		}

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
