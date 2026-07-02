package store

import "fmt"

// SetPassword overwrites username's password hash and clears any pending
// forced-change requirement — a new password satisfies EXPIRE PASSWORD's
// flag regardless of who set it (the user themselves, or an admin via
// RESET PASSWORD). Returns ErrNotFound if no such user exists.
func (s *Store) SetPassword(username, passwordHash string) error {
	res, err := s.db.Exec(
		`UPDATE users SET password_hash = ?, must_change_password = 0 WHERE username = ?`,
		passwordHash, username,
	)
	if err != nil {
		return fmt.Errorf("setting password for %q: %w", username, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ExpirePassword flags username's account so the next successful login is
// followed by a mandatory password change before the lobby loads. The
// current password keeps working for that one login. Returns ErrNotFound
// if no such user exists.
func (s *Store) ExpirePassword(username string) error {
	res, err := s.db.Exec(
		`UPDATE users SET must_change_password = 1 WHERE username = ?`,
		username,
	)
	if err != nil {
		return fmt.Errorf("expiring password for %q: %w", username, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
