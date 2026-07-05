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
	return New(username, role, reg, db, calls, io.Discard, 0)
}

func TestLastUsableAdminGuard(t *testing.T) {
	db := newTestLobbyStore(t)
	if _, err := db.CreateUser("adminA", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminA: %v", err)
	}
	m := newTestModel("adminA", "admin", db)

	if e := lastUsableAdminGuard(m, "BAN", "adminA"); e == "" {
		t.Error("expected guard to refuse targeting the last usable admin, got no refusal")
	}

	if _, err := db.CreateUser("adminB", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser adminB: %v", err)
	}
	if e := lastUsableAdminGuard(m, "BAN", "adminA"); e != "" {
		t.Errorf("expected guard to allow action with 2 usable admins, got refusal: %q", e)
	}
	if e := lastUsableAdminGuard(m, "BAN", "adminB"); e != "" {
		t.Errorf("expected guard to allow action with 2 usable admins, got refusal: %q", e)
	}

	if _, err := db.CreateUser("regularUser", "hash", "user"); err != nil {
		t.Fatalf("CreateUser regularUser: %v", err)
	}
	if e := lastUsableAdminGuard(m, "BAN", "regularUser"); e != "" {
		t.Errorf("expected guard to allow targeting a non-admin (can't drop admin count), got refusal: %q", e)
	}
}

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

func TestDeleteUserCommand_RefusesLastUsableAdminTarget(t *testing.T) {
	db := newTestLobbyStore(t)
	if _, err := db.CreateUser("adminA", "hash", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	m := newTestModel("adminA", "admin", db)

	// deleteUserCommand's own self-guard would otherwise short-circuit
	// before reaching lastUsableAdminGuard for a self-target, so exercise
	// the guard directly with the same label deleteUserCommand uses — this
	// is the scenario the guard exists to cover even though normal
	// operation (an admin acting on themselves) hits the self-guard first.
	if e := lastUsableAdminGuard(m, "DELETE USER", "adminA"); e == "" {
		t.Error("expected guard to refuse targeting the last usable admin for DELETE USER")
	}
}
