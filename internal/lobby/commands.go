package lobby

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/app"
	"github.com/klingon00/retro-vax-bbs/internal/createuser"
	"github.com/klingon00/retro-vax-bbs/internal/phone"
	"github.com/klingon00/retro-vax-bbs/internal/setpassword"
	"github.com/klingon00/retro-vax-bbs/internal/setplan"
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

// helpTopic holds usage and description for one command or group.
type helpTopic struct {
	cmd   string // canonical command name shown in HELP output
	usage string // full usage line (empty if cmd is self-describing)
	desc  string // one-line description
	admin bool   // true = only shown to admin-role users
}

// userHelpTopics is the ordered list of user-facing commands shown by HELP.
var userHelpTopics = []helpTopic{
	{
		cmd:   "WHO",
		usage: "WHO  (or: SHOW USERS)",
		desc:  "List connected users and their current app.",
	},
	{
		cmd:   "FINGER <username>",
		usage: "FINGER <username>  (or: SHOW USER <username>)",
		desc:  "Show a user's status, last login, and plan.",
	},
	{
		cmd:   "TIME",
		usage: "TIME  (or: SHOW TIME)",
		desc:  "Display the current server time.",
	},
	{
		cmd:   "PHONE",
		usage: "PHONE  or  PHONE <username>  or  DIAL <username>",
		desc:  "Enter the PHONE facility, or dial a user directly. Type HELP inside PHONE for full command list.",
	},
	{
		cmd:   "ANSWER",
		usage: "ANSWER",
		desc:  "Answer an incoming call.",
	},
	{
		cmd:   "REJECT",
		usage: "REJECT",
		desc:  "Decline an incoming call.",
	},
	{
		cmd:   "SET PLAN",
		usage: "SET PLAN",
		desc:  "Open an inline editor to write your FINGER blurb. Ctrl+S saves, Esc cancels.",
	},
	{
		cmd:   "SET PLAN CLEAR",
		usage: "SET PLAN CLEAR",
		desc:  "Remove your FINGER blurb immediately without opening the editor.",
	},
	{
		cmd:   "SET PASSWORD",
		usage: "SET PASSWORD",
		desc:  "Change your own password. Asks for your current password first.",
	},
	{
		cmd:   "HELP",
		usage: "HELP  or  HELP <command>",
		desc:  "Show this list, or detailed usage for a specific command.",
	},
	{
		cmd:   "LOGOUT",
		usage: "LOGOUT",
		desc:  "End your session.",
	},
}

// adminHelpTopics is the ordered list of admin-only commands shown by HELP
// when the requesting user has the admin role.
var adminHelpTopics = []helpTopic{
	{
		cmd:   "LIST PENDING",
		usage: "LIST PENDING",
		desc:  "Show accounts awaiting approval (open-with-approval mode).",
		admin: true,
	},
	{
		cmd:   "APPROVE <username>",
		usage: "APPROVE <username>",
		desc:  "Activate a pending account.",
		admin: true,
	},
	{
		cmd:   "DENY <username>",
		usage: "DENY <username>",
		desc:  "Deny a pending account request; frees the username.",
		admin: true,
	},
	{
		cmd:   "LIST USERS",
		usage: "LIST USERS",
		desc:  "Show all accounts with role, status, and last login.",
		admin: true,
	},
	{
		cmd:   "DELETE USER <username>",
		usage: "DELETE USER <username>",
		desc:  "Permanently remove an account and free the username.",
		admin: true,
	},
	{
		cmd:   "CREATE USER <username> [role]",
		usage: "CREATE USER <username> [role]",
		desc:  "Create an account directly (closed-mode utility). Prompts for a masked password.",
		admin: true,
	},
	{
		cmd:   "KICK <username>",
		usage: "KICK <username>",
		desc:  "Disconnect a user's active session immediately. They can reconnect.",
		admin: true,
	},
	{
		cmd:   "BAN <username> <duration>",
		usage: "BAN <username> <duration>",
		desc:  "Suspend an account. Duration: 30s, 15m, 2h, 7d, 2w, perm. Type HELP BAN for details.",
		admin: true,
	},
	{
		cmd:   "UNBAN <username>",
		usage: "UNBAN <username>",
		desc:  "Lift a ban and restore the account to active.",
		admin: true,
	},
	{
		cmd:   "UNLOCK <username>",
		usage: "UNLOCK <username>",
		desc:  "Clear a login lockout (triggered after 5 failed password attempts).",
		admin: true,
	},
	{
		cmd:   "RESET PASSWORD <username>",
		usage: "RESET PASSWORD <username>",
		desc:  "Set a user's password directly. Prompts for a masked new password.",
		admin: true,
	},
	{
		cmd:   "EXPIRE PASSWORD <username>",
		usage: "EXPIRE PASSWORD <username>",
		desc:  "Force a mandatory password change on the user's next login.",
		admin: true,
	},
	{
		cmd:   "INVITE CREATE",
		usage: "INVITE CREATE [uses] [duration]",
		desc:  "Generate an invite code. e.g. INVITE CREATE 5 7d. Type HELP INVITE for details.",
		admin: true,
	},
	{
		cmd:   "LIST INVITES",
		usage: "LIST INVITES",
		desc:  "Show all invite codes with remaining uses and expiry.",
		admin: true,
	},
	{
		cmd:   "PURGE PENDING",
		usage: "PURGE PENDING",
		desc:  "Immediately purge expired pending accounts (also runs automatically).",
		admin: true,
	},
}

// adminCommandKeys is the set of canonical command keys (matching the
// commands-map keys and argCommands prefixes verbatim) that require the
// admin role. Derived from adminHelpTopics — the same declarative table
// that already drives what HELP shows — so a new admin command only has
// to be added there to also be hidden from non-admins in dispatch(): no
// second list to keep in sync by hand.
var adminCommandKeys = buildAdminCommandKeys()

func buildAdminCommandKeys() map[string]bool {
	keys := map[string]bool{
		// INVITE (bare, no args) shares an admin-only parent with
		// INVITE CREATE but has no adminHelpTopics entry of its own —
		// it's the "Usage: INVITE CREATE ..." fallback, registered
		// separately in the commands map.
		"INVITE": true,
	}
	for _, t := range adminHelpTopics {
		keys[adminTopicVerb(t.cmd)] = true
	}
	return keys
}

// adminTopicVerb extracts the command verb from a helpTopic.cmd string,
// e.g. "DELETE USER <username>" -> "DELETE USER", stopping at the first
// "<...>" or "[...]" placeholder token.
func adminTopicVerb(cmd string) string {
	parts := strings.Fields(strings.ToUpper(cmd))
	verbs := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.HasPrefix(p, "<") || strings.HasPrefix(p, "[") {
			break
		}
		verbs = append(verbs, p)
	}
	return strings.Join(verbs, " ")
}

// topicDetails holds extended help text shown by HELP <command>.
// Keys are uppercased canonical names; values are multi-line detail strings.
var topicDetails = map[string]string{
	"BAN": `BAN <username> <duration>

  Suspends an account for the given duration. The user is disconnected
  immediately if currently online. Timed bans lift automatically on the
  user's next login attempt after expiry — no admin action needed.

  Duration formats:
    30s    30 seconds
    15m    15 minutes
    2h     2 hours
    7d     7 days
    2w     2 weeks
    perm   Permanent (until UNBAN is run)

  Examples:
    BAN alice 24h
    BAN troublemaker perm`,

	"INVITE": `INVITE CREATE [uses] [duration]

  Generates an invite code in the format word-word-NN (e.g. swift-oak-42).
  Codes are short and safe to share verbally or by message.

  Arguments (both optional):
    uses      Number of times the code can be used (default: 1)
    duration  How long until the code expires (same format as BAN)

  Examples:
    INVITE CREATE              — 1 use, no expiry
    INVITE CREATE 5            — 5 uses, no expiry
    INVITE CREATE 3 7d         — 3 uses, expires in 7 days
    INVITE CREATE 1 24h        — 1 use, expires in 24 hours
    LIST INVITES               — show all codes and remaining uses`,

	"CREATE USER": `CREATE USER <username> [role]

  Creates a new account directly — the in-lobby equivalent of the
  cmd/adduser CLI tool, for closed-mode admin use. Opens a masked
  password prompt; the password is never typed on the command line
  or shown in scrollback history.

    role   'user' (default) or 'admin'

  Examples:
    CREATE USER alice
    CREATE USER sysop2 admin`,

	"FINGER": `FINGER <username>  (or: SHOW USER <username>)

  Displays a user's profile:
    - Current connection status and active app
    - Last login time
    - Plan text (set with SET PLAN)

  Invisible admin accounts appear as "no information available" to
  regular users — same rule as WHO.`,

	"SET PLAN": `SET PLAN

  Opens an inline editor for your FINGER blurb. The editor supports
  full cursor movement, word navigation, and paste.

    Ctrl+S    Save and return to the lobby
    Esc       Cancel without saving
    Ctrl+C    Cancel without saving

  Character limit: 512. A live counter is shown in the editor.
  Use SET PLAN CLEAR to remove your blurb without opening the editor.`,

	"PHONE": `PHONE  or  PHONE <username>  or  DIAL <username>

  Enters the VAX-BBS PHONE facility.

    PHONE              Open PHONE in idle mode
    PHONE <username>   Dial a user directly
    DIAL <username>    Same as PHONE <username>

  Inside PHONE, type HELP for the full command and keyboard reference.
  The switch-hook character % enters command mode during an active call.`,

	"SET PASSWORD": `SET PASSWORD

  Change your own password. Asks for your current password first — this
  protects an unattended session from being used to lock out the real
  account owner — then a new password and confirmation.

    Esc / Ctrl+C   Cancel without changing anything

  Five wrong current-password attempts locks the account the same as a
  failed login attempt; contact an admin to UNLOCK it early.`,

	"RESET PASSWORD": `RESET PASSWORD <username>

  Sets a user's password directly — for password-reset requests or
  recovering a forgotten admin password. No current-password check (the
  admin doesn't know it). Prompts for a masked new password and
  confirmation.

  Example:
    RESET PASSWORD alice`,

	"EXPIRE PASSWORD": `EXPIRE PASSWORD <username>

  Forces a mandatory password change on the user's next login. Their
  current password still works for that one login, but the session goes
  straight into a password-change screen before the lobby loads — it
  cannot be skipped or dismissed.

  Example:
    EXPIRE PASSWORD alice`,

	"WHO": `WHO  (or: SHOW USERS)

  Lists all connected users and their current app (LOBBY, PHONE, etc.).
  Admin accounts are hidden from regular users unless the admin has
  opted in to visibility with SET VISIBLE.`,

	"HELP": `HELP  or  HELP <command>

  HELP alone lists all available commands with a one-line description.
  Admin commands are shown only to users with the admin role.

  HELP <command> shows detailed usage for that command. Examples:
    HELP FINGER
    HELP SET PLAN
    HELP PHONE
    HELP WHO`,
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
		// SET PLAN commands.
		"SET PLAN":       setPlanCommand,
		"SET PLAN CLEAR": setPlanClearCommand,
		// SET PASSWORD — self-service, available to everyone; not
		// admin-gated (see adminHelpTopics/adminCommandKeys note below).
		"SET PASSWORD": setPasswordCommand,
		// Admin commands (enforced by role check inside each handler).
		"LIST PENDING":    listPendingCommand,
		"LIST USERS":      listUsersCommand,
		"LIST INVITES":    listInvitesCommand,
		"INVITE":          inviteUsage,
		"INVITE CREATE":   inviteCreateCommand,
		"APPROVE":         approveUsage,
		"DENY":            denyUsage,
		"DELETE USER":     deleteUserUsage,
		"CREATE USER":     createUserUsage,
		"UNLOCK":          unlockUsage,
		"RESET PASSWORD":  resetPasswordUsage,
		"EXPIRE PASSWORD": expirePasswordUsage,
		"KICK":            kickUsage,
		"BAN":             banUsage,
		"UNBAN":           unbanUsage,
		"PURGE PENDING":   purgePendingCommand,
	}

	argCommands = []struct {
		prefix  string
		handler argCommandHandler
	}{
		{"HELP", helpByTopic},
		{"SHOW USER", fingerByName},
		{"FINGER", fingerByName},
		// PHONE <user> and DIAL <user> both dial directly.
		{"PHONE", phoneDialCommand},
		{"DIAL", phoneDialCommand},
		// Admin argument commands.
		{"APPROVE", approveCommand},
		{"DENY", denyCommand},
		{"DELETE USER", deleteUserCommand},
		{"CREATE USER", createUserCommand},
		{"UNLOCK", unlockCommand},
		{"RESET PASSWORD", resetPasswordCommand},
		{"EXPIRE PASSWORD", expirePasswordCommand},
		{"KICK", kickCommand},
		{"BAN", banCommand},
		{"UNBAN", unbanCommand},
		{"INVITE CREATE", inviteCreateArgCommand},
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
//
// Non-admins never reach an admin-only handler, not even its no-args
// usage text: dispatch checks adminCommandKeys before calling anything,
// and reports admin commands as unrecognized rather than "access denied" —
// same wording a typo would get, so a regular user typing BAN can't tell
// it from typing gibberish. This is on top of, not instead of, each
// admin handler's own requireAdmin/requireAdminLogged check.
func dispatch(line string, m Model) (output string, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			output = "Internal error running that command (recovered safely). Try again or contact an admin."
			cmd = nil
		}
	}()

	// Expand DCL-style command abbreviations to canonical form before the
	// exact/prefix tables run (see abbrev.go, decision 6). Role-scoped, so a
	// non-admin's abbreviation can never resolve to — or be revealed via an
	// ambiguity message by — an admin command. An ambiguous prefix
	// short-circuits with a helpful message; anything else yields a line the
	// tables below consume unchanged. The adminCommandKeys gates below still
	// run, so this is never a back door into an admin handler.
	resolvedLine, ambiguousMsg := resolveAbbrev(line, m.role)
	if ambiguousMsg != "" {
		return ambiguousMsg, nil
	}
	line = resolvedLine

	upper := strings.ToUpper(strings.TrimSpace(line))

	// Prefix match for argument-taking commands.
	for _, ac := range argCommands {
		pfx := ac.prefix + " "
		if strings.HasPrefix(upper, pfx) {
			if m.role != "admin" && adminCommandKeys[ac.prefix] {
				return unknownCommandMsg(upper), nil
			}
			// Extract the argument from the original line (not uppercased)
			// so usernames are passed through as the user typed them.
			arg := strings.TrimSpace(line[len(pfx):])
			return ac.handler(m, arg)
		}
	}

	// Exact match for everything else.
	if m.role != "admin" && adminCommandKeys[upper] {
		return unknownCommandMsg(upper), nil
	}
	handler, ok := commands[upper]
	if !ok {
		return unknownCommandMsg(upper), nil
	}
	return handler(m)
}

func unknownCommandMsg(upper string) string {
	return fmt.Sprintf("%q is not a recognized command. Type HELP for a list.", upper)
}

func helpCommand(m Model) (string, tea.Cmd) {
	var b strings.Builder
	b.WriteString("Available commands:\n\n")

	b.WriteString("  User commands:\n")
	for _, t := range userHelpTopics {
		b.WriteString("  ")
		b.WriteString(t.usage)
		b.WriteString("\n      ")
		b.WriteString(t.desc)
		b.WriteString("\n")
	}

	if m.role == "admin" {
		b.WriteString("\n  Admin commands:\n")
		for _, t := range adminHelpTopics {
			b.WriteString("  ")
			b.WriteString(t.usage)
			b.WriteString("\n      ")
			b.WriteString(t.desc)
			b.WriteString("\n")
		}
	}

	if m.role == "admin" {
		b.WriteString("\n  Type HELP <command> for detailed usage, e.g. HELP BAN or HELP INVITE.")
	} else {
		b.WriteString("\n  Type HELP <command> for detailed usage, e.g. HELP FINGER or HELP PHONE.")
	}
	return b.String(), nil
}

// helpByTopic handles HELP <command> — shows extended detail for one command.
// Admin-only topics are hidden from non-admin users, same as HELP itself.
func helpByTopic(m Model, arg string) (string, tea.Cmd) {
	if arg == "" {
		return helpCommand(m)
	}
	upper := strings.ToUpper(strings.TrimSpace(arg))

	// Determine whether the requested topic is admin-only by checking
	// adminHelpTopics first. If it is and the caller is not admin, treat
	// it as unknown — don't confirm the command exists.
	isAdminTopic := false
	for _, t := range adminHelpTopics {
		topicUpper := strings.ToUpper(t.cmd)
		// Strip trailing argument placeholder (e.g. "<username>") for matching.
		topicKey := strings.ToUpper(strings.Fields(t.usage)[0])
		if len(strings.Fields(t.usage)) > 1 {
			// Rebuild just the verb portion (everything before first <arg>).
			parts := strings.Fields(topicUpper)
			verbs := []string{}
			for _, p := range parts {
				if strings.HasPrefix(p, "<") {
					break
				}
				verbs = append(verbs, p)
			}
			topicKey = strings.Join(verbs, " ")
		}
		if upper == topicKey || upper == topicUpper ||
			strings.HasPrefix(topicUpper, upper+" ") {
			isAdminTopic = true
			break
		}
	}

	// Also check topicDetails keys that belong to admin topics.
	adminDetailKeys := map[string]bool{"BAN": true, "INVITE": true, "DENY": true}
	if adminDetailKeys[upper] {
		isAdminTopic = true
	}

	if isAdminTopic && m.role != "admin" {
		return fmt.Sprintf("No help available for %q. Type HELP for the full command list.", arg), nil
	}

	// Check topicDetails for extended text.
	if detail, ok := topicDetails[upper]; ok {
		return detail, nil
	}

	// Fall back: match against user topic list.
	for _, t := range userHelpTopics {
		if strings.ToUpper(t.cmd) == upper || strings.HasPrefix(strings.ToUpper(t.cmd), upper+" ") {
			return t.usage + "\n    " + t.desc, nil
		}
	}
	// Admin topics (role already verified above).
	for _, t := range adminHelpTopics {
		if strings.ToUpper(t.cmd) == upper || strings.HasPrefix(strings.ToUpper(t.cmd), upper+" ") {
			return t.usage + "\n    " + t.desc, nil
		}
	}

	return fmt.Sprintf("No help available for %q. Type HELP for the full command list.", arg), nil
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
		// Stored UTC rendered in server-local time for display — period-authentic
		// VAX/VMS showed users local wall-clock, and TIME/WHO already do (audit #1).
		fmt.Fprintf(&b, "Last login: %s\n",
			strings.ToUpper(user.LastLoginAt.Time.Local().Format("02-Jan-2006 15:04:05")))
	} else {
		fmt.Fprintf(&b, "Last login: (never)\n")
	}

	b.WriteString("\nPlan:\n")
	if user.PlanText.Valid && strings.TrimSpace(user.PlanText.String) != "" {
		cleaned := store.StripANSI(user.PlanText.String)
		// Indent every line of a multi-line plan, not just the first.
		indented := "  " + strings.ReplaceAll(cleaned, "\n", "\n  ")
		b.WriteString(indented)
		b.WriteString("\n")
	} else {
		b.WriteString("  (no plan set)\n")
	}

	return b.String(), nil
}

// ---- SET PLAN -----------------------------------------------------------

// setPlanCommand launches the inline SET PLAN editor.
func setPlanCommand(m Model) (string, tea.Cmd) {
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	currentPlan, err := m.db.GetPlan(m.username)
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-DBFAIL, could not retrieve plan: %v", err), nil
	}
	editor := setplan.NewApp(m.db, m.username, currentPlan, m.width)
	return "", launchAppCmd(editor)
}

// setPlanClearCommand removes the user's plan text immediately (no editor).
func setPlanClearCommand(m Model) (string, tea.Cmd) {
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	if err := m.db.ClearPlan(m.username); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-DBFAIL, could not clear plan: %v", err), nil
	}
	return "%VAX-BBS-S-PLANCLR, Plan cleared.", nil
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
		return phone.ErrorMessage(err, username), nil
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

// ---- Admin guard --------------------------------------------------------

// requireAdmin returns an error string if m.role != "admin", or "" if ok.
// Use for read-only admin commands (LIST PENDING, LIST USERS, LIST
// INVITES) that don't change any state and so don't need an audit trail.
func requireAdmin(m Model) string {
	if m.role != "admin" {
		return "%VAX-BBS-E-NOACCESS, you do not have administrative access."
	}
	return ""
}

// requireAdminLogged is requireAdmin plus an audit log line on success.
// Use for every admin command that changes state (APPROVE, DENY, KICK,
// BAN, UNBAN, UNLOCK, DELETE USER, CREATE USER, INVITE CREATE, PURGE
// PENDING). It's the single chokepoint all of those pass through before
// doing anything, so logging here — rather than in each handler body —
// means a future mutating admin command can't skip the audit trail without
// also skipping its own security gate. This logs the attempt, not a
// confirmed successful mutation: several handlers return plain English on
// success with no machine-checkable marker, so "admin ran this command
// with these arguments" is the reliable signal, same spirit as logging
// auth failures rather than only auth successes.
func requireAdminLogged(m Model, action, detail string) string {
	if m.role != "admin" {
		return "%VAX-BBS-E-NOACCESS, you do not have administrative access."
	}
	if detail != "" {
		log.Printf("admin action: %s %s %s", m.username, action, detail)
	} else {
		log.Printf("admin action: %s %s", m.username, action)
	}
	return ""
}

// The last-usable-admin invariant (never let BAN or DELETE USER drop the
// reachable-admin count to zero) is enforced atomically in the store layer —
// store.BanUser / store.DeleteUser fold the count check into their WHERE
// clause and return store.ErrLastUsableAdmin when they refuse. There is
// deliberately no Go-side pre-check here: an earlier CountUsableAdmins()-then-
// mutate version was a TOCTOU race (audit 2026-07-05 finding #3) — two admins
// banning each other concurrently could both pass the read, then both mutate.

// ---- APPROVE / DENY -----------------------------------------------------

func approveUsage(m Model) (string, tea.Cmd) {
	return "Usage: APPROVE <username>", nil
}
func denyUsage(m Model) (string, tea.Cmd) {
	return "Usage: DENY <username>", nil
}

func approveCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "APPROVE", username); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	if err := m.db.ApprovePendingAccount(username); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-APPROVE, %v", err), nil
	}
	return fmt.Sprintf("Account '%s' approved. The user may now log in.", username), nil
}

func denyCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "DENY", username); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	if err := m.db.RejectPendingAccount(username); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-DENY, %v", err), nil
	}
	return fmt.Sprintf("Account request for '%s' denied and removed.", username), nil
}

// ---- LIST PENDING -------------------------------------------------------

func listPendingCommand(m Model) (string, tea.Cmd) {
	if e := requireAdmin(m); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	users, err := m.db.ListPendingAccounts()
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-LIST, %v", err), nil
	}
	if len(users) == 0 {
		return "No accounts pending approval.", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  %-20s  %-30s  %s\n", "USERNAME", "EMAIL", "REQUESTED"))
	sb.WriteString("  " + strings.Repeat("-", 65))
	for _, u := range users {
		email := "(none)"
		if u.Email.String != "" {
			email = u.Email.String
		}
		sb.WriteString(fmt.Sprintf("\n  %-20s  %-30s  %s",
			u.Username, email, u.CreatedAt.Local().Format("02-Jan-2006 15:04"))) // .Local(): stored UTC → server-local (audit #1)
	}
	sb.WriteString(fmt.Sprintf("\n\n  %d pending. Use APPROVE <user> or DENY <user>.", len(users)))
	return sb.String(), nil
}

// ---- UNLOCK -------------------------------------------------------------

func unlockUsage(m Model) (string, tea.Cmd) {
	return "Usage: UNLOCK <username>  — clear login lockout for a user", nil
}

func unlockCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "UNLOCK", username); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	user, err := m.db.GetUserByUsername(username)
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-UNLOCK, user '%s' not found.", username), nil
	}
	if err := m.db.ClearFailedAttempts(user.ID); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-UNLOCK, %v", err), nil
	}
	return fmt.Sprintf("Lockout cleared for '%s'. They may now log in.", username), nil
}

// ---- KICK ---------------------------------------------------------------

func kickUsage(m Model) (string, tea.Cmd) {
	return "Usage: KICK <username>  — disconnect a user's active session", nil
}

func kickCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "KICK", username); e != "" {
		return e, nil
	}
	if m.reg == nil {
		return "KICK is not available — session registry not initialized.", nil
	}
	if m.reg.Kick(username) {
		return fmt.Sprintf("'%s' has been disconnected.", username), nil
	}
	return fmt.Sprintf("'%s' is not currently connected.", username), nil
}

// ---- BAN / UNBAN --------------------------------------------------------

func banUsage(m Model) (string, tea.Cmd) {
	return "Usage: BAN <username> <duration>  (e.g. 30m, 2h, 7d, perm)", nil
}
func unbanUsage(m Model) (string, tea.Cmd) {
	return "Usage: UNBAN <username>", nil
}

func banCommand(m Model, args string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "BAN", args); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return banUsage(m)
	}
	username, durStr := parts[0], parts[1]

	until, display, err := parseBanDuration(durStr)
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-BAN, %v", err), nil
	}
	// The last-usable-admin guard is enforced atomically inside BanUser
	// (audit finding #3): the count-and-ban is one SQL statement, so two
	// admins banning each other concurrently can't both slip through.
	if err := m.db.BanUser(username, until); err != nil {
		switch {
		case errors.Is(err, store.ErrLastUsableAdmin):
			return fmt.Sprintf("%%VAX-BBS-E-LASTADMIN, Cannot BAN %q — this is the last usable admin account.", username), nil
		case errors.Is(err, store.ErrNotFound):
			return fmt.Sprintf("%%VAX-BBS-E-NOUSER, User '%s' not found.", username), nil
		default:
			return fmt.Sprintf("%%VAX-BBS-E-BAN, %v", err), nil
		}
	}
	// Only disconnect the user once the ban is durably applied — avoids
	// kicking someone we then refuse to ban in the last-admin race.
	m.reg.Kick(username)
	return fmt.Sprintf("'%s' has been banned (%s).", username, display), nil
}

func unbanCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "UNBAN", username); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	if err := m.db.UnbanUser(username); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-UNBAN, %v", err), nil
	}
	return fmt.Sprintf("Ban lifted for '%s'. They may now log in.", username), nil
}

// parseBanDuration parses strings like "30m", "2h", "7d", "perm".
// Returns (until *time.Time, display string, error).
// until is nil for permanent bans.
func parseBanDuration(s string) (*time.Time, string, error) {
	switch strings.ToLower(s) {
	case "perm", "permanent", "forever", "never":
		return nil, "permanent", nil
	}
	if len(s) < 2 {
		return nil, "", fmt.Errorf("invalid duration %q; use e.g. 30m, 2h, 7d, perm", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return nil, "", fmt.Errorf("invalid duration %q; use e.g. 30m, 2h, 7d, perm", s)
	}
	var d time.Duration
	var unit string
	switch s[len(s)-1] {
	case 's':
		d, unit = time.Duration(n)*time.Second, fmt.Sprintf("%ds", n)
	case 'm':
		d, unit = time.Duration(n)*time.Minute, fmt.Sprintf("%dm", n)
	case 'h':
		d, unit = time.Duration(n)*time.Hour, fmt.Sprintf("%dh", n)
	case 'd':
		d, unit = time.Duration(n)*24*time.Hour, fmt.Sprintf("%d days", n)
	case 'w':
		d, unit = time.Duration(n)*7*24*time.Hour, fmt.Sprintf("%d weeks", n)
	default:
		return nil, "", fmt.Errorf("unknown unit %q; use s/m/h/d/w or perm", string(s[len(s)-1]))
	}
	t := time.Now().Add(d)
	return &t, unit, nil
}

// ---- INVITE CREATE ------------------------------------------------------

func inviteUsage(m Model) (string, tea.Cmd) {
	return "Usage: INVITE CREATE [uses] [duration]  (e.g. INVITE CREATE 5 7d)", nil
}

// inviteCreateCommand handles bare "INVITE CREATE" (no arguments).
func inviteCreateCommand(m Model) (string, tea.Cmd) {
	return inviteCreateArgCommand(m, "")
}

// inviteCreateArgCommand handles "INVITE CREATE [args]".
func inviteCreateArgCommand(m Model, args string) (string, tea.Cmd) {
	detail := args
	if detail == "" {
		detail = "(default: 1 use, no expiry)"
	}
	if e := requireAdminLogged(m, "INVITE CREATE", detail); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	// Parse optional: uses count, expiry duration.
	uses := 1
	expiresAt := store.NeverExpires()
	expiryDisplay := "never"

	parts := strings.Fields(args)
	if len(parts) >= 1 {
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 1 {
			return "%VAX-BBS-E-INVITE, uses must be a positive integer.", nil
		}
		uses = n
	}
	if len(parts) >= 2 {
		_, display, err := parseBanDuration(parts[1]) // reuse duration parser
		if err != nil {
			return fmt.Sprintf("%%VAX-BBS-E-INVITE, %v", err), nil
		}
		dur := parts[1]
		_, expiresPtr, parseErr := parseBanDurationFull(dur)
		if parseErr != nil {
			return fmt.Sprintf("%%VAX-BBS-E-INVITE, %v", parseErr), nil
		}
		if expiresPtr != nil {
			expiresAt = *expiresPtr
		}
		expiryDisplay = display
	}

	// Look up admin's user ID for the created_by FK.
	adminUser, err := m.db.GetUserByUsername(m.username)
	if err != nil {
		return "%VAX-BBS-E-INVITE, could not look up your account.", nil
	}

	code := generateInviteCode()
	if err := m.db.CreateInvite(code, adminUser.ID, uses, expiresAt); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-INVITE, %v", err), nil
	}
	return fmt.Sprintf("Invite code created: %s\n  Uses: %d  Expires: %s",
		code, uses, expiryDisplay), nil
}

// parseBanDurationFull returns (display, *time.Time, error) for durations.
// nil time.Time = permanent/no expiry.
func parseBanDurationFull(s string) (string, *time.Time, error) {
	switch strings.ToLower(s) {
	case "perm", "permanent", "forever", "never":
		return "never", nil, nil
	}
	if len(s) < 2 {
		return "", nil, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return "", nil, fmt.Errorf("invalid duration %q", s)
	}
	var d time.Duration
	var unit string
	switch s[len(s)-1] {
	case 's':
		d, unit = time.Duration(n)*time.Second, fmt.Sprintf("%ds", n)
	case 'm':
		d, unit = time.Duration(n)*time.Minute, fmt.Sprintf("%dm", n)
	case 'h':
		d, unit = time.Duration(n)*time.Hour, fmt.Sprintf("%dh", n)
	case 'd':
		d, unit = time.Duration(n)*24*time.Hour, fmt.Sprintf("%d days", n)
	case 'w':
		d, unit = time.Duration(n)*7*24*time.Hour, fmt.Sprintf("%d weeks", n)
	default:
		return "", nil, fmt.Errorf("unknown unit %q", string(s[len(s)-1]))
	}
	t := time.Now().Add(d)
	return unit, &t, nil
}

// ---- LIST INVITES -------------------------------------------------------

func listInvitesCommand(m Model) (string, tea.Cmd) {
	if e := requireAdmin(m); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	invites, err := m.db.ListInvites()
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-LIST, %v", err), nil
	}
	if len(invites) == 0 {
		return "No invite codes. Use INVITE CREATE to generate one.", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  %-22s  %5s  %s\n", "CODE", "USES", "EXPIRES"))
	sb.WriteString("  " + strings.Repeat("-", 45))
	for _, inv := range invites {
		sb.WriteString(fmt.Sprintf("\n  %-22s  %5d  %s",
			inv.Code, inv.UsesRemaining, inv.DisplayExpiry()))
	}
	return sb.String(), nil
}

// ---- Invite code generation ---------------------------------------------

var inviteAdj = []string{
	"able", "amber", "bold", "brave", "brisk", "calm", "clean", "clear",
	"cool", "crisp", "dark", "deep", "fair", "fast", "firm", "free",
	"gold", "good", "gray", "hard", "keen", "kind", "late", "light",
	"long", "mild", "neat", "open", "quick", "quiet", "safe", "sharp",
	"slim", "slow", "soft", "still", "swift", "tall", "warm", "wide",
}

var inviteNoun = []string{
	"arc", "ash", "bay", "birch", "blade", "brook", "cedar", "cliff",
	"code", "cove", "creek", "dale", "dawn", "disk", "dusk", "echo",
	"elm", "fern", "ford", "gate", "glen", "grove", "hill", "key",
	"lake", "leaf", "link", "log", "maple", "marsh", "mist", "moss",
	"oak", "path", "peak", "pine", "pond", "reed", "ridge", "rock",
}

func generateInviteCode() string {
	b := make([]byte, 4)
	if _, err := crand.Read(b); err != nil {
		// Fallback to time-seeded if crypto/rand fails (shouldn't happen).
		b[0] = byte(time.Now().UnixNano())
	}
	adj := inviteAdj[int(b[0])%len(inviteAdj)]
	noun := inviteNoun[int(b[1])%len(inviteNoun)]
	num := 10 + int(b[2])%80
	return fmt.Sprintf("%s-%s-%02d", adj, noun, num)
}

// ---- LIST USERS ---------------------------------------------------------

func listUsersCommand(m Model) (string, tea.Cmd) {
	if e := requireAdmin(m); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	users, err := m.db.ListAllUsers()
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-LISTUSERS, %v", err), nil
	}
	if len(users) == 0 {
		return "No accounts found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n  %-20s  %-9s  %-9s  %s\n", "Username", "Role", "Status", "Last Login"))
	sb.WriteString(fmt.Sprintf("  %-20s  %-9s  %-9s  %s\n", "--------", "----", "------", "----------"))
	for _, u := range users {
		lastLogin := "never"
		if u.LastLoginAt.Valid {
			lastLogin = u.LastLoginAt.Time.Local().Format("02-Jan-2006") // .Local(): stored UTC → server-local (audit #1)
		}
		status := u.Status
		if u.BannedUntil.Valid {
			if u.BannedUntil.Time.Year() >= 2090 {
				status = "banned(perm)"
			} else {
				status = "banned"
			}
		}
		if u.LockedUntil.Valid && time.Now().Before(u.LockedUntil.Time) {
			status = "locked"
		}
		sb.WriteString(fmt.Sprintf("  %-20s  %-9s  %-9s  %s\n", u.Username, u.Role, status, lastLogin))
	}
	sb.WriteString(fmt.Sprintf("\n  %d account(s) total.", len(users)))
	return sb.String(), nil
}

// ---- DELETE USER --------------------------------------------------------

func deleteUserUsage(m Model) (string, tea.Cmd) {
	return "Usage: DELETE USER <username>  — permanently remove an account", nil
}

func deleteUserCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "DELETE USER", username); e != "" {
		return e, nil
	}
	if username == "" {
		return deleteUserUsage(m)
	}
	// Safety check: don't let an admin delete themselves.
	if strings.EqualFold(username, m.username) {
		return "%VAX-BBS-E-SELF, Cannot DELETE USER on your own account.", nil
	}
	// The last-usable-admin guard is enforced atomically inside DeleteUser
	// (audit finding #3), the same one-statement check-and-mutate as BanUser.
	if err := m.db.DeleteUser(username); err != nil {
		switch {
		case errors.Is(err, store.ErrLastUsableAdmin):
			return fmt.Sprintf("%%VAX-BBS-E-LASTADMIN, Cannot DELETE USER %q — this is the last usable admin account.", username), nil
		case errors.Is(err, store.ErrNotFound):
			return fmt.Sprintf("%%VAX-BBS-E-NOUSER, User '%s' not found.", username), nil
		default:
			return fmt.Sprintf("%%VAX-BBS-E-DELETE, %v", err), nil
		}
	}
	// Only disconnect once the delete is durably applied — see banCommand.
	_ = m.reg.Kick(username) // ignore "not online" errors
	return fmt.Sprintf("%%VAX-BBS-S-DELETED, Account '%s' has been permanently deleted.", username), nil
}

// ---- CREATE USER ---------------------------------------------------------

func createUserUsage(m Model) (string, tea.Cmd) {
	return "Usage: CREATE USER <username> [role]  (role: user (default) or admin)", nil
}

// createUserCommand validates the requested username and role, then
// launches the createuser app to collect a masked password. The account
// isn't created until the password prompt completes — see
// internal/createuser for that flow.
func createUserCommand(m Model, args string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "CREATE USER", args); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}

	parts := strings.Fields(args)
	if len(parts) < 1 {
		return createUserUsage(m)
	}
	username := parts[0]

	role := "user"
	if len(parts) >= 2 {
		role = strings.ToLower(parts[1])
		if role != "user" && role != "admin" {
			return fmt.Sprintf("%%VAX-BBS-E-CREATE, invalid role %q; must be 'user' or 'admin'.", parts[1]), nil
		}
	}

	if err := validateNewUsername(username); err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-CREATE, %v", err), nil
	}
	if _, err := m.db.GetUserByUsername(username); err == nil {
		return fmt.Sprintf("%%VAX-BBS-E-CREATE, username %q is already taken.", username), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("%%VAX-BBS-E-CREATE, %v", err), nil
	}

	editor := createuser.NewApp(m.db, m.username, username, role)
	return "", launchAppCmd(editor)
}

// validateNewUsername applies the same format rules as self-service
// registration (3-20 chars, letters/digits/underscore) but skips the
// reserved-word block — an admin creating accounts directly may
// legitimately want a name like "sysop", which self-registration blocks
// to stop impersonation attempts. The one exception is "new": it isn't just
// a reserved word, it's the registration routing sentinel (the public
// listener sends username "new" to registration, not login — see
// cmd/server/main.go), so an account named "new" is either unreachable
// (user role) or a confusing footgun (admin role). Block it here too, the
// same way BOOTSTRAP_ADMIN_USERNAME already does. Audit 2026-07-05 #5.
func validateNewUsername(s string) error {
	if len(s) < 3 {
		return fmt.Errorf("username must be at least 3 characters")
	}
	if len(s) > 20 {
		return fmt.Errorf("username must be 20 characters or fewer")
	}
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return fmt.Errorf("username may only contain letters, numbers, and underscores")
		}
	}
	if strings.EqualFold(s, "new") {
		return fmt.Errorf("username %q is reserved for self-registration and could never log in", s)
	}
	return nil
}

// ---- SET PASSWORD / RESET PASSWORD / EXPIRE PASSWORD --------------------

// setPasswordCommand launches the self-service password-change flow.
// Available to every authenticated user — deliberately not admin-gated,
// which is also why it's registered in userHelpTopics rather than
// adminHelpTopics: an adminHelpTopics entry would derive an
// adminCommandKeys["SET PASSWORD"] gate that collides with this exact
// dispatch key and would incorrectly hide it from non-admins too.
func setPasswordCommand(m Model) (string, tea.Cmd) {
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	editor := setpassword.NewSelfApp(m.db, m.username)
	return "", launchAppCmd(editor)
}

func resetPasswordUsage(m Model) (string, tea.Cmd) {
	return "Usage: RESET PASSWORD <username>", nil
}

// resetPasswordCommand validates the target exists, then launches the
// setpassword app to collect a masked new password. The password isn't
// changed until that prompt completes — see internal/setpassword.
func resetPasswordCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "RESET PASSWORD", username); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	if username == "" {
		return resetPasswordUsage(m)
	}
	if _, err := m.db.GetUserByUsername(username); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Sprintf("%%VAX-BBS-E-NOUSER, User '%s' not found.", username), nil
		}
		return fmt.Sprintf("%%VAX-BBS-E-RESET, %v", err), nil
	}
	editor := setpassword.NewAdminApp(m.db, m.username, username)
	return "", launchAppCmd(editor)
}

func expirePasswordUsage(m Model) (string, tea.Cmd) {
	return "Usage: EXPIRE PASSWORD <username>  — force a password change on next login", nil
}

// expirePasswordCommand is a single-phase mutation (no sub-app), so
// requireAdminLogged alone covers the audit trail — unlike RESET PASSWORD
// and CREATE USER, there's no follow-up prompt the admin could cancel.
func expirePasswordCommand(m Model, username string) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "EXPIRE PASSWORD", username); e != "" {
		return e, nil
	}
	if m.db == nil {
		return "%VAX-BBS-E-NODB, database unavailable.", nil
	}
	if username == "" {
		return expirePasswordUsage(m)
	}
	if err := m.db.ExpirePassword(username); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Sprintf("%%VAX-BBS-E-NOUSER, User '%s' not found.", username), nil
		}
		return fmt.Sprintf("%%VAX-BBS-E-EXPIRE, %v", err), nil
	}
	return fmt.Sprintf("%%VAX-BBS-S-EXPIRE, '%s' must set a new password on next login.", username), nil
}

// ---- PURGE PENDING ------------------------------------------------------

func purgePendingCommand(m Model) (string, tea.Cmd) {
	if e := requireAdminLogged(m, "PURGE PENDING", ""); e != "" {
		return e, nil
	}
	n, err := m.db.PurgeExpiredPendingAccounts(m.pendingExpiry)
	if err != nil {
		return fmt.Sprintf("%%VAX-BBS-E-PURGE, %v", err), nil
	}
	if m.pendingExpiry == 0 {
		return "%VAX-BBS-W-PURGE, PENDING_EXPIRY_DAYS is 0 — auto-expiry is disabled; nothing to purge.", nil
	}
	if n == 0 {
		return "%VAX-BBS-I-PURGE, No expired pending accounts found.", nil
	}
	return fmt.Sprintf("%%VAX-BBS-S-PURGE, Purged %d expired pending account(s).", n), nil
}
