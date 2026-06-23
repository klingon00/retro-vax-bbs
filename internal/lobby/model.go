// Package lobby implements the closed command-loop shell every session
// lands in after connecting: WHO, FINGER, PHONE, and friends all run and
// return here, exactly like the original VAX/VMS DCL prompt. Nothing exits
// sideways into a real shell — there isn't one to exit into.
package lobby

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
)

// Model is the root Bubble Tea model for a session sitting at the lobby
// prompt. wish gives each connected SSH session its own tea.Program, so
// one Model exists per session and there is no shared mutable state
// between sessions here by construction.
//
// Cross-session data (WHO list, future PHONE call routing) is accessed
// through the registry pointer, which is safe for concurrent read access
// from multiple goroutines — each session's tea.Program runs in its own
// goroutine, but the registry uses sync.RWMutex internally.
type Model struct {
	username string
	role     string             // "user" or "admin" — for WHO visibility
	reg      *registry.Registry // nil until a real registry exists (safe: whoCommand checks)
	input    string
	history  []string
	width    int
	height   int
}

// New returns a fresh lobby Model for the authenticated session.
// username and role come from the verified account record; reg is the
// shared session registry used by WHO. Both the role and registry are
// passed in explicitly — not read from package-level state — so the
// lobby has no hidden dependencies and remains easy to test.
func New(username, role string, reg *registry.Registry) Model {
	return Model{
		username: username,
		role:     role,
		reg:      reg,
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
			output, cmd := dispatch(line, m)
			m.history = append(m.history, output)
			return m, cmd

		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			return m, nil

		case tea.KeySpace:
			// Bubble Tea routes space through KeySpace, not KeyRunes.
			// Without this case, spaces are silently dropped, breaking
			// two-word commands like SHOW USERS and SHOW TIME.
			m.input += " "
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
