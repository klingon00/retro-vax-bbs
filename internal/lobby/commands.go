package lobby

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/app"
	"github.com/klingon00/retro-vax-bbs/internal/phone"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// commandHandler is the only shape a no-argument lobby command may take.
// Handlers receive the current Model for session context (role, registry,
// store). The closed-command-grammar guarantee is preserved: the commands
// map is the only path from raw user input to executing code.
type commandHandler func(m Model) (string, tea.Cmd)

// argCommandHandler is the shape for commands that take a single argument
// (FINGER <username>, and future admin commands like APPROVE <username>).
// The arg is the original-case text after the command prefix — usernames
// are passed through as typed rather than uppercased, since SQLite lookups
// are case-sensitive.
type argCommandHandler func(m Model, arg string) (string, tea.Cmd)

// commands is the exact-match dispatch table. Populated in init().
var commands map[string]commandHandler

// argCommands is the prefix-match dispatch table for commands that take
// arguments. Checked before the exact-match table in dispatch(). Prefixes
// are uppercased; matching is done against the uppercased input so the
// user can type in any case.
var argCommands []struct {
	prefix  string
	handler argCommandHandler
}

// helpEntries drives the HELP output display. Separate from the dispatch
var helpEntries = []struct{ display string }{
	{"PHONE                       — enter phone facility (or: PHONE <username> to dial directly)"},
	{"DIAL <username>             — place a call (or: PHONE <username>)"},
	{"ANSWER                      — answer an incoming call"},
	{"REJECT                      — decline an incoming call"},
	{"FINGER <username>           (or: SHOW USER <username>)"},
	{"HELP"},
	{"LOGOUT"},
	{"TIME                        (or: SHOW TIME)"},
	{"WHO                         (or: SHOW USERS)"},
	{"SHOW <keyword>              (SHOW USER <username>, SHOW USERS, SHOW TIME)"},
}

func init() {
	commands = map[string]commandHandler{
		"HELP":       helpCommand,
		"LOGOUT":     logoutCommand,
		"TIME":       timeCommand,
		"SHOW TIME":  timeCommand,
		"WHO":        whoCommand,
		"SHOW USERS": whoCommand,
		"SHOW":       showCommand,
		"ANSWER":     answerCommand,
		"REJECT":     rejectCommand,
		"FINGER":     fingerUsage,
		"SHOW USER":  fingerUsage,
		// PHONE alone opens the facility in idle state.
		// DIAL alone shows usage (you must specify a username to dial).
		"PHONE": phoneOpenCommand,
		"DIAL":  dialUsage,
	}

	argCommands = []struct {
		prefix  string
		handler argCommandHandler
	}{
		{"SHOW USER", fingerByName},
		{"FINGER", fingerByName},
		// PHONE <user> and DIAL <user> both dial directly.
		{"PHONE", phoneDialCommand},
		{"DIAL", phoneDialCommand},
	}
}

// dispatch resolves one line of raw user input to a handler and runs it
// under recover(). A panic in one handler affects only that session.
//
// Argument-taking commands (FINGER, SHOW USER) are matched by prefix
// first — the prefix is compared case-insensitively against the
// uppercased input, and the remainder (the argument) is extracted from
// the original line to preserve case. Exact-match commands (everything
// else) are looked up after prefix matching finds nothing.
func dispatch(line string, m Model) (output string, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			output = "Internal error running that command (recovered safely). Try again or contact an admin."
			cmd = nil
		}
	}()

	upper := strings.ToUpper(strings.TrimSpace(line))

	// Prefix match for argument-taking commands.
	for _, ac := range argCommands {
		pfx := ac.prefix + " "
		if strings.HasPrefix(upper, pfx) {
			// Extract the argument from the original line (not uppercased)
			// so usernames are passed through as the user typed them.
			arg := strings.TrimSpace(line[len(pfx):])
			return ac.handler(m, arg)
		}
	}

	// Exact match for everything else.
	handler, ok := commands[upper]
	if !ok {
		return fmt.Sprintf("%q is not a recognized command. Type HELP for a list.", upper), nil
	}
	return handler(m)
}

func helpCommand(m Model) (string, tea.Cmd) {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, e := range helpEntries {
		b.WriteString("  ")
		b.WriteString(e.display)
		b.WriteString("\n")
	}
	return b.String(), nil
}

func logoutCommand(m Model) (string, tea.Cmd) {
	return "Goodbye.", tea.Quit
}

// timeCommand displays the current server time in VAX/VMS style:
// DD-MON-YYYY HH:MM:SS (e.g. 22-JUN-2026 15:30:24).
func timeCommand(m Model) (string, tea.Cmd) {
	now := time.Now()
	return strings.ToUpper(now.Format("02-Jan-2006 15:04:05")), nil
}

// whoCommand lists active sessions per the registry visibility rules,
// in a VAX/VMS-inspired columnar format.
func whoCommand(m Model) (string, tea.Cmd) {
	if m.reg == nil {
		return "WHO is not available — session registry not initialized.", nil
	}

	views := m.reg.List(m.role)
	if len(views) == 0 {
		return "No users connected.", nil
	}

	now := strings.ToUpper(time.Now().Format("02-Jan-2006 15:04:05"))
	var b strings.Builder
	fmt.Fprintf(&b, "VAX-BBS Interactive Users        %s\n\n", now)
	fmt.Fprintf(&b, "  %-20s %-12s %s\n", "Username", "App", "")
	fmt.Fprintf(&b, "  %-20s %-12s\n", "--------", "---")

	totalSessions := 0
	for _, v := range views {
		totalSessions += v.Count
		app := v.CurrentApp
		if app == "" {
			app = "LOBBY"
		}
		if v.Count > 1 {
			fmt.Fprintf(&b, "  %-20s %-12s (%d sessions)\n", v.Username, app, v.Count)
		} else {
			fmt.Fprintf(&b, "  %-20s %s\n", v.Username, app)
		}
	}
	fmt.Fprintf(&b, "\n  Total: %d user(s), %d session(s).", len(views), totalSessions)
	return b.String(), nil
}

// fingerUsage handles FINGER or SHOW USER typed without a username.
func fingerUsage(m Model) (string, tea.Cmd) {
	return "Usage: FINGER <username>  (or: SHOW USER <username>)", nil
}

// showCommand handles bare SHOW with no keyword.
func showCommand(m Model) (string, tea.Cmd) {
	return "SHOW requires a keyword. Try: SHOW USER <username>, SHOW USERS, SHOW TIME", nil
}

// fingerByName is the argument-taking FINGER handler. It looks up the
// target user by username, applies the same visibility rules as WHO
// (invisible admins look identical to nonexistent users — no enumeration),
// and displays their profile: current status, last login, and plan text.
func fingerByName(m Model, username string) (string, tea.Cmd) {
	if username == "" {
		return "Usage: FINGER <username>  (or: SHOW USER <username>)", nil
	}
	if m.db == nil {
		return "FINGER is not available — store not initialized.", nil
	}

	user, err := m.db.GetUserByUsername(username)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("No information available for %q.", username), nil
	}
	if err != nil {
		return "Error looking up user. Try again or contact an admin.", nil
	}

	// Visibility: invisible admins appear nonexistent to non-admins.
	// Same rule as WHO — if you can't see someone in the user list,
	// you can't target them with FINGER either.
	if user.Role == "admin" && m.role != "admin" && !user.AdminVisible {
		return fmt.Sprintf("No information available for %q.", username), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Username:   %s\n", user.Username)

	// Connection status from registry — raw Get bypasses visibility
	// filtering since we've already applied it above.
	if m.reg != nil {
		if sv, connected := m.reg.Get(user.Username); connected {
			app := sv.CurrentApp
			if app == "" {
				app = "LOBBY"
			}
			if sv.Count > 1 {
				fmt.Fprintf(&b, "Status:     Connected  %s  (%d sessions)\n", app, sv.Count)
			} else {
				fmt.Fprintf(&b, "Status:     Connected  %s\n", app)
			}
		} else {
			fmt.Fprintf(&b, "Status:     Not connected\n")
		}
	}

	if user.LastLoginAt.Valid {
		fmt.Fprintf(&b, "Last login: %s\n",
			strings.ToUpper(user.LastLoginAt.Time.Format("02-Jan-2006 15:04:05")))
	} else {
		fmt.Fprintf(&b, "Last login: (never)\n")
	}

	b.WriteString("\nPlan:\n")
	if user.PlanText.Valid && strings.TrimSpace(user.PlanText.String) != "" {
		b.WriteString("  ")
		b.WriteString(user.PlanText.String)
		b.WriteString("\n")
	} else {
		b.WriteString("  (no plan set)\n")
	}

	return b.String(), nil
}

// phoneUsage handles DIAL or PHONE typed without a username.
// phoneOpenCommand opens PHONE in idle state — no call started yet.
// The user lands at the % prompt and can dial, answer, etc. from there.
func phoneOpenCommand(m Model) (string, tea.Cmd) {
	if m.calls == nil {
		return "%PHONE-E-NOCALLS, phone system not initialized.", nil
	}
	phoneModel := phone.NewIdle(m.username, m.calls, m.reg, m.out, m.width, m.height)
	return "", launchAppCmd(phoneModel)
}

// dialUsage is shown when DIAL is typed with no username at the lobby prompt.
func dialUsage(m Model) (string, tea.Cmd) {
	return "Usage: DIAL <username>  (or: PHONE <username>)", nil
}

// phoneDialCommand places a call to another user, launching the PHONE app.
func phoneDialCommand(m Model, username string) (string, tea.Cmd) {
	if username == "" {
		return "Usage: DIAL <username>  (or: PHONE <username>)", nil
	}
	if m.calls == nil {
		return "%PHONE-E-NOCALLS, phone system not initialized.", nil
	}

	call, callerP, err := m.calls.Dial(m.username, username)
	if err != nil {
		return fmt.Sprintf("%%PHONE-E-NOLOGIN, %v", err), nil
	}

	phoneModel := phone.New(
		m.username, call.ID, username,
		m.calls, m.reg,
		callerP.IncomingChar,
		m.out, m.width, m.height,
	)

	return "", launchAppCmd(phoneModel)
}

// answerCommand answers the most recent pending incoming call.
func answerCommand(m Model) (string, tea.Cmd) {
	if m.calls == nil {
		return "%PHONE-E-NOCALLS, phone system not initialized.", nil
	}

	callID := findPendingCallID(m)
	if callID == "" {
		return "%PHONE-W-NOCALL, no incoming call to answer.", nil
	}

	call, calleeP, err := m.calls.Answer(callID, m.username)
	if err != nil {
		return fmt.Sprintf("%%PHONE-E-ANSWER, %v", err), nil
	}

	// Build the "others" list from all call participants so conference calls
	// get proper viewports for everyone, not just the original caller.
	allParticipants := m.calls.Participants(call.ID)
	others := make([]string, 0, len(allParticipants)-1)
	for _, p := range allParticipants {
		if p != m.username {
			others = append(others, p)
		}
	}

	phoneModel := phone.NewAnswering(
		m.username, call.ID, others,
		m.calls, m.reg,
		calleeP.IncomingChar,
		m.out, m.width, m.height,
	)

	return "", launchAppCmd(phoneModel)
}

// rejectCommand declines the most recent pending incoming call.
func rejectCommand(m Model) (string, tea.Cmd) {
	if m.calls == nil {
		return "%PHONE-E-NOCALLS, phone system not initialized.", nil
	}

	callID := findPendingCallID(m)
	if callID == "" {
		return "%PHONE-W-NOCALL, no incoming call to reject.", nil
	}

	if err := m.calls.Reject(callID, m.username); err != nil {
		return fmt.Sprintf("%%PHONE-E-REJECT, %v", err), nil
	}
	return "Call rejected.", nil
}

// findPendingCallID returns the call-id of the most recent incoming ring,
// stored directly on the model when the ring event arrived.
func findPendingCallID(m Model) string {
	return m.pendingCallID
}

// launchAppCmd returns a tea.Cmd that, when executed, sends a
// launchAppMsg to the lobby's Update loop so it can set m.activeApp.
// We can't mutate the model directly from a command handler (handlers
// only return string + tea.Cmd), so we use a message to carry the app.
func launchAppCmd(a app.App) tea.Cmd {
	return func() tea.Msg {
		return launchAppMsg{app: a}
	}
}

// launchAppMsg is received by the lobby's Update and triggers the
// transition into the given app.
type launchAppMsg struct {
	app app.App
}
