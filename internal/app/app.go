// Package app defines the contract every lobby-launched application
// (PHONE first, then mail and the text game) implements.
package app

import tea "github.com/charmbracelet/bubbletea"

// App extends Bubble Tea's tea.Model with a Done() signal. The lobby
// delegates input and rendering to an active App, checking Done() after
// every Update to know when to resume the lobby prompt.
//
// Using tea.Model as the Update return type (rather than App) avoids a
// circular import between internal/app and internal/phone, while still
// giving the lobby everything it needs: it type-asserts the returned
// tea.Model back to App to check Done().
type App interface {
	tea.Model // Init() tea.Cmd, Update(tea.Msg) (tea.Model, tea.Cmd), View() string

	// Done reports whether the app has finished and control should
	// return to the lobby. The lobby checks this after every Update.
	Done() bool
}
