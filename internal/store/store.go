// Package store implements account persistence on SQLite, via
// modernc.org/sqlite — a CGo-free, pure-Go driver, chosen specifically
// to preserve the single-static-binary property the design doc picked
// Go for in the first place. mattn/go-sqlite3 was considered and
// rejected for this reason: it's CGo-based, which would require a C
// toolchain at build time and complicate cross-compilation.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver via side-effecting init()
)

// ErrNotFound is returned by lookups when no matching row exists. Callers
// (e.g. the auth handler) check for this specifically rather than
// treating every error the same way, since "user doesn't exist" and "the
// database is unreachable" should usually be handled differently —
// though per the design doc's no-enumeration rule, neither should ever
// produce a different *user-facing* message during login.
var ErrNotFound = errors.New("not found")

// User mirrors the design doc's schema sketch. Several fields
// (SSHPubkey, PlanText, ColorOptIn, AdminVisible) aren't read or written
// by anything yet — the columns exist so the schema doesn't need a
// migration later for FINGER or SET KEY. FailedAttempts and LockedUntil
// are now fully active (see RecordFailedAttempt / ClearFailedAttempts).
// Email and BannedUntil were added in the registration-modes milestone.
type User struct {
	ID             int64
	Username       string
	PasswordHash   sql.NullString
	SSHPubkey      sql.NullString
	Status         string // pending | active | suspended
	Role           string // user | admin
	PlanText       sql.NullString
	ColorOptIn     bool
	AdminVisible   bool
	FailedAttempts int
	LockedUntil    sql.NullTime
	CreatedAt      time.Time
	LastLoginAt    sql.NullTime
	Email          sql.NullString // optional; used for open-with-approval notifications
	BannedUntil    sql.NullTime   // nil = not banned or permanent ban
}

// Store wraps a database/sql connection pool to the SQLite file.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and
// ensures the schema exists. path is typically "data/<something>.db" —
// the same data/ directory the SSH host key already lives in, which is
// already gitignored.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite does not enforce FOREIGN KEY constraints by default; it's a
	// per-connection PRAGMA, not a database-level setting. Without this,
	// invites.created_by would silently accept invalid user IDs.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("running schema migration: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate creates the schema if it doesn't already exist, and runs
// additive column migrations so existing databases gain new columns
// without losing data.
func (s *Store) migrate() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT,
		ssh_pubkey TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		role TEXT NOT NULL DEFAULT 'user',
		plan_text TEXT,
		color_opt_in BOOLEAN NOT NULL DEFAULT 0,
		admin_visible BOOLEAN NOT NULL DEFAULT 0,
		failed_attempts INTEGER NOT NULL DEFAULT 0,
		locked_until DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_login_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS invites (
		code TEXT PRIMARY KEY,
		created_by INTEGER NOT NULL REFERENCES users(id),
		uses_remaining INTEGER NOT NULL,
		expires_at DATETIME NOT NULL
	);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Additive column migrations — safe to run on every startup.
	// SQLite errors with "duplicate column name" if the column already
	// exists; we ignore that specific error and fail on anything else.
	for _, ddl := range []string{
		`ALTER TABLE users ADD COLUMN email TEXT`,
		`ALTER TABLE users ADD COLUMN banned_until DATETIME`,
	} {
		if _, err := s.db.Exec(ddl); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migration %q: %w", ddl, err)
			}
		}
	}
	return nil
}

// NeverExpires returns the sentinel timestamp stored for invites with no
// expiry. Year 2099 is far enough in the future to be treated as
// "forever" by display code while remaining a valid DATETIME value in
// the NOT NULL schema column.
func NeverExpires() time.Time {
	return time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
}

// Invite mirrors the invites table.
type Invite struct {
	Code          string
	CreatedBy     int64
	UsesRemaining int
	ExpiresAt     time.Time
}

// IsExpired returns true if the invite has an expiry before now.
// Invites stored with NeverExpires() are never expired.
func (inv *Invite) IsExpired() bool {
	return inv.ExpiresAt.Year() < 2090 && time.Now().After(inv.ExpiresAt)
}

// DisplayExpiry returns a human-readable expiry string.
func (inv *Invite) DisplayExpiry() string {
	if inv.ExpiresAt.Year() >= 2090 {
		return "never"
	}
	return inv.ExpiresAt.Format("02-Jan-2006")
}

// CreateUser inserts a new user with the given username, pre-hashed
// password, and role ("user" or "admin"). Used by cmd/adduser to seed
// accounts in closed registration mode.
func (s *Store) CreateUser(username, passwordHash, role string) (*User, error) {
	res, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, status, role) VALUES (?, ?, 'active', ?)`,
		username, passwordHash, role,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("inserting user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading inserted user id: %w", err)
	}
	return s.GetUserByID(id)
}

// GetUserByUsername looks up a user by username, for the login path.
// Returns ErrNotFound if no such user exists.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, ssh_pubkey, status, role,
		        plan_text, color_opt_in, admin_visible, failed_attempts,
		        locked_until, created_at, last_login_at,
		        COALESCE(email, '') as email,
		        banned_until
		 FROM users WHERE username = ?`,
		username,
	)
	return scanUser(row)
}

// GetUserByID looks up a user by primary key.
func (s *Store) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, ssh_pubkey, status, role,
		        plan_text, color_opt_in, admin_visible, failed_attempts,
		        locked_until, created_at, last_login_at,
		        COALESCE(email, '') as email,
		        banned_until
		 FROM users WHERE id = ?`,
		id,
	)
	return scanUser(row)
}

// lockoutThreshold is the number of consecutive wrong passwords before
// an account is locked. Matches the design doc: "per-account lockout
// after 5 failed attempts."
const lockoutThreshold = 5

// lockoutDuration is how long an account stays locked before the timer
// expires naturally. Admin UNLOCK clears it early regardless. 15 minutes
// is a reasonable starting point — long enough to make brute-forcing
// very slow (5 attempts per 15 minutes = 480 attempts/day), short enough
// not to be punishing for a real user who simply forgot their password.
const lockoutDuration = 15 * time.Minute

// RecordFailedAttempt increments failed_attempts for the given user ID.
// If the new count reaches lockoutThreshold, locked_until is set to
// now + lockoutDuration in the same UPDATE, so the lock is always
// applied atomically with the counter increment — no window where the
// counter is at threshold but the lock hasn't been written yet.
func (s *Store) RecordFailedAttempt(userID int64) error {
	_, err := s.db.Exec(`
		UPDATE users
		SET
			failed_attempts = failed_attempts + 1,
			locked_until = CASE
				WHEN failed_attempts + 1 >= ? THEN datetime('now', ?)
				ELSE locked_until
			END
		WHERE id = ?`,
		lockoutThreshold,
		fmt.Sprintf("+%d seconds", int(lockoutDuration.Seconds())),
		userID,
	)
	if err != nil {
		return fmt.Errorf("recording failed attempt for user %d: %w", userID, err)
	}
	return nil
}

// ClearFailedAttempts resets failed_attempts to 0 and clears locked_until
// for the given user ID. Called on successful login (clean counter after
// a good password), and by the future admin UNLOCK command (early
// release from a lockout).
func (s *Store) ClearFailedAttempts(userID int64) error {
	_, err := s.db.Exec(
		`UPDATE users SET failed_attempts = 0, locked_until = NULL WHERE id = ?`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("clearing failed attempts for user %d: %w", userID, err)
	}
	return nil
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.SSHPubkey, &u.Status, &u.Role,
		&u.PlanText, &u.ColorOptIn, &u.AdminVisible, &u.FailedAttempts,
		&u.LockedUntil, &u.CreatedAt, &u.LastLoginAt,
		&u.Email, &u.BannedUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning user row: %w", err)
	}
	return &u, nil
}

// UpdateLastLogin sets last_login_at to the current UTC time for the
// given user ID. Called from the auth handler on every successful login.
func (s *Store) UpdateLastLogin(userID int64) error {
	_, err := s.db.Exec(
		`UPDATE users SET last_login_at = datetime('now') WHERE id = ?`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("updating last login for user %d: %w", userID, err)
	}
	return nil
}

// ---- Registration -------------------------------------------------------

// ErrUsernameTaken is returned by CreatePendingAccount when the username
// is already in use.
var ErrUsernameTaken = errors.New("username already taken")

// CreatePendingAccount inserts a new account with status='pending'.
// Before inserting, it deletes any expired pending account with the same
// username so squatted names are automatically freed after the expiry window.
// For invite-only, callers should follow up with ActivateAccount to set
// status='active' without requiring separate admin approval.
func (s *Store) CreatePendingAccount(username, email, passwordHash string, pendingExpiry time.Duration) (*User, error) {
	// Free the username if a prior pending registration has expired.
	if pendingExpiry > 0 {
		_, _ = s.db.Exec(
			`DELETE FROM users
			 WHERE username = ? AND status = 'pending'
			       AND created_at < datetime('now', ?)`,
			username,
			fmt.Sprintf("-%d seconds", int(pendingExpiry.Seconds())),
		)
	}
	_, err := s.db.Exec(
		`INSERT INTO users (username, email, password_hash, status, role)
		 VALUES (?, ?, ?, 'pending', 'user')`,
		username, nullableString(email), passwordHash,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("creating pending account: %w", err)
	}
	return s.GetUserByUsername(username)
}

// PurgeExpiredPendingAccounts deletes pending accounts older than maxAge.
// Called at startup and periodically to prevent username squatting. Pass
// 0 to skip (never auto-expire).
func (s *Store) PurgeExpiredPendingAccounts(maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(
		`DELETE FROM users
		 WHERE status = 'pending'
		       AND created_at < datetime('now', ?)`,
		fmt.Sprintf("-%d seconds", int(maxAge.Seconds())),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ActivateAccount sets an account's status to 'active' without requiring
// admin approval. Used for invite-only registrations where the valid
// invite code acts as the approval.
func (s *Store) ActivateAccount(username string) error {
	_, err := s.db.Exec(
		`UPDATE users SET status = 'active' WHERE username = ? AND status = 'pending'`,
		username,
	)
	return err
}

// ApprovePendingAccount sets a pending account to active.
func (s *Store) ApprovePendingAccount(username string) error {
	res, err := s.db.Exec(
		`UPDATE users SET status = 'active' WHERE username = ? AND status = 'pending'`,
		username,
	)
	if err != nil {
		return fmt.Errorf("approving account %q: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no pending account found for %q", username)
	}
	return nil
}

// RejectPendingAccount deletes a pending account.
func (s *Store) RejectPendingAccount(username string) error {
	res, err := s.db.Exec(
		`DELETE FROM users WHERE username = ? AND status = 'pending'`,
		username,
	)
	if err != nil {
		return fmt.Errorf("rejecting account %q: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no pending account found for %q", username)
	}
	return nil
}

// ListPendingAccounts returns all accounts with status='pending',
// ordered by creation time.
func (s *Store) ListPendingAccounts() ([]User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, password_hash, ssh_pubkey, status, role,
		        plan_text, color_opt_in, admin_visible, failed_attempts,
		        locked_until, created_at, last_login_at,
		        COALESCE(email, '') as email,
		        banned_until
		 FROM users WHERE status = 'pending' ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing pending accounts: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.Username, &u.PasswordHash, &u.SSHPubkey, &u.Status, &u.Role,
			&u.PlanText, &u.ColorOptIn, &u.AdminVisible, &u.FailedAttempts,
			&u.LockedUntil, &u.CreatedAt, &u.LastLoginAt,
			&u.Email, &u.BannedUntil,
		); err != nil {
			return nil, fmt.Errorf("scanning pending account: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountPendingAccounts returns the number of accounts awaiting approval.
func (s *Store) CountPendingAccounts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE status = 'pending'`).Scan(&n)
	return n, err
}

// ---- Ban / suspend -------------------------------------------------------

// BanUser sets a user's status to 'suspended' and records when the ban
// lifts. Pass nil for a permanent ban. Permanent bans record a far-future
// sentinel (NeverExpires) so banned_until remains NOT NULL-compatible if
// needed.
func (s *Store) BanUser(username string, until *time.Time) error {
	var banUntil interface{}
	if until == nil {
		ne := NeverExpires()
		banUntil = ne.Format("2006-01-02 15:04:05")
	} else {
		banUntil = until.Format("2006-01-02 15:04:05")
	}
	res, err := s.db.Exec(
		`UPDATE users SET status = 'suspended', banned_until = ? WHERE username = ?`,
		banUntil, username,
	)
	if err != nil {
		return fmt.Errorf("banning %q: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnbanUser lifts a ban, restoring status to 'active' and clearing
// banned_until.
func (s *Store) UnbanUser(username string) error {
	res, err := s.db.Exec(
		`UPDATE users SET status = 'active', banned_until = NULL
		 WHERE username = ? AND status = 'suspended'`,
		username,
	)
	if err != nil {
		return fmt.Errorf("unbanning %q: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user %q is not suspended", username)
	}
	return nil
}

// CheckAndLiftExpiredBan checks whether a suspended user's timed ban has
// expired and, if so, restores their status to 'active'. Returns true if
// the ban was lifted. Called from the auth handler so expired bans
// self-heal on the user's next login attempt.
func (s *Store) CheckAndLiftExpiredBan(userID int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE users SET status = 'active', banned_until = NULL
		 WHERE id = ? AND status = 'suspended'
		       AND banned_until IS NOT NULL
		       AND banned_until < datetime('now')
		       AND CAST(strftime('%Y', banned_until) AS INTEGER) < 2090`,
		userID,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- Invite codes -------------------------------------------------------

// CreateInvite inserts a new invite code. expiresAt should be
// NeverExpires() for codes with no time limit.
func (s *Store) CreateInvite(code string, createdBy int64, uses int, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO invites (code, created_by, uses_remaining, expires_at)
		 VALUES (?, ?, ?, ?)`,
		code, createdBy, uses, expiresAt.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("creating invite: %w", err)
	}
	return nil
}

// ErrInviteInvalid is returned when an invite code doesn't exist, has no
// uses remaining, or has expired.
var ErrInviteInvalid = errors.New("invite code invalid or expired")

// ValidateAndConsumeInvite atomically checks that the code is valid and
// decrements uses_remaining. Returns ErrInviteInvalid if the code cannot
// be used.
func (s *Store) ValidateAndConsumeInvite(code string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var uses int
	var expiresAt string
	err = tx.QueryRow(
		`SELECT uses_remaining, expires_at FROM invites WHERE code = ?`, code,
	).Scan(&uses, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInviteInvalid
	}
	if err != nil {
		return err
	}
	if uses <= 0 {
		return ErrInviteInvalid
	}
	// Parse and check expiry; year >= 2090 = never expires.
	t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
	if err != nil {
		// Try alternate format SQLite may use.
		t, err = time.Parse("2006-01-02T15:04:05Z", expiresAt)
	}
	if err == nil && t.Year() < 2090 && time.Now().After(t) {
		return ErrInviteInvalid
	}

	if _, err := tx.Exec(
		`UPDATE invites SET uses_remaining = uses_remaining - 1 WHERE code = ?`, code,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ListInvites returns all invite codes, for admin display.
func (s *Store) ListInvites() ([]Invite, error) {
	rows, err := s.db.Query(
		`SELECT code, created_by, uses_remaining, expires_at FROM invites ORDER BY rowid`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invites []Invite
	for rows.Next() {
		var inv Invite
		var expiresStr string
		if err := rows.Scan(&inv.Code, &inv.CreatedBy, &inv.UsesRemaining, &expiresStr); err != nil {
			return nil, err
		}
		t, _ := time.Parse("2006-01-02 15:04:05", expiresStr)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02T15:04:05Z", expiresStr)
		}
		inv.ExpiresAt = t
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// DeleteUser permanently removes an account by username.
// Returns ErrNotFound if no such user exists.
// Distinct from BAN (which suspends) — this is a hard delete.
func (s *Store) DeleteUser(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("deleting user %q: %w", username, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAllUsers returns every account, ordered by creation date.
// Includes pending, active, and suspended accounts.
func (s *Store) ListAllUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, COALESCE(email,''), status, role,
		        COALESCE(admin_visible,0), COALESCE(failed_attempts,0),
		        locked_until, last_login_at, banned_until
		 FROM users
		 ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u := &User{}
		var email string
		if err := rows.Scan(
			&u.ID, &u.Username, &email, &u.Status, &u.Role,
			&u.AdminVisible, &u.FailedAttempts,
			&u.LockedUntil, &u.LastLoginAt, &u.BannedUntil,
		); err != nil {
			return nil, err
		}
		if email != "" {
			u.Email = sql.NullString{String: email, Valid: true}
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// nullableString returns nil for empty strings, or the string value.
// Used when inserting optional TEXT columns so they're stored as NULL
// rather than empty string.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
