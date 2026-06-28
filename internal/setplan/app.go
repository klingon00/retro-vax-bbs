package setplan

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// AppAdapter wraps Model to satisfy internal/app.App.
//
// Why a wrapper rather than making Model implement app.App directly?
// app.App embeds tea.Model, whose Update must return (tea.Model, tea.Cmd).
// If Model.Update returned (tea.Model, tea.Cmd) we'd lose the concrete type
// and couldn't access IsDone/StatusMsg without a type assertion every call.
// The adapter keeps Model's API clean while giving the lobby what it expects.
//
// Usage in the lobby (identical to PHONE):
//
//	m.activeApp = setplan.NewApp(m.store, m.username, currentPlan, m.termWidth)
type AppAdapter struct {
	inner Model
}

// NewApp constructs an AppAdapter ready to hand to the lobby as an app.App.
func NewApp(st *store.Store, username, currentPlan string, termWidth int) *AppAdapter {
	return &AppAdapter{inner: New(st, username, currentPlan, termWidth)}
}

// Init implements tea.Model / app.App.
func (a *AppAdapter) Init() tea.Cmd {
	return a.inner.Init()
}

// Update implements tea.Model / app.App.
// Returns (tea.Model, tea.Cmd) per the interface; the lobby type-asserts
// the returned tea.Model back to app.App to call Done() — same as PHONE.
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
// The lobby reads this once to populate m.output before clearing activeApp.
func (a *AppAdapter) StatusMsg() string {
	return a.inner.StatusMsg
}
