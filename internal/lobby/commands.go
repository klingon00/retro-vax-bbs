package lobby

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// commandHandler is the only shape a lobby command may take. Handlers
// receive the current Model so they can access session context (role,
// registry) without package-level variables or closures — this is the
// "option 2" approach that keeps future commands (PHONE, FINGER, etc.)
// from needing special wiring. The closed-command-grammar guarantee is
// preserved: the commands map is still the only path from raw user input
// to executing code.
type commandHandler func(m Model) (string, tea.Cmd)

// commands is the dispatch table — every string a user can type maps to
// exactly one handler, including aliases. Populated in init() to avoid
// the initialization cycle that arose when helpCommand read the commands
// map during its own package-level initialization.
var commands map[string]commandHandler

// helpEntries drives the HELP output display. Separate from the dispatch
// table so aliases are grouped visually rather than repeated as separate
// entries, and so display order is deterministic.
var helpEntries = []struct{ display string }{
	{"HELP"},
	{"LOGOUT"},
	{"TIME                        (or: SHOW TIME)"},
	{"WHO                         (or: SHOW USERS)"},
	{"SHOW <keyword>              (SHOW USERS, SHOW TIME)"},
}

func init() {
	commands = map[string]commandHandler{
		"HELP":       helpCommand,
		"LOGOUT":     logoutCommand,
		"TIME":       timeCommand,
		"SHOW TIME":  timeCommand,
		"WHO":        whoCommand,
		"SHOW USERS": whoCommand,
		// SHOW alone — helpful error rather than "unrecognized command",
		// matching DCL's own style of guiding the user toward valid syntax.
		"SHOW": showCommand,
	}
}

// dispatch resolves one line of raw user input to a handler and runs it
// under recover(). A panic in one handler affects only that session.
func dispatch(line string, m Model) (output string, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			output = "Internal error running that command (recovered safely). Try again or contact an admin."
			cmd = nil
		}
	}()

	name := strings.ToUpper(strings.TrimSpace(line))
	handler, ok := commands[name]
	if !ok {
		return fmt.Sprintf("%q is not a recognized command. Type HELP for a list.", name), nil
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
	// Go's reference time: Mon Jan 2 15:04:05 MST 2006
	// VMS format: 22-JUN-2026 15:30:24
	formatted := strings.ToUpper(now.Format("02-Jan-2006 15:04:05"))
	return fmt.Sprintf("%s", formatted), nil
}

// whoCommand lists active sessions per the registry visibility rules,
// in a VAX/VMS-inspired columnar format. The App column shows what each
// user is currently doing — "LOBBY" at the prompt, "PHONE" or "MAIL"
// once those apps are built and registered.
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

// showCommand handles bare SHOW with no keyword — guides the user toward
// valid SHOW subcommands rather than returning a generic "unrecognized"
// error, matching DCL's style of descriptive error messages.
func showCommand(m Model) (string, tea.Cmd) {
	return "SHOW requires a keyword. Try: SHOW USERS, SHOW TIME", nil
}
