package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// MaxPlanLength is the hard character limit for plan text (counted in runes,
// not bytes). 512 is generous for a .plan-file blurb while keeping FINGER
// output readable and preventing storage abuse.
const MaxPlanLength = 512

// ErrPlanTooLong is returned when submitted plan text exceeds MaxPlanLength.
var ErrPlanTooLong = fmt.Errorf("plan text exceeds %d character limit", MaxPlanLength)

// SetPlan stores sanitized plan text for username.
//
// Sanitization (belt):
//   - ANSI/VT100 escape sequences stripped (terminal injection protection)
//   - C0 control chars stripped, except \n and \t (legitimate formatting)
//   - Leading/trailing whitespace trimmed
//
// FINGER also strips at display time (suspenders), so any data that predates
// this sanitization is safe to render.
//
// Returns ErrPlanTooLong if the sanitized text exceeds MaxPlanLength runes.
func (s *Store) SetPlan(username, text string) error {
	clean := StripANSI(text)
	clean = strings.TrimSpace(clean)

	if len([]rune(clean)) > MaxPlanLength {
		return ErrPlanTooLong
	}

	_, err := s.db.Exec(
		`UPDATE users SET plan_text = ? WHERE username = ?`,
		clean, username,
	)
	return err
}

// ClearPlan sets plan_text to NULL for username,
// restoring the "(no plan set)" display in FINGER.
func (s *Store) ClearPlan(username string) error {
	_, err := s.db.Exec(
		`UPDATE users SET plan_text = NULL WHERE username = ?`,
		username,
	)
	return err
}

// GetPlan returns the plan text for username.
// Returns ("", nil) if no plan is set — not an error.
func (s *Store) GetPlan(username string) (string, error) {
	var text sql.NullString
	err := s.db.QueryRow(
		`SELECT plan_text FROM users WHERE username = ?`,
		username,
	).Scan(&text)
	if err != nil {
		return "", err
	}
	if !text.Valid {
		return "", nil
	}
	return text.String, nil
}

// StripANSI removes ANSI/VT100 escape sequences and unsafe C0 control
// characters from s. Exported so FINGER display can call it at render time.
//
// Handles:
//   - CSI sequences: ESC [ ... <final byte 0x40–0x7E>
//   - OSC sequences: ESC ] ... ST (ESC\ or BEL)
//   - Any other ESC + single char
//   - Bare C0 controls (0x00–0x1F) except \n (0x0A) and \t (0x09)
//   - DEL (0x7F) and non-printable Unicode (preserving \n and \t)
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]

		// ESC (0x1B) starts an escape sequence — consume and skip it entirely.
		if r == '\x1b' {
			i++
			if i >= len(runes) {
				break
			}
			switch runes[i] {
			case '[': // CSI: ESC [ ... <final 0x40–0x7E>
				i++
				for i < len(runes) {
					c := runes[i]
					i++
					if c >= 0x40 && c <= 0x7E {
						break // final byte ends the sequence
					}
				}
			case ']': // OSC: ESC ] ... BEL or ESC\
				i++
				for i < len(runes) {
					c := runes[i]
					i++
					if c == '\x07' {
						break // BEL terminator
					}
					if c == '\x1b' && i < len(runes) && runes[i] == '\\' {
						i++ // ST = ESC\
						break
					}
				}
			default: // Any other ESC + single char: skip both.
				i++
			}
			continue
		}

		// Strip C0 controls except newline and tab (legitimate formatting).
		if r < 0x20 && r != '\n' && r != '\t' {
			i++
			continue
		}

		// Strip DEL and non-printable Unicode.
		if r == '\x7f' || (!unicode.IsPrint(r) && r != '\n' && r != '\t') {
			i++
			continue
		}

		b.WriteRune(r)
		i++
	}
	return b.String()
}
