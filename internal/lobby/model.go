// Package lobby implements the closed command-loop shell every session
// lands in after connecting: WHO, FINGER, PHONE, and friends all run and
// return here, exactly like the original VAX/VMS DCL prompt. Nothing exits
// sideways into a real shell — there isn't one to exit into.
package lobby

import (
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/klingon00/retro-vax-bbs/internal/app"
	"github.com/klingon00/retro-vax-bbs/internal/phone"
	"github.com/klingon00/retro-vax-bbs/internal/registry"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// phoneRingMsg fires when the registry delivers a PhoneEvent for this
// session — specifically a ring event while at the lobby prompt.
type phoneRingMsg struct {
	event registry.PhoneEvent
}

// Model is the root Bubble Tea model for a session at the lobby prompt.
// When activeApp is non-nil, all input and rendering is delegated to it;
// the lobby resumes when activeApp.Done() returns true.
type Model struct {
	username      string
	role          string
	reg           *registry.Registry
	db            *store.Store
	calls         *phone.Calls
	pendingExpiry time.Duration // for PURGE PENDING; 0 = disabled

	// out is the SSH session's output writer, used for direct terminal
	// writes that must bypass BubbleTea's renderer (e.g. the bell character).
	out io.Writer

	activeApp     app.App // non-nil when inside PHONE (or future apps)
	pendingCallID string  // call-id of the most recent incoming ring, if any

	input   string
	history []string
	width   int
	height  int
}

// New returns a fresh lobby Model for the authenticated session.
func New(username, role string, reg *registry.Registry, db *store.Store, calls *phone.Calls, out io.Writer, pendingExpiry time.Duration) Model {
	return Model{
		username:      username,
		role:          role,
		reg:           reg,
		db:            db,
		calls:         calls,
		out:           out,
		pendingExpiry: pendingExpiry,
		history:       buildWelcome(username, role, db),
	}
}

// buildWelcome constructs the initial history for a new session.
// Admins additionally see a count of pending registrations if any exist.
func buildWelcome(username, role string, db *store.Store) []string {
	msgs := []string{fmt.Sprintf("Welcome, %s. Type HELP for a list of commands.", username)}
	if role == "admin" && db != nil {
		if n, err := db.CountPendingAccounts(); err == nil && n > 0 {
			msgs = append(msgs,
				fmt.Sprintf("%%VAX-BBS-I-PEND, %d account registration(s) awaiting approval.", n),
				"  Type LIST PENDING to review.",
			)
		}
	}
	return msgs
}

// ringBellCmd writes BEL directly to the SSH session output, bypassing
// BubbleTea's cellbuf renderer (which strips \a as a non-printable).
func (m Model) ringBellCmd() tea.Cmd {
	if m.out == nil {
		return nil
	}
	out := m.out
	return func() tea.Msg {
		out.Write([]byte("\a")) //nolint:errcheck
		return nil
	}
}

func (m Model) Init() tea.Cmd {
	// Poll for incoming PHONE events (ring notifications) even at the
	// lobby prompt. The Cmd blocks on the session's notify channel and
	// fires a phoneRingMsg when an event arrives.
	return waitForPhoneEvent(m.reg.Events(m.username))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// If an app is active, delegate to it. Phone events that arrive via
	// the lobby's waitForPhoneEvent goroutine are converted to the
	// phone.PhoneEventMsg type that the app understands, since the lobby
	// owns the notify channel exclusively (the app does not subscribe
	// directly — that was the source of a race condition).
	if m.activeApp != nil {
		actualMsg := tea.Msg(msg)
		var resubscribeCmd tea.Cmd

		if ring, ok := msg.(phoneRingMsg); ok {
			// Lobby's goroutine consumed an event from the channel.
			// Convert it for the app and restart our goroutine so we
			// keep consuming future events while the app is running.
			actualMsg = phone.PhoneEventMsg{Event: ring.event}
			resubscribeCmd = waitForPhoneEvent(m.reg.Events(m.username))
		}

		updated, appCmd := m.activeApp.Update(actualMsg)
		if updatedApp, ok := updated.(app.App); ok {
			m.activeApp = updatedApp
			if m.activeApp.Done() {
				m.activeApp = nil
				// When the phone app exits because of an event (not the
				// user's own hangup), show context in the lobby history.
				if ring, ok := msg.(phoneRingMsg); ok {
					switch ring.event.Type {
					case registry.EventReject:
						m.history = append(m.history,
							fmt.Sprintf("%%VAX-BBS-I-PHONE, %s rejected your call.", ring.event.Callee))
					case registry.EventHangup:
						if ring.event.Callee != "" {
							// Pending call cancelled by caller before we answered.
							m.history = append(m.history,
								fmt.Sprintf("%%VAX-BBS-I-PHONE, %s cancelled the call.", ring.event.Caller))
						}
					}
				}
				return m, waitForPhoneEvent(m.reg.Events(m.username))
			}
		}
		return m, tea.Batch(appCmd, resubscribeCmd)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case launchAppMsg:
		m.activeApp = msg.app
		m.pendingCallID = "" // answered or dialed — no longer pending
		return m, m.activeApp.Init()

	case phoneRingMsg:
		// Someone is calling us at the lobby.
		var bellCmd tea.Cmd
		m, bellCmd = m.handleRing(msg.event)
		return m, tea.Batch(bellCmd, waitForPhoneEvent(m.reg.Events(m.username)))

	case clearLobbyBellMsg:
		// No-op: bellPending approach was replaced by direct writer; kept to
		// drain any in-flight msgs from a previous session.
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
			if output != "" {
				m.history = append(m.history, output)
			}
			return m, cmd

		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			return m, nil

		case tea.KeySpace:
			m.input += " "
			return m, nil

		case tea.KeyRunes:
			m.input += string(msg.Runes)
			return m, nil
		}
	}
	return m, nil
}

// handleRing displays an incoming call notification at the lobby.
// Returns the updated model and a tea.Cmd (ringBellCmd when a bell is needed).
func (m Model) handleRing(event registry.PhoneEvent) (Model, tea.Cmd) {
	switch event.Type {
	case registry.EventRing:
		t := time.Now()
		timeStr := fmt.Sprintf("(%02d:%02d:%02d)", t.Hour(), t.Minute(), t.Second())
		m.pendingCallID = event.CallID
		m.history = append(m.history,
			fmt.Sprintf("%%VAX-BBS-I-PHONE, %s is phoning you %s",
				event.Caller, timeStr))
		m.history = append(m.history,
			"  Type ANSWER to accept or REJECT to decline.")
		return m, m.ringBellCmd()
	case registry.EventHangup, registry.EventReject:
		m.pendingCallID = ""
		m.history = append(m.history,
			fmt.Sprintf("%%VAX-BBS-I-PHONE, %s cancelled the call.", event.Caller))
	case registry.EventAdminNotify:
		// One-shot notification to admin sessions about a new registration.
		if m.role == "admin" {
			m.history = append(m.history,
				fmt.Sprintf("%%VAX-BBS-I-REG, %s has requested an account.", event.Caller),
				"  Type LIST PENDING to review.")
		}
	}
	return m, nil
}

// clearLobbyBellMsg is no longer used — keeping type for any pending msgs in flight.
type clearLobbyBellMsg struct{}

var promptStyle = lipgloss.NewStyle().Bold(true)

func (m Model) View() string {
	// Delegate to active app if one is running.
	if m.activeApp != nil {
		return m.activeApp.View()
	}

	var b strings.Builder
	for _, line := range m.history {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(promptStyle.Render("LOBBY>") + " " + m.input + "█")
	return b.String()
}

// waitForPhoneEvent returns a tea.Cmd that blocks until a PhoneEvent
// arrives on the session's notify channel, firing a phoneRingMsg.
func waitForPhoneEvent(ch <-chan registry.PhoneEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return phoneRingMsg{event: event}
	}
}
