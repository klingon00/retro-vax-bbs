package store

import "testing"

func TestSetPassword_UpdatesHashAndClearsFlag(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("testuser", "old-hash", "user")

	if err := s.ExpirePassword("testuser"); err != nil {
		t.Fatalf("ExpirePassword: %v", err)
	}
	pending, _ := s.GetUserByID(u.ID)
	if !pending.MustChangePassword {
		t.Fatal("pre-condition: must_change_password should be set before SetPassword")
	}

	if err := s.SetPassword("testuser", "new-hash"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	got, _ := s.GetUserByID(u.ID)
	if got.PasswordHash.String != "new-hash" {
		t.Errorf("got password hash %q, want %q", got.PasswordHash.String, "new-hash")
	}
	if got.MustChangePassword {
		t.Error("must_change_password should be cleared after SetPassword")
	}
}

func TestSetPassword_NotFound(t *testing.T) {
	s := openTestStore(t)

	if err := s.SetPassword("nobody", "new-hash"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestExpirePassword_SetsFlag(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("testuser", "hash", "user")

	fresh, _ := s.GetUserByID(u.ID)
	if fresh.MustChangePassword {
		t.Fatal("pre-condition: new user should not have must_change_password set")
	}

	if err := s.ExpirePassword("testuser"); err != nil {
		t.Fatalf("ExpirePassword: %v", err)
	}

	got, _ := s.GetUserByID(u.ID)
	if !got.MustChangePassword {
		t.Error("must_change_password should be set after ExpirePassword")
	}
	if got.PasswordHash.String != "hash" {
		t.Error("ExpirePassword should not change the existing password hash")
	}
}

func TestExpirePassword_NotFound(t *testing.T) {
	s := openTestStore(t)

	if err := s.ExpirePassword("nobody"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
