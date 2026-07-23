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

// statusReporter is implemented by inline apps (SET PLAN, CREATE USER, ...)
// whose AppAdapter exposes a one-shot result string for the lobby to show
// once the app finishes. Checked by interface rather than a per-app type
// switch so adding a new inline app doesn't require touching this file.
type statusReporter interface {
	StatusMsg() string
}

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
	sessionID     string // this session's registry ID; routes PHONE events to us
	reg           *registry.Registry
	db            *store.Store
	calls         *phone.Calls
	pendingExpiry time.Duration // for PURGE PENDING; 0 = disabled

	// out is the SSH session's output writer, used for direct terminal
	// writes that must bypass BubbleTea's renderer (e.g. the bell character).
	out io.Writer

	activeApp     app.App // non-nil when inside PHONE (or future apps)
	pendingCallID string  // call-id of the most recent incoming ring, if any

	input        string
	history      []string
	scrollOffset int // lines scrolled back from the bottom; 0 = live/bottom
	width        int
	height       int
}

// New returns a fresh lobby Model for the authenticated session. sessionID is
// this session's registry ID (from Registry.Register), used to receive PHONE
// events addressed to this specific session rather than the account.
func New(username, role, sessionID, version string, reg *registry.Registry, db *store.Store, calls *phone.Calls, out io.Writer, pendingExpiry time.Duration) Model {
	return Model{
		username:      username,
		role:          role,
		sessionID:     sessionID,
		reg:           reg,
		db:            db,
		calls:         calls,
		out:           out,
		pendingExpiry: pendingExpiry,
		history:       buildWelcome(username, role, version, db),
	}
}

// bannerContext carries everything a banner provider may need to decide what
// (if anything) to contribute to a session's login welcome. It is deliberately
// not an ssh.Session: the lobby package stays decoupled from the SSH layer, so
// providers see only the session facts assembled at New() time. Add a field
// here when a future provider needs one (e.g. a mail store for unread counts).
type bannerContext struct {
	username string
	role     string
	version  string
	db       *store.Store
}

// bannerProvider produces zero or more login-banner lines for a session.
// Returning nil (or an empty slice) means "nothing to show" — which subsumes a
// separate show bool, and lets one provider emit several lines (the pending-
// approvals provider emits a status line plus a follow-up hint).
type bannerProvider func(bannerContext) []string

// bannerProviders is the ordered list of system-driven banner providers. The
// login banner is the greeting line followed by each provider's output, in
// this order. Adding a provider (e.g. a mail unread-count line once the Mail
// app exists) is an append here plus the function — no change to the login
// flow. This is a static registry, not per-session state, so a package-level
// var is appropriate: nothing mutates it at runtime.
var bannerProviders = []bannerProvider{
	versionBanner,
	pendingApprovalsBanner,
}

// buildWelcome constructs the initial history for a new session: a fixed
// greeting followed by every banner provider's contribution, in order.
func buildWelcome(username, role, version string, db *store.Store) []string {
	ctx := bannerContext{username: username, role: role, version: version, db: db}
	msgs := []string{
		fmt.Sprintf("Welcome, %s. Type HELP for a list of commands.", username),
	}
	for _, provider := range bannerProviders {
		msgs = append(msgs, provider(ctx)...)
	}
	return msgs
}

// versionBanner stamps the running server version. Always shown.
func versionBanner(ctx bannerContext) []string {
	return []string{fmt.Sprintf("%%VAX-BBS-I-VERSION, running %s", ctx.version)}
}

// pendingApprovalsBanner shows admins how many registrations await approval,
// with a hint on how to review them. Nothing for non-admins, when the store is
// unavailable, or when the queue is empty.
func pendingApprovalsBanner(ctx bannerContext) []string {
	if ctx.role != "admin" || ctx.db == nil {
		return nil
	}
	n, err := ctx.db.CountPendingAccounts()
	if err != nil || n == 0 {
		return nil
	}
	return []string{
		fmt.Sprintf("%%VAX-BBS-I-PEND, %d account registration(s) awaiting approval.", n),
		"  Type LIST PENDING to review.",
	}
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
	return m.subscribePhoneEvents()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// If an app is active, delegate to it.
	if m.activeApp != nil {
		actualMsg := tea.Msg(msg)
		var resubscribeCmd tea.Cmd

		if ring, ok := msg.(phoneRingMsg); ok {
			actualMsg = phone.PhoneEventMsg{Event: ring.event}
			resubscribeCmd = m.subscribePhoneEvents()
		}

		updated, appCmd := m.activeApp.Update(actualMsg)
		if updatedApp, ok := updated.(app.App); ok {
			m.activeApp = updatedApp
			if m.activeApp.Done() {
				if sr, ok := m.activeApp.(statusReporter); ok {
					if msg := sr.StatusMsg(); msg != "" {
						m.history = append(m.history, msg)
					}
				}
				m.activeApp = nil
				if ring, ok := msg.(phoneRingMsg); ok {
					switch ring.event.Type {
					case registry.EventReject:
						m.history = append(m.history,
							fmt.Sprintf("%%VAX-BBS-I-PHONE, %s rejected your call.", ring.event.Callee))
					case registry.EventHangup:
						if ring.event.Callee != "" {
							m.history = append(m.history,
								fmt.Sprintf("%%VAX-BBS-I-PHONE, %s cancelled the call.", ring.event.Caller))
						}
					}
				}
				return m, m.subscribePhoneEvents()
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
		m.pendingCallID = ""
		return m, m.activeApp.Init()

	case phoneRingMsg:
		var bellCmd tea.Cmd
		m, bellCmd = m.handleRing(msg.event)
		return m, tea.Batch(bellCmd, m.subscribePhoneEvents())

	case clearLobbyBellMsg:
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit

		case tea.KeyPgUp:
			// Scroll back one screenful (leaving one line of overlap for context).
			m.scrollOffset = m.scrollOffset + m.viewportHeight() - 1
			// Clamp to available history.
			flat := flattenHistory(m.history)
			maxOffset := len(flat) - m.viewportHeight()
			if maxOffset < 0 {
				maxOffset = 0
			}
			if m.scrollOffset > maxOffset {
				m.scrollOffset = maxOffset
			}
			return m, nil

		case tea.KeyPgDown:
			// Scroll forward one screenful.
			m.scrollOffset -= m.viewportHeight() - 1
			if m.scrollOffset < 0 {
				m.scrollOffset = 0
			}
			return m, nil

		case tea.KeyEnd:
			// Jump to bottom (most recent output).
			m.scrollOffset = 0
			return m, nil

		case tea.KeyEnter:
			line := m.input
			m.input = ""
			m.scrollOffset = 0 // new command output always jumps to bottom
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
		// Only clear the ring we're actually showing (defence-in-depth).
		if event.CallID != m.pendingCallID {
			return m, nil
		}
		m.pendingCallID = ""
		m.history = append(m.history,
			fmt.Sprintf("%%VAX-BBS-I-PHONE, %s cancelled the call.", event.Caller))
	case registry.EventAnswerElsewhere:
		// Another of this account's sessions answered the call we were being
		// rung for; retract our ring without blaming the caller for cancelling.
		if event.CallID != m.pendingCallID {
			return m, nil
		}
		m.pendingCallID = ""
		m.history = append(m.history,
			"%VAX-BBS-I-PHONE, Call answered on another session.")
	case registry.EventCalleeGone, registry.EventCalleeUnavailable:
		// Defence-in-depth. A caller waiting on a ring is normally inside the
		// PHONE app (DIAL launches it), which handles these in handlePhoneEvent,
		// so this path is not the primary route — but an event that reached the
		// lobby should still say something truthful rather than be dropped.
		// Deliberately NOT added to the app-exit mapping in Update(): these
		// events call goIdle rather than ending the app, so that block never
		// sees them and a case there would be dead code.
		if event.Type == registry.EventCalleeUnavailable {
			m.history = append(m.history,
				fmt.Sprintf("%%VAX-BBS-I-PHONE, %s is unavailable.", event.Callee))
		} else {
			m.history = append(m.history,
				fmt.Sprintf("%%VAX-BBS-I-PHONE, %s has disconnected.", event.Callee))
		}
	case registry.EventAdminNotify:
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

var (
	promptStyle    = lipgloss.NewStyle().Bold(true)
	scrollBarStyle = lipgloss.NewStyle().Faint(true)
)

// viewportHeight returns the number of lines available for history output.
// Reserves 1 line for the prompt and 1 for the scroll indicator (when shown).
func (m Model) viewportHeight() int {
	h := m.height - 1 // always reserve prompt line
	if h < 4 {
		h = 4
	}
	return h
}

// flattenHistory expands history entries (which may contain embedded \n
// from multi-line command output) into individual display lines.
// This ensures scroll math is based on rendered line count, not entry count.
func flattenHistory(history []string) []string {
	var lines []string
	for _, entry := range history {
		parts := strings.Split(entry, "\n")
		lines = append(lines, parts...)
	}
	return lines
}

func (m Model) View() string {
	// Delegate to active app if one is running.
	if m.activeApp != nil {
		return m.activeApp.View()
	}

	flat := flattenHistory(m.history)
	vh := m.viewportHeight()

	// When scrolled back, reserve one extra line for the scroll indicator.
	indicatorLine := ""
	if m.scrollOffset > 0 {
		vh-- // shrink viewport to make room for indicator at top
		remaining := len(flat) - vh - m.scrollOffset
		if remaining < 0 {
			remaining = 0
		}
		indicatorLine = scrollBarStyle.Render(
			fmt.Sprintf("── scrolled back (%d lines above) ── PgDn / End to return ──", remaining),
		)
	}

	// Calculate the window of lines to show.
	// scrollOffset=0 → show the last vh lines (bottom/live).
	// scrollOffset=N → show vh lines ending N lines before the bottom.
	total := len(flat)
	end := total - m.scrollOffset
	if end > total {
		end = total
	}
	start := end - vh
	if start < 0 {
		start = 0
	}

	var b strings.Builder

	if indicatorLine != "" {
		b.WriteString(indicatorLine)
		b.WriteString("\n")
	}

	for _, line := range flat[start:end] {
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Show prompt only when at the bottom (not scrolled back).
	if m.scrollOffset == 0 {
		b.WriteString(promptStyle.Render("LOBBY>") + " " + m.input + "█")
	} else {
		b.WriteString(scrollBarStyle.Render("── (scrolling — press End to return to prompt) ──"))
	}

	return b.String()
}

// subscribePhoneEvents re-arms this session's phone-event receiver. It fetches
// the notify and done channels in one Events() call so they are always a
// matched pair from the same registry entry generation — the invariant
// waitForPhoneEvent relies on. Returns a nil Cmd when the session isn't
// registered (Events returns both nil).
func (m Model) subscribePhoneEvents() tea.Cmd {
	events, done := m.reg.Events(m.sessionID)
	return waitForPhoneEvent(events, done)
}

// waitForPhoneEvent returns a tea.Cmd that blocks until either a PhoneEvent
// arrives on the session's notify channel (firing a phoneRingMsg) or the
// session's done channel closes at teardown (returning nil so the goroutine
// exits instead of leaking). notify itself is never closed — it has lock-free
// non-blocking senders — so done is the shutdown signal, selected alongside it.
// Bubble Tea can't cancel an in-flight Cmd, so without this select the blocking
// receive would keep the goroutine alive for the whole process lifetime.
func waitForPhoneEvent(ch <-chan registry.PhoneEvent, done <-chan struct{}) tea.Cmd {
	// Guard both, not just ch: a receive from a nil channel blocks forever, so
	// a non-nil ch paired with a nil done would silently disable the shutdown
	// arm and reintroduce the leak with no error. Events() always returns the
	// two together (both non-nil or both nil), so a mismatch is a broken
	// invariant — fail toward "stop listening" (visible) not "leak" (silent).
	if ch == nil || done == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			return phoneRingMsg{event: event}
		case <-done:
			return nil
		}
	}
}
