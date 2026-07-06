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

// isClosed reports whether a signal-only channel (one that is only ever
// closed, never sent to) has been closed, without blocking.
func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func TestUnregister_ClosesDoneChannel(t *testing.T) {
	r := New()
	r.Register("carol", "user", false, "LOBBY")

	_, done := r.Events("carol")
	if done == nil {
		t.Fatal("Events returned a nil done channel for a registered user")
	}
	if isClosed(done) {
		t.Fatal("done should be open while the session is active")
	}

	r.Unregister("carol")
	if !isClosed(done) {
		t.Fatal("done should be closed once the last session unregisters")
	}
}

func TestUnregister_DoneStaysOpenUntilLastSession(t *testing.T) {
	r := New()
	r.Register("carol", "user", false, "LOBBY")
	r.Register("carol", "user", false, "LOBBY") // second session shares the entry
	_, done := r.Events("carol")

	r.Unregister("carol") // one session remains — done must stay open
	if isClosed(done) {
		t.Fatal("done closed while a session for carol still remains")
	}

	r.Unregister("carol") // last session gone — done must close
	if !isClosed(done) {
		t.Fatal("done should be closed after the final session unregisters")
	}
}

func TestEvents_MatchedPairAndNilWhenAbsent(t *testing.T) {
	r := New()

	// Not connected: both nil, so waitForPhoneEvent's guard returns a nil Cmd.
	notify, done := r.Events("ghost")
	if notify != nil || done != nil {
		t.Fatalf("Events for an absent user should return (nil, nil), got notify!=nil=%v done!=nil=%v",
			notify != nil, done != nil)
	}

	// Connected: both non-nil (a matched pair from the same entry).
	r.Register("carol", "user", false, "LOBBY")
	notify, done = r.Events("carol")
	if notify == nil || done == nil {
		t.Fatalf("Events for a registered user should return a non-nil pair, got notify!=nil=%v done!=nil=%v",
			notify != nil, done != nil)
	}
}
