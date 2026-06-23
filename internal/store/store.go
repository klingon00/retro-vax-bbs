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

// migrate creates the schema if it doesn't already exist. CREATE TABLE
// IF NOT EXISTS is intentionally the entire migration strategy for now —
// a real migration framework (golang-migrate or similar, with versioned
// up/down steps) is more machinery than a single-developer hobby project
// at this stage needs. Worth revisiting if the schema needs to change
// shape under existing data later; for now, schema changes mean editing
// this SQL directly during development, before there's any real data to
// preserve.
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
	_, err := s.db.Exec(schema)
	return err
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
		        locked_until, created_at, last_login_at
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
		        locked_until, created_at, last_login_at
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
