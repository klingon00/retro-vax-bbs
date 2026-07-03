package registry

import "testing"

func TestRegisterAndList(t *testing.T) {
	r := New()
	r.Register("carol", "user", false, "LOBBY")

	views := r.List("user")
	if len(views) != 1 || views[0].Username != "carol" || views[0].Count != 1 {
		t.Fatalf("expected [{carol 1}], got %v", views)
	}
}

func TestMultipleSessions_ShowsCount(t *testing.T) {
	r := New()
	r.Register("carol", "user", false, "LOBBY")
	r.Register("carol", "user", false, "LOBBY") // second session, same user

	views := r.List("user")
	if len(views) != 1 {
		t.Fatalf("expected 1 entry for carol, got %d", len(views))
	}
	if views[0].Count != 2 {
		t.Errorf("expected count 2, got %d", views[0].Count)
	}
}

func TestUnregister_DecrementsCount(t *testing.T) {
	r := New()
	r.Register("carol", "user", false, "LOBBY")
	r.Register("carol", "user", false, "LOBBY")
	r.Unregister("carol")

	views := r.List("user")
	if len(views) != 1 || views[0].Count != 1 {
		t.Fatalf("expected [{carol 1}] after one unregister, got %v", views)
	}
}

func TestUnregister_RemovesEntryAtZero(t *testing.T) {
	r := New()
	r.Register("carol", "user", false, "LOBBY")
	r.Unregister("carol")

	views := r.List("user")
	if len(views) != 0 {
		t.Fatalf("expected empty list after full unregister, got %v", views)
	}
}

func TestList_AlphabeticallySorted(t *testing.T) {
	r := New()
	r.Register("zebra", "user", false, "LOBBY")
	r.Register("alice", "user", false, "LOBBY")
	r.Register("carol", "user", false, "LOBBY")

	views := r.List("user")
	if views[0].Username != "alice" || views[1].Username != "carol" || views[2].Username != "zebra" {
		t.Errorf("expected alphabetical order, got %v", views)
	}
}

func TestList_AdminInvisibleToRegularUser(t *testing.T) {
	r := New()
	r.Register("sysop", "admin", false, "LOBBY") // adminVisible=false
	r.Register("carol", "user", false, "LOBBY")

	views := r.List("user") // regular viewer
	if len(views) != 1 || views[0].Username != "carol" {
		t.Fatalf("invisible admin should not appear to regular user, got %v", views)
	}
}

func TestList_AdminVisibleWhenOptedIn(t *testing.T) {
	r := New()
	r.Register("sysop", "admin", true, "LOBBY") // adminVisible=true
	r.Register("carol", "user", false, "LOBBY")

	views := r.List("user") // regular viewer
	if len(views) != 2 {
		t.Fatalf("opted-in admin should appear to regular user, got %v", views)
	}
}

func TestList_AdminSeesOtherAdmins(t *testing.T) {
	r := New()
	r.Register("sysop", "admin", false, "LOBBY") // invisible, but admin viewer
	r.Register("carol", "user", false, "LOBBY")

	views := r.List("admin") // admin viewer
	if len(views) != 2 {
		t.Fatalf("admin viewer should see invisible admin, got %v", views)
	}
}
