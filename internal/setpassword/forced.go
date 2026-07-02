package setpassword

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// ForcedModel is the session's root model when the connecting user's
// account has must_change_password set (via admin EXPIRE PASSWORD). It is
// launched directly by teaHandler in cmd/server/main.go, in place of
// lobby.New — the same slot internal/registration occupies for username
// "new".
//
// This codebase has no mechanism to swap the root Bubble Tea model
// mid-session (wish's bm.Middleware builds exactly one tea.Program per
// SSH session; when it quits, the session closes). internal/registration
// hits the identical constraint, so ForcedModel follows its precedent:
// once the password is changed, tell the user to reconnect rather than
// attempting to hand off into the lobby inline.
//
// The flow cannot be skipped: Esc does nothing (Model.Cancelable is
// false, so its own cancel branch never fires), and only Ctrl+C/Ctrl+D
// disconnects — leaving must_change_password set, so the same prompt
// greets the user again next login.
type ForcedModel struct {
	inner  Model
	result string // non-empty once the password has been changed
}

// NewForced returns the mandatory password-change model for username.
// No current-password step — they just authenticated with it moments ago.
func NewForced(st *store.Store, username string) tea.Model {
	return ForcedModel{inner: New(st, username, username, false, false)}
}

func (m ForcedModel) Init() tea.Cmd { return nil }

func (m ForcedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.result != "" {
				return m, tea.Quit
			}
		}
	}

	if m.result == "" {
		updated, cmd := m.inner.Update(msg)
		m.inner = updated
		if m.inner.Done() {
			m.result = m.inner.StatusMsg +
				"\n\nPlease reconnect and log in with your new password.\n\n" +
				"Press Enter to disconnect."
		}
		return m, cmd
	}
	return m, nil
}

func (m ForcedModel) View() string {
	if m.result != "" {
		var b strings.Builder
		b.WriteString("\n")
		for _, line := range strings.Split(m.result, "\n") {
			b.WriteString("  " + line + "\n")
		}
		return b.String()
	}

	// inner.View() already writes the sacrifice blank line 1 required by
	// the BubbleTea v1.3.x rendering gotcha; keep it as line 1 here too
	// rather than doubling up.
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  Your password has expired and must be changed before you continue.\n")
	b.WriteString("  " + strings.Repeat("─", 40) + "\n")
	b.WriteString(strings.TrimPrefix(m.inner.View(), "\n"))
	return b.String()
}
