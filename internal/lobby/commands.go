package lobby

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// commandHandler is the only shape a lobby command may take: no
// arguments (line-parsing happens in dispatch before lookup), returns
// text to display and an optional tea.Cmd for side effects such as
// tea.Quit.
//
// The commands map below is the entire user-reachable surface of the
// lobby. There is no path from raw user input to anything other than a
// lookup into this table — no eval, no shelling out, no reflection-based
// dispatch by command name. Adding a command means adding an entry here;
// that is the whole interface. This is the "closed command grammar"
// principle from the design doc, expressed directly in the type system
// rather than as a runtime check somewhere.
type commandHandler func() (string, tea.Cmd)

// commands is populated in init(), not as a composite-literal var
// initializer, on purpose: helpCommand()'s body reads commands (to list
// the available names), and commands' own initializer would reference
// helpCommand — a direct cycle in Go's static, lexical initialization
// dependency analysis, even though nothing about it is actually circular
// at runtime. init() runs after all package-level var declarations exist,
// which sidesteps that analysis entirely.
var commands map[string]commandHandler

func init() {
	commands = map[string]commandHandler{
		"HELP":   helpCommand,
		"WHO":    whoCommand,
		"LOGOUT": logoutCommand,
	}
}

// dispatch resolves one line of raw user input to a command handler and
// runs it under recover(). A bug or malicious input in a single handler
// can only ever affect the session that triggered it — never another
// session, never the server process.
//
// This is per-session crash isolation applied at the command level (the
// granularity that actually matters for a closed command-loop shell),
// layered on top of — not a replacement for — the session-level recover
// middleware in main.go. That middleware is the backstop for a panic
// outside the dispatch loop entirely (e.g. during session setup); this is
// the one that keeps a single bad command from ending your whole session.
func dispatch(line string) (output string, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			// TODO: once structured logging exists (see the auth
			// milestone), log r + debug.Stack() here along with the
			// username and raw input, the same way wish's recover
			// middleware does at the session level. For now this only
			// protects the session; it doesn't yet leave a trail.
			output = "Internal error running that command (recovered safely). Try again or contact an admin."
			cmd = nil
		}
	}()

	name := strings.ToUpper(strings.TrimSpace(line))
	handler, ok := commands[name]
	if !ok {
		return fmt.Sprintf("%q is not a recognized command. Type HELP for a list.", name), nil
	}
	return handler()
}

func helpCommand() (string, tea.Cmd) {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return "Available commands: " + strings.Join(names, ", "), nil
}

func whoCommand() (string, tea.Cmd) {
	// Stub. The real WHO (browsable list, admin invisibility rules,
	// multi-session counts like "alice (2 sessions)") needs a session
	// registry that doesn't exist until accounts do — that's the next
	// milestone per the design doc's build order, not this one.
	return "WHO is not wired up yet — no session registry exists until accounts do.", nil
}

func logoutCommand() (string, tea.Cmd) {
	return "Goodbye.", tea.Quit
}
