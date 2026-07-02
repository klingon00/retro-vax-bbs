package setpassword

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// AppAdapter wraps Model to satisfy internal/app.App — same pattern and
// same reason as setplan.AppAdapter and createuser.AppAdapter: Model's
// Update returns a concrete Model, not tea.Model, so the lobby needs a
// thin wrapper rather than coupling directly to this package's type.
//
// Only the two lobby-launched flows go through AppAdapter. The forced
// flow (EXPIRE PASSWORD) is launched directly as the session's root model
// by teaHandler, not as a lobby activeApp — see ForcedModel in forced.go.
type AppAdapter struct {
	inner Model
}

// NewSelfApp constructs the self-service SET PASSWORD flow: username
// changes their own password, re-entering the current one first.
func NewSelfApp(st *store.Store, username string) *AppAdapter {
	return &AppAdapter{inner: New(st, username, username, true, true)}
}

// NewAdminApp constructs the admin-initiated RESET PASSWORD flow: actor
// (the admin) sets target's password directly with no current-password
// check — the admin doesn't know it.
func NewAdminApp(st *store.Store, actor, target string) *AppAdapter {
	return &AppAdapter{inner: New(st, actor, target, false, true)}
}

// Init implements tea.Model / app.App.
func (a *AppAdapter) Init() tea.Cmd {
	return a.inner.Init()
}

// Update implements tea.Model / app.App.
func (a *AppAdapter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := a.inner.Update(msg)
	a.inner = updated
	return a, cmd
}

// View implements tea.Model / app.App.
func (a *AppAdapter) View() string {
	return a.inner.View()
}

// Done implements app.App.
func (a *AppAdapter) Done() bool {
	return a.inner.Done()
}

// StatusMsg returns the human-readable result once Done() is true.
// The lobby reads this once to populate history before clearing activeApp.
func (a *AppAdapter) StatusMsg() string {
	return a.inner.StatusMsg
}
