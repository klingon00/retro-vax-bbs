package store

import (
	"testing"
	"time"
)

// openTestStore opens an in-memory SQLite database for testing —
// discarded when the test process exits, no cleanup needed.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetUser(t *testing.T) {
	s := openTestStore(t)

	u, err := s.CreateUser("testuser", "$argon2id$v=19$fake-hash-for-test", "user")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "testuser" {
		t.Errorf("got username %q, want %q", u.Username, "testuser")
	}
	if u.Status != "active" {
		t.Errorf("got status %q, want %q", u.Status, "active")
	}
	if u.FailedAttempts != 0 {
		t.Errorf("new user has %d failed attempts, want 0", u.FailedAttempts)
	}
	if u.LockedUntil.Valid {
		t.Error("new user should not have a locked_until set")
	}

	got, err := s.GetUserByUsername("testuser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("got id %d, want %d", got.ID, u.ID)
	}
}

func TestCountUsers(t *testing.T) {
	s := openTestStore(t)

	n, err := s.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d users on a fresh store, want 0", n)
	}

	if _, err := s.CreateUser("first", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	n, err = s.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d users after one CreateUser, want 1", n)
	}

	if _, err := s.CreateUser("second", "hash", "user"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	n, err = s.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d users after two CreateUser calls, want 2", n)
	}
}

func TestGetUserByUsername_NotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetUserByUsername("nobody")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRecordFailedAttempt_IncrementsCounter(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("testuser", "hash", "user")

	if err := s.RecordFailedAttempt(u.ID); err != nil {
		t.Fatalf("RecordFailedAttempt: %v", err)
	}

	got, _ := s.GetUserByID(u.ID)
	if got.FailedAttempts != 1 {
		t.Errorf("got %d failed attempts, want 1", got.FailedAttempts)
	}
	if got.LockedUntil.Valid {
		t.Error("should not be locked after only 1 failed attempt")
	}
}

func TestRecordFailedAttempt_LocksAtThreshold(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("testuser", "hash", "user")

	for i := 0; i < lockoutThreshold; i++ {
		if err := s.RecordFailedAttempt(u.ID); err != nil {
			t.Fatalf("RecordFailedAttempt attempt %d: %v", i+1, err)
		}
	}

	got, _ := s.GetUserByID(u.ID)
	if got.FailedAttempts != lockoutThreshold {
		t.Errorf("got %d failed attempts, want %d", got.FailedAttempts, lockoutThreshold)
	}
	if !got.LockedUntil.Valid {
		t.Fatal("account should be locked after reaching threshold, locked_until is NULL")
	}
	if !got.LockedUntil.Time.After(time.Now()) {
		t.Errorf("locked_until %v is not in the future", got.LockedUntil.Time)
	}
}

func TestClearFailedAttempts_ResetsCounter(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("testuser", "hash", "user")

	// Drive it up to threshold so locked_until gets set too.
	for i := 0; i < lockoutThreshold; i++ {
		_ = s.RecordFailedAttempt(u.ID)
	}
	got, _ := s.GetUserByID(u.ID)
	if !got.LockedUntil.Valid {
		t.Fatal("pre-condition: account should be locked before clear")
	}

	if err := s.ClearFailedAttempts(u.ID); err != nil {
		t.Fatalf("ClearFailedAttempts: %v", err)
	}

	got, _ = s.GetUserByID(u.ID)
	if got.FailedAttempts != 0 {
		t.Errorf("got %d failed attempts after clear, want 0", got.FailedAttempts)
	}
	if got.LockedUntil.Valid {
		t.Error("locked_until should be NULL after clear")
	}
}

func TestRecordFailedAttempt_DoesNotExtendLockBeyondThreshold(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("testuser", "hash", "user")

	// Reach threshold.
	for i := 0; i < lockoutThreshold; i++ {
		_ = s.RecordFailedAttempt(u.ID)
	}
	atThreshold, _ := s.GetUserByID(u.ID)
	firstLockTime := atThreshold.LockedUntil.Time

	// One more attempt past threshold — locked_until should not move.
	_ = s.RecordFailedAttempt(u.ID)
	afterExtra, _ := s.GetUserByID(u.ID)

	// Allow a tiny clock skew (1 second) but it shouldn't be jumping
	// by lockoutDuration again.
	diff := afterExtra.LockedUntil.Time.Sub(firstLockTime)
	if diff > time.Second {
		t.Errorf("locked_until moved by %v after an extra attempt past threshold — should stay fixed", diff)
	}
}
