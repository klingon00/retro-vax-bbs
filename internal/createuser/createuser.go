// Package createuser provides the CREATE USER masked password-entry flow.
// It lets an admin provision an account directly from the lobby — the
// entire mechanism cmd/adduser existed to cover in closed mode before this
// existed — without a plaintext password ever touching the command line
// or the lobby's scrollback history.
//
// The username and role are validated by the lobby command handler before
// this app is launched (internal/lobby/commands.go); this model only
// handles the masked password + confirmation and the final CreateUser call.
package createuser

import (
	"errors"
	"fmt"
	"log"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/auth"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

type state int

const (
	statePassword state = iota
	stateConfirm
)

// Model is the Bubble Tea model for the CREATE USER password prompt.
// It runs inline (no alt screen), same as SET PLAN.
type Model struct {
	St       *store.Store
	Actor    string // admin username running this command, for the audit log
	Username string
	Role     string

	step     state
	input    string
	password string

	ValidationErr string
	StatusMsg     string
	IsDone        bool
}

// New creates a CREATE USER Model for the given, already-validated
// username and role ("user" or "admin"). actor is the admin's own
// username, recorded in the audit log line once the flow finishes —
// this app is where the account creation actually happens (the lobby's
// requireAdminLogged only logs that CREATE USER was invoked, since the
// admin might still cancel the password prompt).
func New(st *store.Store, actor, username, role string) Model {
	return Model{St: st, Actor: actor, Username: username, Role: role}
}

// Init returns the initial Cmd — nothing to do until the user types.
func (m Model) Init() tea.Cmd { return nil }

// Update processes a message and returns the updated Model and any Cmd.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.IsDone {
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		log.Printf("admin action: %s CREATE USER %s (role=%s) cancelled, no account created", m.Actor, m.Username, m.Role)
		m.StatusMsg = fmt.Sprintf("%%VAX-BBS-I-CREATE, CREATE USER %s cancelled. No account was created.", m.Username)
		m.IsDone = true
		return m, nil

	case tea.KeyEnter:
		return m.submit()

	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
			m.ValidationErr = ""
		}
		return m, nil

	case tea.KeySpace:
		m.input += " "
		m.ValidationErr = ""
		return m, nil

	case tea.KeyRunes:
		m.input += string(keyMsg.Runes)
		m.ValidationErr = ""
		return m, nil
	}

	return m, nil
}

// submit validates the current input and advances to the next step, or
// finalises account creation.
func (m Model) submit() (Model, tea.Cmd) {
	switch m.step {
	case statePassword:
		if err := validatePassword(m.input); err != nil {
			m.ValidationErr = err.Error()
			m.input = ""
			return m, nil
		}
		m.password = m.input
		m.input = ""
		m.step = stateConfirm
		return m, nil

	case stateConfirm:
		if m.input != m.password {
			m.ValidationErr = "Passwords do not match. Please try again."
			m.input = ""
			m.password = ""
			m.step = statePassword
			return m, nil
		}
		return m.finalise()
	}
	return m, nil
}

// finalise hashes the password and creates the account. This is the point
// where the account actually comes into existence (or doesn't) — the
// audit log line here is the authoritative one for CREATE USER, since the
// lobby's dispatch-time log only records that the command was invoked,
// not whether the admin went on to complete or cancel the password prompt.
func (m Model) finalise() (Model, tea.Cmd) {
	hash, err := auth.HashPassword(m.password)
	if err != nil {
		log.Printf("admin action: %s CREATE USER %s (role=%s) failed: hashing password: %v", m.Actor, m.Username, m.Role, err)
		m.StatusMsg = fmt.Sprintf("%%VAX-BBS-E-CREATE, could not hash password: %v", err)
		m.IsDone = true
		return m, nil
	}

	user, err := m.St.CreateUser(m.Username, hash, m.Role)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			log.Printf("admin action: %s CREATE USER %s (role=%s) failed: username taken", m.Actor, m.Username, m.Role)
			m.StatusMsg = fmt.Sprintf("%%VAX-BBS-E-CREATE, username %q was taken while you were typing.", m.Username)
		} else {
			log.Printf("admin action: %s CREATE USER %s (role=%s) failed: %v", m.Actor, m.Username, m.Role, err)
			m.StatusMsg = fmt.Sprintf("%%VAX-BBS-E-CREATE, %v", err)
		}
		m.IsDone = true
		return m, nil
	}

	log.Printf("admin action: %s CREATE USER %s (role=%s) created", m.Actor, user.Username, user.Role)
	m.StatusMsg = fmt.Sprintf("%%VAX-BBS-S-CREATED, Account '%s' created (role=%s). They may now log in.",
		user.Username, user.Role)
	m.IsDone = true
	return m, nil
}

// View renders the prompt. Line 1 is a sacrifice blank — see the BubbleTea
// v1.3.x off-by-one rendering note in open-questions.md (same fix as
// SET PLAN's inline editor).
func (m Model) View() string {
	var b strings.Builder
	b.WriteString("\n")

	fmt.Fprintf(&b, "CREATE USER %s (role: %s)\n\n", m.Username, m.Role)

	switch m.step {
	case statePassword:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  Password: " + masked + "█\n")
		b.WriteString("  (8-72 characters)\n")
	case stateConfirm:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  Confirm password: " + masked + "█\n")
	}

	if m.ValidationErr != "" {
		b.WriteString("\n  ! " + m.ValidationErr + "\n")
	}

	b.WriteString("\n  Esc / Ctrl+C  cancel\n")
	return b.String()
}

// Done reports whether the flow has finished (created or cancelled).
func (m Model) Done() bool { return m.IsDone }

func validatePassword(s string) error {
	if len(s) < 8 {
		return fmt.Errorf("Password must be at least 8 characters.")
	}
	if len(s) > 72 {
		return fmt.Errorf("Password must be 72 characters or fewer.")
	}
	return nil
}
