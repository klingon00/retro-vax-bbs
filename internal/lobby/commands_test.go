package lobby

import (
	"io"
	"strings"
	"testing"

	"github.com/klingon00/retro-vax-bbs/internal/phone"
	"github.com/klingon00/retro-vax-bbs/internal/registry"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

// newTestLobbyStore opens an in-memory SQLite database for testing —
// discarded when the test process exits, no cleanup needed.
func newTestLobbyStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTestModel(username, role string, db *store.Store) Model {
	reg := registry.New()
	calls := phone.NewCalls(reg)
	sid := reg.Register(username, role, false, "LOBBY")
	return New(username, role, sid, reg, db, calls, io.Discard, 0)
}

// Note: the last-usable-admin invariant itself is now enforced and unit-
// tested at the store layer (internal/store: TestBanUser_RefusesLastUsableAdmin,
// TestDeleteUser_RefusesLastUsableAdmin, TestBanUser_ConcurrentMutualBan).
// The lobby tests below cover the handler wiring: that banCommand /
// deleteUserCommand map store.ErrLastUsableAdmin to the user-facing LASTADMIN
// message and leave the account untouched on refusal.

func TestBanCommand_RefusesSelfBanAsLastUsableAdmin(t *testing.T) {
	db := newTestLobbyStore(t)
	if _, err := db.CreateUser("adminA", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	m := newTestModel("adminA", "admin", db)

	msg, _ := banCommand(m, "adminA perm")
	if !strings.Contains(msg, "LASTADMIN") {
		t.Errorf("expected LASTADMIN refusal for self-ban as last usable admin, got: %q", msg)
	}
	u, err := db.GetUserByUsername("adminA")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.Status != "active" {
		t.Errorf("account should remain active after a refused ban, got status %q", u.Status)
	}
}

func TestBanCommand_AllowsBanWithAnotherAdminRemaining(t *testing.T) {
	db := newTestLobbyStore(t)
	if _, err := db.CreateUser("adminA", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminA: %v", err)
	}
	if _, err := db.CreateUser("adminB", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminB: %v", err)
	}
	m := newTestModel("adminA", "admin", db)

	msg, _ := banCommand(m, "adminB perm")
	if strings.Contains(msg, "LASTADMIN") {
		t.Fatalf("ban should have succeeded with 2 usable admins, got refusal: %q", msg)
	}
	u, err := db.GetUserByUsername("adminB")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.Status != "suspended" {
		t.Errorf("adminB should be suspended after a successful ban, got status %q", u.Status)
	}

	// adminA is now the last usable admin — a self-ban should be refused.
	msg2, _ := banCommand(m, "adminA perm")
	if !strings.Contains(msg2, "LASTADMIN") {
		t.Errorf("expected LASTADMIN refusal for self-ban as last remaining admin, got: %q", msg2)
	}
}

func TestDeleteUserCommand_MapsLastUsableAdminRefusal(t *testing.T) {
	db := newTestLobbyStore(t)
	// Only adminB exists as a usable admin, and the acting session claims a
	// different admin username ("adminA"). Normal operation can't reach the
	// last-usable-admin case through DELETE USER for a *self* target — the
	// E-SELF self-guard fires first — so this drives the defensive
	// ErrLastUsableAdmin -> LASTADMIN mapping via a non-self target that is
	// nonetheless the last usable admin in the store.
	if _, err := db.CreateUser("adminB", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminB: %v", err)
	}
	m := newTestModel("adminA", "admin", db)

	msg, _ := deleteUserCommand(m, "adminB")
	if !strings.Contains(msg, "LASTADMIN") {
		t.Errorf("expected LASTADMIN refusal deleting the last usable admin, got: %q", msg)
	}
	if _, err := db.GetUserByUsername("adminB"); err != nil {
		t.Errorf("last admin must survive a refused delete: %v", err)
	}
}

// ---- Reserved "new" username in admin CREATE USER (audit 2026-07-05 #5) ----

// TestValidateNewUsername_BlocksNewSentinel guards finding #5: the admin
// CREATE USER path (via validateNewUsername) must reject "new" — the
// self-registration routing sentinel — case-insensitively, while still
// allowing the rest of the reserved-word set that self-registration blocks
// (admins may legitimately create "sysop"/"admin"/"root"), and still
// enforcing the format rules.
func TestValidateNewUsername_BlocksNewSentinel(t *testing.T) {
	cases := []struct {
		name      string
		username  string
		wantError bool
	}{
		{"new lowercase blocked", "new", true},
		{"New mixed-case blocked", "New", true},
		{"NEW uppercase blocked", "NEW", true},
		{"reserved-but-admin-allowed sysop", "sysop", false},
		{"reserved-but-admin-allowed admin", "admin", false},
		{"reserved-but-admin-allowed root", "root", false},
		{"ordinary name allowed", "alice", false},
		{"too short rejected", "ab", true},
		{"bad char rejected", "bad!", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateNewUsername(c.username)
			if c.wantError && err == nil {
				t.Errorf("validateNewUsername(%q) = nil, want an error", c.username)
			}
			if !c.wantError && err != nil {
				t.Errorf("validateNewUsername(%q) = %v, want nil", c.username, err)
			}
		})
	}
}
