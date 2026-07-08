package store

import (
	"database/sql"
	"errors"
	"sync"
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

// forceBan writes a suspended/banned state directly, bypassing BanUser's
// last-usable-admin guard. Read-side tests (e.g. CountUsableAdmins) need to
// construct "unusable admin" states — including the zero-usable-admins state
// that the guard now makes unreachable through the normal ban/delete path —
// without the write itself being refused. White-box: same package.
func forceBan(t *testing.T, s *Store, username string, until time.Time) {
	t.Helper()
	if _, err := s.db.Exec(
		`UPDATE users SET status = 'suspended', banned_until = ? WHERE username = ?`,
		until.UTC().Format("2006-01-02 15:04:05"), username,
	); err != nil {
		t.Fatalf("forceBan %q: %v", username, err)
	}
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

func TestCheckAndLiftExpiredBan_FutureBanStaysBanned(t *testing.T) {
	s := openTestStore(t)
	u, err := s.CreateUser("bannedfuture", "hash", "user")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	future := time.Now().Add(10 * time.Minute)
	if err := s.BanUser("bannedfuture", &future); err != nil {
		t.Fatalf("BanUser: %v", err)
	}

	lifted, err := s.CheckAndLiftExpiredBan(u.ID)
	if err != nil {
		t.Fatalf("CheckAndLiftExpiredBan: %v", err)
	}
	if lifted {
		t.Error("ban with a 10-minute future expiry was lifted immediately — should still be banned")
	}

	got, err := s.GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("got status %q immediately after banning 10 minutes out, want %q", got.Status, "suspended")
	}
}

func TestCheckAndLiftExpiredBan_PastBanIsLifted(t *testing.T) {
	s := openTestStore(t)
	u, err := s.CreateUser("bannedpast", "hash", "user")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	past := time.Now().Add(-10 * time.Minute)
	if err := s.BanUser("bannedpast", &past); err != nil {
		t.Fatalf("BanUser: %v", err)
	}

	lifted, err := s.CheckAndLiftExpiredBan(u.ID)
	if err != nil {
		t.Fatalf("CheckAndLiftExpiredBan: %v", err)
	}
	if !lifted {
		t.Error("ban with a 10-minute-past expiry was not lifted — should self-heal")
	}

	got, err := s.GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("got status %q after a lapsed ban, want %q", got.Status, "active")
	}
}

func TestValidateAndConsumeInvite_FutureExpiryStillValid(t *testing.T) {
	s := openTestStore(t)
	creator, err := s.CreateUser("invitecreator1", "hash", "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	future := time.Now().Add(10 * time.Minute)
	if err := s.CreateInvite("future-code-42", creator.ID, 1, future); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := s.ValidateAndConsumeInvite("future-code-42"); err != nil {
		t.Errorf("invite with a 10-minute future expiry was rejected immediately after creation: %v", err)
	}
}

func TestValidateAndConsumeInvite_PastExpiryIsInvalid(t *testing.T) {
	s := openTestStore(t)
	creator, err := s.CreateUser("invitecreator2", "hash", "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	past := time.Now().Add(-10 * time.Minute)
	if err := s.CreateInvite("past-code-42", creator.ID, 1, past); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := s.ValidateAndConsumeInvite("past-code-42"); err != ErrInviteInvalid {
		t.Errorf("invite with a 10-minute-past expiry: got err %v, want %v", err, ErrInviteInvalid)
	}
}

// TestValidateInvite_DoesNotConsume guards the registration fix: validating a
// code must not decrement its use count — only ValidateAndConsumeInvite does,
// deferred to confirmed account creation. A single-use code must therefore
// survive repeated validation and still be consumable exactly once.
func TestValidateInvite_DoesNotConsume(t *testing.T) {
	s := openTestStore(t)
	creator, err := s.CreateUser("invitecreator3", "hash", "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateInvite("single-use-42", creator.ID, 1, NeverExpires()); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	// Validate several times: a 1-use code must stay valid, never decremented.
	for i := 0; i < 3; i++ {
		if err := s.ValidateInvite("single-use-42"); err != nil {
			t.Fatalf("ValidateInvite call %d rejected a still-unused code: %v", i+1, err)
		}
	}

	// The single use is still available: consume succeeds once...
	if err := s.ValidateAndConsumeInvite("single-use-42"); err != nil {
		t.Errorf("ValidateAndConsumeInvite after validate-only: got %v, want nil", err)
	}
	// ...and is now exhausted for both validate and consume.
	if err := s.ValidateInvite("single-use-42"); err != ErrInviteInvalid {
		t.Errorf("ValidateInvite on exhausted code: got %v, want %v", err, ErrInviteInvalid)
	}
	if err := s.ValidateAndConsumeInvite("single-use-42"); err != ErrInviteInvalid {
		t.Errorf("ValidateAndConsumeInvite on exhausted code: got %v, want %v", err, ErrInviteInvalid)
	}
}

// TestValidateInvite_UnknownCode confirms a nonexistent code is rejected the
// same way by the validate-only path as by the consuming path.
func TestValidateInvite_UnknownCode(t *testing.T) {
	s := openTestStore(t)
	if err := s.ValidateInvite("no-such-code-99"); err != ErrInviteInvalid {
		t.Errorf("ValidateInvite on unknown code: got %v, want %v", err, ErrInviteInvalid)
	}
}

// ---- Invite expiry fail-closed (audit 2026-07-05 finding #6) -------------

// TestInviteExpired pins the fail-closed logic of the inviteExpired helper: a
// stored expires_at that parses under neither known layout must read as EXPIRED
// (true), not never-expiring. The empty-string and garbage cases are the #6
// regression (they returned false — "valid" — under the old fail-open code); the
// past/future/sentinel/alt-layout cases guard the surrounding behavior so the fix
// can't silently break valid-invite handling.
func TestInviteExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")
	sentinel := NeverExpires().Format("2006-01-02 15:04:05")
	altPast := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")

	cases := []struct {
		name      string
		expiresAt string
		want      bool // true = expired (reject)
	}{
		{"past expiry", past, true},
		{"future expiry", future, false},
		{"never-expires sentinel", sentinel, false},
		{"alternate ISO layout, past", altPast, true},
		{"empty string fails closed", "", true},
		{"garbage fails closed", "not-a-timestamp", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inviteExpired(c.expiresAt); got != c.want {
				t.Errorf("inviteExpired(%q) = %v, want %v", c.expiresAt, got, c.want)
			}
		})
	}
}

// TestValidateAndConsumeInvite_CorruptedExpiryFailsClosed proves the fail-closed
// behavior end-to-end through the public API: a valid invite whose stored
// expires_at is later corrupted (simulating a hand-edited / migrated row that
// parses under neither layout) must be rejected by BOTH the validate-only and the
// consuming path, not treated as never-expiring and consumed. White-box: the raw
// UPDATE bypasses CreateInvite's well-formed .UTC().Format write, same as forceBan.
func TestValidateAndConsumeInvite_CorruptedExpiryFailsClosed(t *testing.T) {
	s := openTestStore(t)
	creator, err := s.CreateUser("invitecreator4", "hash", "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateInvite("corrupt-code", creator.ID, 1, NeverExpires()); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := s.db.Exec(
		`UPDATE invites SET expires_at = ? WHERE code = ?`, "garbage-timestamp", "corrupt-code",
	); err != nil {
		t.Fatalf("corrupting expires_at: %v", err)
	}

	if err := s.ValidateInvite("corrupt-code"); err != ErrInviteInvalid {
		t.Errorf("ValidateInvite on corrupted expiry: got %v, want ErrInviteInvalid", err)
	}
	if err := s.ValidateAndConsumeInvite("corrupt-code"); err != ErrInviteInvalid {
		t.Errorf("ValidateAndConsumeInvite on corrupted expiry: got %v, want ErrInviteInvalid", err)
	}
}

func TestGetUserByUsernameCI(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateUser("SysOp", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := s.GetUserByUsernameCI("sysop")
	if err != nil {
		t.Fatalf("GetUserByUsernameCI(%q): %v", "sysop", err)
	}
	if got.Username != "SysOp" {
		t.Errorf("got username %q, want %q (original case preserved)", got.Username, "SysOp")
	}

	got, err = s.GetUserByUsernameCI("SYSOP")
	if err != nil {
		t.Fatalf("GetUserByUsernameCI(%q): %v", "SYSOP", err)
	}
	if got.Username != "SysOp" {
		t.Errorf("got username %q, want %q", got.Username, "SysOp")
	}

	_, err = s.GetUserByUsernameCI("nobody")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for a genuinely absent username, got %v", err)
	}
}

func TestCountUsableAdmins(t *testing.T) {
	s := openTestStore(t)

	n, err := s.CountUsableAdmins()
	if err != nil {
		t.Fatalf("CountUsableAdmins: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d usable admins on a fresh store, want 0", n)
	}

	if _, err := s.CreateUser("activeAdmin", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	n, err = s.CountUsableAdmins()
	if err != nil {
		t.Fatalf("CountUsableAdmins: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d usable admins with one active admin, want 1", n)
	}

	// Permanently ban the only admin. Forced directly: BanUser now refuses to
	// ban the last usable admin (that's the whole point of the guard), so the
	// zero-usable-admins state can only be constructed out-of-band here.
	forceBan(t, s, "activeAdmin", NeverExpires())
	n, err = s.CountUsableAdmins()
	if err != nil {
		t.Fatalf("CountUsableAdmins: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d usable admins with the only admin permanently banned, want 0", n)
	}

	if _, err := s.CreateUser("lapsedBanAdmin", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Also forced: at this moment lapsedBanAdmin is the sole *usable* admin
	// (activeAdmin is perma-banned), so a guarded BanUser would refuse it too.
	forceBan(t, s, "lapsedBanAdmin", time.Now().Add(-time.Hour))
	n, err = s.CountUsableAdmins()
	if err != nil {
		t.Fatalf("CountUsableAdmins: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d usable admins with one permanently-banned and one lapsed-ban admin, want 1 (lapsed ban self-heals)", n)
	}
}

func TestIsUsableAdmin(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	permanent := NeverExpires()

	cases := []struct {
		name string
		u    User
		want bool
	}{
		{"active admin", User{Role: "admin", Status: "active"}, true},
		{"active user (non-admin)", User{Role: "user", Status: "active"}, false},
		{"suspended admin, permanent ban", User{Role: "admin", Status: "suspended", BannedUntil: sql.NullTime{Time: permanent, Valid: true}}, false},
		{"suspended admin, timed ban not yet expired", User{Role: "admin", Status: "suspended", BannedUntil: sql.NullTime{Time: future, Valid: true}}, false},
		{"suspended admin, timed ban already lapsed", User{Role: "admin", Status: "suspended", BannedUntil: sql.NullTime{Time: past, Valid: true}}, true},
		{"suspended admin, no banned_until set", User{Role: "admin", Status: "suspended"}, false},
		{"pending admin", User{Role: "admin", Status: "pending"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.u.IsUsableAdmin(); got != c.want {
				t.Errorf("IsUsableAdmin() = %v, want %v", got, c.want)
			}
		})
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

// ---- Atomic last-usable-admin guard (audit 2026-07-05 finding #3) --------

// TestBanUser_RefusesLastUsableAdmin proves the invariant: banning down to
// the last usable admin is refused, and the account is left untouched.
func TestBanUser_RefusesLastUsableAdmin(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateUser("admin1", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser admin1: %v", err)
	}
	if _, err := s.CreateUser("admin2", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser admin2: %v", err)
	}

	// Banning one of two usable admins is allowed — one still remains.
	if err := s.BanUser("admin1", nil); err != nil {
		t.Fatalf("banning one of two admins should succeed: %v", err)
	}
	if n, _ := s.CountUsableAdmins(); n != 1 {
		t.Fatalf("after banning one admin, want 1 usable admin, got %d", n)
	}

	// Banning the last usable admin must be refused, atomically.
	if err := s.BanUser("admin2", nil); !errors.Is(err, ErrLastUsableAdmin) {
		t.Errorf("banning the last usable admin: got %v, want ErrLastUsableAdmin", err)
	}
	if n, _ := s.CountUsableAdmins(); n != 1 {
		t.Errorf("last admin must survive a refused ban: want 1 usable admin, got %d", n)
	}
	got, _ := s.GetUserByUsername("admin2")
	if got.Status != "active" {
		t.Errorf("refused-ban admin status = %q, want active (unchanged)", got.Status)
	}
}

// TestDeleteUser_RefusesLastUsableAdmin is the DELETE USER twin of the ban
// test above.
func TestDeleteUser_RefusesLastUsableAdmin(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateUser("admin1", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser admin1: %v", err)
	}
	if _, err := s.CreateUser("admin2", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser admin2: %v", err)
	}

	if err := s.DeleteUser("admin1"); err != nil {
		t.Fatalf("deleting one of two admins should succeed: %v", err)
	}
	if n, _ := s.CountUsableAdmins(); n != 1 {
		t.Fatalf("after deleting one admin, want 1 usable admin, got %d", n)
	}

	if err := s.DeleteUser("admin2"); !errors.Is(err, ErrLastUsableAdmin) {
		t.Errorf("deleting the last usable admin: got %v, want ErrLastUsableAdmin", err)
	}
	if n, _ := s.CountUsableAdmins(); n != 1 {
		t.Errorf("last admin must survive a refused delete: want 1 usable admin, got %d", n)
	}
	if _, err := s.GetUserByUsername("admin2"); err != nil {
		t.Errorf("refused-delete admin should still exist: %v", err)
	}
}

// TestBanUser_NonAdminAllowedWithSingleAdmin confirms the guard only bites
// admins: banning a regular user never touches the admin count, even when a
// single admin remains (the NOT(usable-admin) branch of the predicate).
func TestBanUser_NonAdminAllowedWithSingleAdmin(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateUser("soleadmin", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser soleadmin: %v", err)
	}
	if _, err := s.CreateUser("regular", "hash", "user"); err != nil {
		t.Fatalf("CreateUser regular: %v", err)
	}
	if err := s.BanUser("regular", nil); err != nil {
		t.Errorf("banning a regular user with one admin present should succeed: %v", err)
	}
}

// TestBanUser_NotFoundNotMisreportedAsLastAdmin checks the 0-rows
// disambiguation: a nonexistent target reports ErrNotFound, not the guard's
// ErrLastUsableAdmin.
func TestBanUser_NotFoundNotMisreportedAsLastAdmin(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateUser("soleadmin", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.BanUser("ghost", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("banning a nonexistent user: got %v, want ErrNotFound", err)
	}
}

// TestBanUser_ConcurrentMutualBan is the direct regression for finding #3:
// two admins banning each other. The atomic guard must let at most one
// through — a usable admin always survives. The pool is pinned to a single
// connection because modernc's ":memory:" gives each connection its own
// isolated database; the atomicity guarantee itself is structural (one SQL
// statement under SQLite's write serialization), so serialized execution
// still exercises the exact "second write sees the first" ordering the fix
// relies on.
func TestBanUser_ConcurrentMutualBan(t *testing.T) {
	s := openTestStore(t)
	s.db.SetMaxOpenConns(1)
	if _, err := s.CreateUser("adminA", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminA: %v", err)
	}
	if _, err := s.CreateUser("adminB", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminB: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	targets := []string{"adminB", "adminA"}
	wg.Add(2)
	for i := range targets {
		go func(i int) {
			defer wg.Done()
			errs[i] = s.BanUser(targets[i], nil)
		}(i)
	}
	wg.Wait()

	// Load-bearing assertion: never zero usable admins.
	n, err := s.CountUsableAdmins()
	if err != nil {
		t.Fatalf("CountUsableAdmins: %v", err)
	}
	if n < 1 {
		t.Fatalf("mutual concurrent ban left %d usable admins — invariant violated", n)
	}

	// Exactly one ban succeeds; the other is refused by the guard.
	successes, refusals := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, ErrLastUsableAdmin):
			refusals++
		default:
			t.Errorf("unexpected BanUser error in mutual ban: %v", e)
		}
	}
	if successes != 1 || refusals != 1 {
		t.Errorf("mutual ban: got %d success / %d refused, want 1 / 1", successes, refusals)
	}
}

// ---- DSN timezone guard (audit 2026-07-05 finding #4) --------------------

// TestTimestampRoundTripsAsUTC is the regression guard for audit 2026-07-05 finding
// #4: store.Open's SQLite DSN must never gain a ?_timezone= (or _loc) parameter.
// Timestamps are written naive-UTC and read back correctly as UTC ONLY because the
// driver's connection loc is nil (no DSN timezone param). If someone adds
// ?_timezone=Local, the driver ParseInLocation's stored UTC strings in the server's
// zone, skewing every ban/lockout comparison by the UTC offset. To keep that
// observable even in a UTC CI (where Local == UTC would hide it), this test pins
// time.Local to a fixed non-UTC zone: a future ?_timezone=Local then resolves to THIS
// zone and shifts the round-tripped instant, failing the assertion. (None of this
// package's tests use t.Parallel(), so the global time.Local swap is safe; it's
// restored on cleanup and has no effect on the current nil-loc path.)
func TestTimestampRoundTripsAsUTC(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("TEST+5", 5*60*60) // UTC+5, no DST, no tzdata dependency
	t.Cleanup(func() { time.Local = orig })

	s := openTestStore(t)
	if _, err := s.CreateUser("tzuser", "hash", "user"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// A pinned, unambiguous future instant in UTC. BanUser stores it as
	// until.UTC().Format("2006-01-02 15:04:05") — a naive-UTC string.
	want := time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := s.BanUser("tzuser", &want); err != nil {
		t.Fatalf("BanUser: %v", err)
	}

	got, err := s.GetUserByUsername("tzuser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if !got.BannedUntil.Valid {
		t.Fatal("banned_until did not round-trip as a valid timestamp")
	}
	// Equal compares absolute instants (location-independent): passes today
	// (UTC in, UTC out), fails only if a DSN timezone param made the driver
	// reinterpret the stored UTC string in a non-UTC zone.
	if !got.BannedUntil.Time.Equal(want) {
		t.Errorf("banned_until round-tripped to %v (want %v): a UTC/local skew of %v — did store.Open's DSN gain a _timezone param?",
			got.BannedUntil.Time.UTC(), want, got.BannedUntil.Time.Sub(want))
	}
}
