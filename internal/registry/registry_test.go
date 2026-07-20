package registry

import "testing"

// --- Presence (account-level: WHO / FINGER) --------------------------------

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
	r.Register("carol", "user", false, "LOBBY") // second session, same account

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
	sid1 := r.Register("carol", "user", false, "LOBBY")
	r.Register("carol", "user", false, "LOBBY")
	r.Unregister(sid1)

	views := r.List("user")
	if len(views) != 1 || views[0].Count != 1 {
		t.Fatalf("expected [{carol 1}] after one unregister, got %v", views)
	}
}

func TestUnregister_RemovesEntryAtZero(t *testing.T) {
	r := New()
	sid := r.Register("carol", "user", false, "LOBBY")
	r.Unregister(sid)

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

// --- Per-session event delivery --------------------------------------------

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

// recv does a non-blocking receive so tests can assert what did (and did not)
// land on a session's notify channel.
func recv(ch <-chan PhoneEvent) (PhoneEvent, bool) {
	select {
	case e := <-ch:
		return e, true
	default:
		return PhoneEvent{}, false
	}
}

// TestSendToSession_ReachesOnlyTargetSession is the unit-level tripwire for
// finding 11: an event addressed to one session of an account must reach ONLY
// that session, never a sibling session sharing the account. Under the old
// shared-per-account channel this could not even be expressed — there was one
// channel, so a "sibling receiver" did not exist. Its existence encodes the fix.
func TestSendToSession_ReachesOnlyTargetSession(t *testing.T) {
	r := New()
	sid1 := r.Register("bob", "user", false, "LOBBY")
	sid2 := r.Register("bob", "user", false, "LOBBY")

	n1, _ := r.Events(sid1)
	n2, _ := r.Events(sid2)

	r.SendToSession(sid1, PhoneEvent{Type: EventAnswer, CallID: "c1"})

	if e, ok := recv(n1); !ok || e.CallID != "c1" {
		t.Fatalf("target session should have received the event, got ok=%v e=%+v", ok, e)
	}
	if _, ok := recv(n2); ok {
		t.Fatal("sibling session must NOT receive an event addressed to the other session (finding 11)")
	}
}

func TestSendToSession_UnknownSessionIsNoop(t *testing.T) {
	r := New()
	// Must not panic and must simply drop the event.
	r.SendToSession("no-such-session", PhoneEvent{Type: EventRing})
}

func TestSessionsOf(t *testing.T) {
	r := New()
	if ids := r.SessionsOf("nobody"); ids != nil {
		t.Fatalf("SessionsOf for an absent account should be nil, got %v", ids)
	}

	sid1 := r.Register("bob", "user", false, "LOBBY")
	sid2 := r.Register("bob", "user", false, "LOBBY")

	ids := r.SessionsOf("bob")
	if len(ids) != 2 {
		t.Fatalf("expected 2 session IDs, got %v", ids)
	}
	seen := map[string]bool{ids[0]: true, ids[1]: true}
	if !seen[sid1] || !seen[sid2] {
		t.Fatalf("SessionsOf should return both sids %q and %q, got %v", sid1, sid2, ids)
	}

	r.Unregister(sid1)
	ids = r.SessionsOf("bob")
	if len(ids) != 1 || ids[0] != sid2 {
		t.Fatalf("after unregistering sid1, expected [%s], got %v", sid2, ids)
	}
}

func TestConnected(t *testing.T) {
	r := New()
	if r.Connected("bob") {
		t.Fatal("Connected should be false before any session registers")
	}
	sid := r.Register("bob", "user", false, "LOBBY")
	if !r.Connected("bob") {
		t.Fatal("Connected should be true with an active session")
	}
	r.Unregister(sid)
	if r.Connected("bob") {
		t.Fatal("Connected should be false after the last session departs")
	}
}

func TestUnregister_ClosesDoneChannel(t *testing.T) {
	r := New()
	sid := r.Register("carol", "user", false, "LOBBY")

	_, done := r.Events(sid)
	if done == nil {
		t.Fatal("Events returned a nil done channel for a registered session")
	}
	if isClosed(done) {
		t.Fatal("done should be open while the session is active")
	}

	r.Unregister(sid)
	if !isClosed(done) {
		t.Fatal("done should be closed once the session unregisters")
	}
}

// TestUnregister_ClosesOnlyThatSessionsDone replaces the old shared-done test:
// each session now owns its done, so unregistering one session must close only
// its own done and leave a sibling's open.
func TestUnregister_ClosesOnlyThatSessionsDone(t *testing.T) {
	r := New()
	sid1 := r.Register("bob", "user", false, "LOBBY")
	sid2 := r.Register("bob", "user", false, "LOBBY")
	_, done1 := r.Events(sid1)
	_, done2 := r.Events(sid2)

	r.Unregister(sid1)
	if !isClosed(done1) {
		t.Fatal("the unregistered session's done should be closed")
	}
	if isClosed(done2) {
		t.Fatal("a sibling session's done must stay open when another session departs")
	}

	r.Unregister(sid2)
	if !isClosed(done2) {
		t.Fatal("the last session's done should close on its unregister")
	}
}

// TestUnregister_SiblingKeepsReceiving confirms the account entry and the
// surviving session's delivery both outlive a sibling's departure.
func TestUnregister_SiblingKeepsReceiving(t *testing.T) {
	r := New()
	sid1 := r.Register("bob", "user", false, "LOBBY")
	sid2 := r.Register("bob", "user", false, "LOBBY")
	n2, _ := r.Events(sid2)

	r.Unregister(sid1) // sid1 leaves; the account entry and sid2 survive

	r.SendToSession(sid2, PhoneEvent{Type: EventRing, CallID: "c9"})
	if e, ok := recv(n2); !ok || e.CallID != "c9" {
		t.Fatalf("surviving session should still receive events, got ok=%v e=%+v", ok, e)
	}
	if !r.Connected("bob") {
		t.Fatal("bob should still be connected via the surviving session")
	}
}

func TestEvents_MatchedPairAndNilWhenAbsent(t *testing.T) {
	r := New()

	// Unknown session: both nil, so waitForPhoneEvent's guard returns a nil Cmd.
	notify, done := r.Events("no-such-session")
	if notify != nil || done != nil {
		t.Fatalf("Events for an unknown session should return (nil, nil), got notify!=nil=%v done!=nil=%v",
			notify != nil, done != nil)
	}

	// Known session: both non-nil (a matched pair from the same sessionState).
	sid := r.Register("carol", "user", false, "LOBBY")
	notify, done = r.Events(sid)
	if notify == nil || done == nil {
		t.Fatalf("Events for a registered session should return a non-nil pair, got notify!=nil=%v done!=nil=%v",
			notify != nil, done != nil)
	}
}

func TestNotifyAdmins_ReachesEveryAdminSession(t *testing.T) {
	r := New()
	a1 := r.Register("sysop", "admin", false, "LOBBY")
	a2 := r.Register("sysop", "admin", false, "LOBBY") // second admin session
	u := r.Register("carol", "user", false, "LOBBY")

	r.NotifyAdmins(PhoneEvent{Type: EventAdminNotify, Caller: "newbie"})

	for _, sid := range []string{a1, a2} {
		n, _ := r.Events(sid)
		if e, ok := recv(n); !ok || e.Type != EventAdminNotify {
			t.Fatalf("admin session %s should have received the notify, got ok=%v e=%+v", sid, ok, e)
		}
	}
	nu, _ := r.Events(u)
	if _, ok := recv(nu); ok {
		t.Fatal("a non-admin session must not receive NotifyAdmins")
	}
}
