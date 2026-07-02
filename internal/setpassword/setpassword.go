// Package setpassword provides the password-change flow shared by three
// entry points: self-service SET PASSWORD, admin-initiated RESET PASSWORD,
// and the mandatory post-EXPIRE PASSWORD change forced at next login.
//
// The masked hand-rolled input (no bubbles textinput widget, `*` rendering
// only, real characters never leave m.input) mirrors internal/createuser —
// password characters must never be echoed or land in the lobby's
// scrollback history.
package setpassword

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/klingon00/retro-vax-bbs/internal/auth"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

type state int

const (
	stateCurrent state = iota // verify existing password (self-service only)
	stateNew
	stateConfirm
)

// Model is the Bubble Tea model for the password-change prompt. It runs
// inline (no alt screen), same as SET PLAN / CREATE USER.
type Model struct {
	St     *store.Store
	Actor  string // who is running this flow, for the audit/log line
	Target string // whose password is being changed; equals Actor for self-service

	// RequireCurrent gates the stateCurrent step: true for self-service
	// SET PASSWORD (protects an unattended session from being used to
	// lock out the real account owner), false for admin RESET PASSWORD
	// (the admin doesn't know the target's current password) and for the
	// forced flow (the user just authenticated with it moments ago).
	RequireCurrent bool

	// Cancelable gates whether Esc/Ctrl+C abandons the flow. True for
	// both lobby-launched cases (self-service and admin). False for the
	// forced flow, which cannot be skipped — Esc is simply ignored there;
	// only Ctrl+C/Ctrl+D disconnects the session outright (handled one
	// level up, by ForcedModel), leaving must_change_password set.
	Cancelable bool

	step        state
	input       string
	newPassword string

	ValidationErr string
	StatusMsg     string
	IsDone        bool
}

// New returns a fresh password-change Model.
func New(st *store.Store, actor, target string, requireCurrent, cancelable bool) Model {
	step := stateNew
	if requireCurrent {
		step = stateCurrent
	}
	return Model{
		St:             st,
		Actor:          actor,
		Target:         target,
		RequireCurrent: requireCurrent,
		Cancelable:     cancelable,
		step:           step,
	}
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
		if !m.Cancelable {
			return m, nil
		}
		if m.Actor == m.Target {
			m.StatusMsg = "%VAX-BBS-I-PASSWD, SET PASSWORD cancelled. Your password was not changed."
		} else {
			m.StatusMsg = fmt.Sprintf("%%VAX-BBS-I-PASSWD, RESET PASSWORD %s cancelled. Password was not changed.", m.Target)
		}
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
// finalises the password change.
func (m Model) submit() (Model, tea.Cmd) {
	switch m.step {
	case stateCurrent:
		user, err := m.St.GetUserByUsername(m.Target)
		if err != nil || !user.PasswordHash.Valid {
			m.ValidationErr = "Could not verify current password. Please try again."
			m.input = ""
			return m, nil
		}
		// Same discipline as login: reject before argon2id runs once
		// locked, and never call RecordFailedAttempt while already
		// locked (that would keep pushing locked_until forward).
		if user.LockedUntil.Valid && user.LockedUntil.Time.After(time.Now()) {
			m.StatusMsg = "%VAX-BBS-E-LOCKED, Account is locked from too many failed attempts. Try again later or contact an admin."
			m.IsDone = true
			return m, nil
		}
		ok, err := auth.VerifyPassword(m.input, user.PasswordHash.String)
		if err != nil || !ok {
			_ = m.St.RecordFailedAttempt(user.ID)
			m.ValidationErr = "Current password incorrect."
			m.input = ""
			return m, nil
		}
		_ = m.St.ClearFailedAttempts(user.ID)
		m.input = ""
		m.step = stateNew
		return m, nil

	case stateNew:
		if err := validatePassword(m.input); err != nil {
			m.ValidationErr = err.Error()
			m.input = ""
			return m, nil
		}
		m.newPassword = m.input
		m.input = ""
		m.step = stateConfirm
		return m, nil

	case stateConfirm:
		if m.input != m.newPassword {
			m.ValidationErr = "Passwords do not match. Please try again."
			m.input = ""
			m.newPassword = ""
			m.step = stateNew
			return m, nil
		}
		return m.finalise()
	}
	return m, nil
}

// finalise hashes and stores the new password. This is the point where the
// change actually takes effect — for the admin-initiated case, this is the
// authoritative audit log line, since the lobby's dispatch-time log only
// records that RESET PASSWORD was invoked, not whether the admin went on
// to complete or cancel the prompt (same two-phase pattern as CREATE USER).
func (m Model) finalise() (Model, tea.Cmd) {
	hash, err := auth.HashPassword(m.newPassword)
	if err != nil {
		m.StatusMsg = fmt.Sprintf("%%VAX-BBS-E-PASSWD, could not hash password: %v", err)
		m.IsDone = true
		return m, nil
	}

	if err := m.St.SetPassword(m.Target, hash); err != nil {
		m.StatusMsg = fmt.Sprintf("%%VAX-BBS-E-PASSWD, %v", err)
		m.IsDone = true
		return m, nil
	}

	if m.Actor == m.Target {
		log.Printf("password changed: %s (self-service)", m.Target)
		m.StatusMsg = "%VAX-BBS-S-PASSWD, Your password has been updated."
	} else {
		log.Printf("admin action: %s RESET PASSWORD %s (password updated)", m.Actor, m.Target)
		m.StatusMsg = fmt.Sprintf("%%VAX-BBS-S-PASSWD, Password for '%s' has been updated.", m.Target)
	}
	m.IsDone = true
	return m, nil
}

// View renders the prompt. Line 1 is a sacrifice blank — see the BubbleTea
// v1.3.x off-by-one rendering note in open-questions.md (same fix as
// SET PLAN's inline editor and CREATE USER's password prompt).
func (m Model) View() string {
	var b strings.Builder
	b.WriteString("\n")

	if m.Actor == m.Target {
		b.WriteString("SET PASSWORD\n\n")
	} else {
		fmt.Fprintf(&b, "RESET PASSWORD %s\n\n", m.Target)
	}

	switch m.step {
	case stateCurrent:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  Current password: " + masked + "█\n")
	case stateNew:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  New password: " + masked + "█\n")
		b.WriteString("  (8-72 characters)\n")
	case stateConfirm:
		masked := strings.Repeat("*", len(m.input))
		b.WriteString("  Confirm new password: " + masked + "█\n")
	}

	if m.ValidationErr != "" {
		b.WriteString("\n  ! " + m.ValidationErr + "\n")
	}

	if m.Cancelable {
		b.WriteString("\n  Esc / Ctrl+C  cancel\n")
	}
	return b.String()
}

// Done reports whether the flow has finished (changed or cancelled).
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
