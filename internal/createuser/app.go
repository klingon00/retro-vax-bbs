package createuser

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// AppAdapter wraps Model to satisfy internal/app.App — same pattern and
// same reason as setplan.AppAdapter (see internal/setplan/app.go): Model's
// Update returns a concrete Model, not tea.Model, so the lobby needs a
// thin wrapper rather than coupling directly to this package's type.
//
// Usage in the lobby (identical to SET PLAN):
//
//	m.activeApp = createuser.NewApp(m.db, m.username, username, role)
type AppAdapter struct {
	inner Model
}

// NewApp constructs an AppAdapter ready to hand to the lobby as an app.App.
// actor is the admin's own username, for the completion audit log line.
func NewApp(st *store.Store, actor, username, role string) *AppAdapter {
	return &AppAdapter{inner: New(st, actor, username, role)}
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
