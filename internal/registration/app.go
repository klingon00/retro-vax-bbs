// Package registration provides the Bubble Tea model for the self-service
// account registration flow. It handles two modes:
//
//   - invite-only: user provides username, invite code, and password.
//     A valid code immediately activates the account (no admin approval).
//
//   - open-with-approval: user provides username, email (optional), and
//     password. Account is created as pending; an admin must APPROVE it
//     before the user can log in.
//
// The model is launched by teaHandler when the connecting user's SSH
// username is "new" and REGISTRATION_MODE is not "closed".
package registration

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// state represents the current registration step.
type state int

const (
	stateUsername     state = iota // ask for desired username
	stateEmail                     // ask for email (open-with-approval only)
	stateEmailConfirm              // confirm email to catch typos
	stateInviteCode                // ask for invite code (invite-only only)
	statePassword                  // ask for password
	stateConfirm                   // confirm password
	stateDone                      // show result and wait for keypress to disconnect
)

// Model is the Bubble Tea model for the registration flow.
// It runs inline (no alt screen) — prompts appear sequentially.
type Model struct {
	mode          string // "invite-only" or "open-with-approval"
	db            *store.Store
	reg           *registry.Registry
	pendingExpiry time.Duration // 0 = never auto-expire pending accounts

	step     state
	username string
	email    string
	invite   string
	password string

	input        string // current line the user is typing
	emailConfirm string // second copy of email for typo check
	errMsg       string // validation error shown under the current prompt
	result       string // final success/failure message shown at stateDone
	done         bool   // signals Bubble Tea to quit
}

// New returns a fresh registration Model.
func New(mode string, db *store.Store, reg *registry.Registry, pendingExpiry time.Duration) Model {
	return Model{mode: mode, db: db, reg: reg, pendingExpiry: pendingExpiry}
}

func (m Model) Done() bool { return m.done }

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			m.done = true
			return m, tea.Quit

		case tea.KeyEnter:
			return m.submit()

		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
				m.errMsg = ""
			}
			return m, nil

		case tea.KeyRunes:
			// Allow space in password fields; strip from username/invite.
			r := string(msg.Runes)
			if m.step == statePassword || m.step == stateConfirm ||
				m.step == stateEmail || m.step == stateEmailConfirm {
				m.input += r
			} else {
				m.input += strings.Map(func(c rune) rune {
					if unicode.IsSpace(c) {
						return -1
					}
					return c
				}, r)
			}
			m.errMsg = ""
			return m, nil
		}

	case tea.WindowSizeMsg:
		// Nothing to resize — inline rendering.
	}
	return m, nil
}

// submit validates the current input and advances to the next state,
// or finalises the registration.
func (m Model) submit() (tea.Model, tea.Cmd) {
	switch m.step {

	case stateUsername:
		if err := validateUsername(m.input); err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		// Check availability.
		if _, err := m.db.GetUserByUsername(m.input); err == nil {
			m.errMsg = "Username already taken. Please choose another."
			m.input = ""
			return m, nil
		}
		m.username = m.input
		m.input = ""
		if m.mode == "invite-only" {
			m.step = stateInviteCode
		} else {
			m.step = stateEmail
		}

	case stateEmail:
		// Email is optional but if provided we ask for it twice to catch typos.
		m.email = strings.TrimSpace(m.input)
		m.input = ""
		if m.email == "" {
			m.step = statePassword
		} else {
			m.step = stateEmailConfirm
		}

	case stateEmailConfirm:
		if strings.TrimSpace(m.input) != m.email {
			m.errMsg = "Email addresses do not match. Please re-enter."
			m.email = ""
			m.input = ""
			m.step = stateEmail
			return m, nil
		}
		m.input = ""
		m.step = statePassword

	case stateInviteCode:
		code := strings.TrimSpace(m.input)
		if code == "" {
			m.errMsg = "Please enter your invite code."
			return m, nil
		}
		// Validate only — do not consume a use yet. The actual decrement is
		// deferred to finalise(), once the account is confirmed created, so an
		// abandoned registration (e.g. disconnect during password entry) or a
		// failed account creation never burns an invite use.
		if err := m.db.ValidateInvite(code); err != nil {
			m.errMsg = "Invite code is invalid or has expired. Please try again."
			m.input = ""
			return m, nil
		}
		m.invite = code
		m.input = ""
		m.step = statePassword

	case statePassword:
		if err := validatePassword(m.input); err != nil {
			m.errMsg = err.Error()
			m.input = ""
			return m, nil
		}
		m.password = m.input
		m.input = ""
		m.step = stateConfirm

	case stateConfirm:
		if m.input != m.password {
			m.errMsg = "Passwords do not match. Please try again."
			m.input = ""
			m.step = statePassword
			return m, nil
		}
		return m.finalise()

	case stateDone:
		m.done = true
		return m, tea.Quit
	}

	return m, nil
}

// finalise creates the account and transitions to stateDone.
func (m Model) finalise() (tea.Model, tea.Cmd) {
	hash, err := hashPassword(m.password)
	if err != nil {
		m.result = fmt.Sprintf("Internal error: %v\nPlease try again later.", err)
		m.step = stateDone
		return m, nil
	}

	user, err := m.db.CreatePendingAccount(m.username, m.email, hash, m.pendingExpiry)
	if err != nil {
		if err == store.ErrUsernameTaken {
			m.errMsg = "Username was taken while you were registering. Please restart."
		} else {
			m.errMsg = fmt.Sprintf("Could not create account: %v", err)
		}
		m.step = stateUsername
		m.input = ""
		return m, nil
	}

	if m.mode == "invite-only" {
		// Consume the invite only now that the account row exists. The code
		// was validated (without decrementing) back at stateInviteCode; if the
		// user had abandoned registration, or CreatePendingAccount above had
		// failed, the invite would still be untouched. This re-checks and
		// decrements atomically, since the code could have expired or been
		// used up by another registration in the meantime.
		if err := m.db.ValidateAndConsumeInvite(m.invite); err != nil {
			// The invite went invalid between entry and now. Roll back the
			// pending account we just created so a now-unusable, never-
			// activated account doesn't linger and squat the username.
			_ = m.db.RejectPendingAccount(m.username)
			m.errMsg = "Invite code is no longer valid or has been used up. Please restart registration."
			m.step = stateUsername
			m.input = ""
			return m, nil
		}
		// Valid invite = immediate approval; no admin action needed.
		if err := m.db.ActivateAccount(m.username); err != nil {
			m.result = fmt.Sprintf("Account created but activation failed: %v\nContact the administrator.", err)
		} else {
			m.result = fmt.Sprintf(
				"Account '%s' created and activated!\n\n"+
					"You may now reconnect and log in with your new credentials.\n\n"+
					"Press Enter to disconnect.",
				m.username,
			)
		}
		m.step = stateDone
		return m, nil
	}

	// open-with-approval: account is pending.
	m.reg.NotifyAdmins(registry.PhoneEvent{
		Type:   registry.EventAdminNotify,
		Caller: m.username,
		CallID: fmt.Sprintf("reg:%d", user.ID),
	})

	emailNote := ""
	if m.email != "" {
		emailNote = fmt.Sprintf("\nYou provided %s as a contact address.", m.email)
	}
	m.result = fmt.Sprintf(
		"Registration request submitted for '%s'.%s\n\n"+
			"An administrator will review your request. Please check back later\n"+
			"or wait to be contacted by the system administrator.\n\n"+
			"Press Enter to disconnect.",
		m.username, emailNote,
	)
	m.step = stateDone
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  VAX-BBS Account Registration\n")
	b.WriteString("  " + strings.Repeat("─", 40) + "\n\n")

	switch m.step {
	case stateUsername:
		b.WriteString("  Desired username: " + m.input + "█\n")
		b.WriteString("  (3–20 characters, letters/numbers/underscore)\n")

	case stateEmail:
		b.WriteString("  Email address (optional — press Enter to skip):\n")
		b.WriteString("  " + m.input + "█\n")
		b.WriteString("  Used only to contact you about your request.\n")

	case stateEmailConfirm:
		b.WriteString("  Confirm email: " + m.input + "█\n")
		b.WriteString("  Re-enter to confirm: " + m.email + "\n")

	case stateInviteCode:
		b.WriteString("  Invite code: " + m.input + "█\n")

	case statePassword:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  Password: " + masked + "█\n")
		b.WriteString("  (minimum 8 characters)\n")

	case stateConfirm:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  Confirm password: " + masked + "█\n")

	case stateDone:
		b.WriteString("\n")
		for _, line := range strings.Split(m.result, "\n") {
			b.WriteString("  " + line + "\n")
		}
		return b.String()
	}

	if m.errMsg != "" {
		b.WriteString("\n  ! " + m.errMsg + "\n")
	}

	if m.step > stateUsername {
		b.WriteString("\n  Username: " + m.username + "\n")
	}

	return b.String()
}

// ---- Validation helpers -------------------------------------------------

var reservedUsernames = map[string]bool{
	"new": true, "admin": true, "root": true, "system": true,
	"sysop": true, "operator": true, "anonymous": true,
}

func validateUsername(s string) error {
	if len(s) < 3 {
		return fmt.Errorf("Username must be at least 3 characters.")
	}
	if len(s) > 20 {
		return fmt.Errorf("Username must be 20 characters or fewer.")
	}
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return fmt.Errorf("Only letters, numbers, and underscores are allowed.")
		}
	}
	if reservedUsernames[strings.ToLower(s)] {
		return fmt.Errorf("That username is reserved. Please choose another.")
	}
	return nil
}

func validatePassword(s string) error {
	if len(s) < 8 {
		return fmt.Errorf("Password must be at least 8 characters.")
	}
	if len(s) > 72 {
		return fmt.Errorf("Password must be 72 characters or fewer.")
	}
	return nil
}

// hashPassword wraps the auth package's hashing. We call it here to
// avoid importing auth from main, which would create an import cycle.
// The actual implementation is imported from internal/auth.
func hashPassword(password string) (string, error) {
	// Import via the auth package — see auth/hash.go.
	// Calling through a function var avoids a direct import cycle.
	return hashFn(password)
}

// hashFn is set by the server at startup via SetHashFn, allowing the
// registration package to use the same argon2id implementation without
// a direct import of auth (which imports nothing that would cycle, but
// this pattern keeps coupling explicit).
var hashFn func(string) (string, error)

// SetHashFn registers the password hashing function. Called once from
// main before any registration sessions can begin.
func SetHashFn(fn func(string) (string, error)) {
	hashFn = fn
}
