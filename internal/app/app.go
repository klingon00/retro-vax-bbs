// Package app defines the contract every lobby-launched application
// (PHONE first, then mail and the text game) implements.
package app

import tea "github.com/charmbracelet/bubbletea"

// App mirrors Bubble Tea's own Model interface (Init/Update/View) on
// purpose. An App composes directly into the lobby's single tea.Program
// for a session rather than needing an adapter layer or a nested
// tea.Program of its own.
//
// Not wired into the lobby yet — there's only one "screen" (the lobby
// itself) until PHONE exists to actually launch. This interface is
// defined now, ahead of any implementation, because the design doc calls
// out getting this contract right early as mattering more than how many
// apps exist on day one: it's what keeps future apps from requiring
// changes to lobby code.
type App interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (App, tea.Cmd)
	View() string

	// Done reports whether the app has finished and control should
	// return to the lobby. The lobby checks this after every Update.
	Done() bool
}
