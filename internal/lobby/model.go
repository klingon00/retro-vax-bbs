// Package lobby implements the closed command-loop shell every session
// lands in after connecting: WHO, FINGER, PHONE, and friends all run and
// return here, exactly like the original VMS DCL prompt. Nothing exits
// sideways into a real shell — there isn't one to exit into.
package lobby

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the root Bubble Tea model for a session sitting at the lobby
// prompt. wish gives each connected SSH session its own tea.Program, so
// one Model exists per session and there is no shared mutable state
// between sessions here by construction. Anything that genuinely needs
// to be cross-session (the WHO list, PHONE call routing) will live behind
// an explicit registry passed into New(), once one exists — not smuggled
// in as a package-level variable.
type Model struct {
	username string
	input    string
	history  []string
	width    int
	height   int
}

// New returns a fresh lobby Model for the given username.
//
// There is no real authentication yet: main.go currently passes through
// whatever username the SSH client offers, unchecked. Account state
// (pending/active/suspended), password/key verification, and lockout are
// the next milestone per the design doc's build order — they will sit
// between the SSH handshake and this call, not inside it.
func New(username string) Model {
	return Model{
		username: username,
		history:  []string{fmt.Sprintf("Welcome, %s. Type HELP for a list of commands.", username)},
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit

		case tea.KeyEnter:
			line := m.input
			m.input = ""
			if strings.TrimSpace(line) == "" {
				return m, nil
			}
			m.history = append(m.history, promptStyle.Render("LOBBY>")+" "+line)
			output, cmd := dispatch(line)
			m.history = append(m.history, output)
			return m, cmd

		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			return m, nil

		case tea.KeyRunes:
			m.input += string(msg.Runes)
			return m, nil
		}
	}
	return m, nil
}

var promptStyle = lipgloss.NewStyle().Bold(true)

func (m Model) View() string {
	var b strings.Builder
	for _, line := range m.history {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(promptStyle.Render("LOBBY>") + " " + m.input)
	return b.String()
}
