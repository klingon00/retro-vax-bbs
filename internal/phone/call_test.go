package phone

import (
	"errors"
	"testing"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
)

// isChanClosed reports whether ch is closed, without blocking. The test
// channels are never sent to, so a receive that succeeds means "closed"
// (the zero value with ok == false); an empty open channel takes the default.
func isChanClosed(ch <-chan CharEvent) bool {
	select {
	case _, ok := <-ch:
		return !ok
	default:
		return false
	}
}

// containsType reports whether any event in evs has the given type.
func containsType(evs []registry.PhoneEvent, typ registry.EventType) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// drainEvents collects whatever is queued on a session's notify channel without
// blocking. The channel is buffered (size 8) and never closed, so a default
// branch is the only safe way to stop. Keyed by sessionID, matching the
// per-session event delivery the registry now provides.
func drainEvents(t *testing.T, reg *registry.Registry, sessionID string) []registry.PhoneEvent {
	t.Helper()
	ch, _ := reg.Events(sessionID)
	var out []registry.PhoneEvent
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// dialAndAnswer sets up an active 2-party call between alice (caller) and bob
// (callee), registering both so their participants carry real session IDs.
// Returns the call plus both participants.
func dialAndAnswer(t *testing.T) (*Calls, *Call, *Participant, *Participant) {
	t.Helper()
	reg := registry.New()
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	bobSid := reg.Register("bob", "user", false, "LOBBY")
	calls := NewCalls(reg)

	call, callerP, err := calls.Dial(aliceSid, "alice", "bob")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_, calleeP, err := calls.Answer(call.ID, bobSid, "bob") // active; also closes stopRing
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	return calls, call, callerP, calleeP
}

func TestHangup_ClosesDepartingParticipantOnly(t *testing.T) {
	calls, call, callerP, calleeP := dialAndAnswer(t)

	calls.Hangup(call.ID, callerP.SessionID)

	if !isChanClosed(callerP.IncomingChar) {
		t.Fatal("departing participant's IncomingChar should be closed after Hangup")
	}
	if isChanClosed(calleeP.IncomingChar) {
		t.Fatal("remaining participant's IncomingChar must NOT be closed")
	}
	// Call still has bob — it must not be torn down while a participant remains.
	if parts := calls.Participants(call.ID); len(parts) != 1 || parts[0] != "bob" {
		t.Fatalf("expected only bob remaining, got %v", parts)
	}
}

func TestHangupSession_RemovesFromActiveCallWithoutCallID(t *testing.T) {
	calls, call, callerP, _ := dialAndAnswer(t)

	// Mimic a dropped SSH session: teardown knows the sessionID but not the
	// callID, so it calls HangupSession. Keying on sessionID (not username) is
	// what keeps a dropped session from tearing down a sibling session's call.
	calls.HangupSession(callerP.SessionID)

	if !isChanClosed(callerP.IncomingChar) {
		t.Fatal("HangupSession should close the departed session's IncomingChar")
	}
	parts := calls.Participants(call.ID)
	if len(parts) != 1 || parts[0] != "bob" {
		t.Fatalf("alice should have been removed, expected only bob, got %v", parts)
	}
}

func TestHangup_IdempotentNoDoubleClose(t *testing.T) {
	// If any of these repeated removals double-closed IncomingChar, the test
	// would panic ("close of closed channel") and fail.
	calls, call, callerP, _ := dialAndAnswer(t)

	calls.Hangup(call.ID, callerP.SessionID) // removes + closes callerP
	calls.HangupSession(callerP.SessionID)   // no-op: alice's session already gone
	calls.Hangup(call.ID, callerP.SessionID) // no-op: not a participant

	if !isChanClosed(callerP.IncomingChar) {
		t.Fatal("caller channel should remain closed after repeated hangups")
	}
}

func TestReject_ClosesCallerChannel(t *testing.T) {
	reg := registry.New()
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	reg.Register("bob", "user", false, "LOBBY")
	calls := NewCalls(reg)

	call, callerP, err := calls.Dial(aliceSid, "alice", "bob") // pending
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if err := calls.Reject(call.ID, "bob"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	// The caller leaves a rejected pending call via goIdle in its EventReject
	// handler, not via Hangup, so Reject must close the caller's channel itself.
	if !isChanClosed(callerP.IncomingChar) {
		t.Fatal("Reject should close the caller's IncomingChar")
	}
}

// ---- Call admission -----------------------------------------------------------
//
// Regression tests for docs/audits/audit-2026-07-13-phone-call-admission.md.
// Dial and Add are the only two entry points that start a ring, and both route
// through admitLocked — so each rule is asserted on whichever entry point can
// actually reach the state, not mechanically on both.

// isStopClosed reports whether a ring's stop channel is closed, without
// blocking. Nothing is ever sent on these — they are signal-only — so a
// receive that succeeds means closed.
func isStopClosed(ch <-chan struct{}) bool {
	select {
	case _, ok := <-ch:
		return !ok
	default:
		return false
	}
}

// activeCall builds an answered 2-party call between caller and callee, both of
// whom it registers with fresh sessions. Returns the call and the two session
// IDs, so tests can drive Hangup/HangupSession by session.
func activeCall(t *testing.T, calls *Calls, reg *registry.Registry, caller, callee string) (*Call, string, string) {
	t.Helper()
	callerSid := reg.Register(caller, "user", false, "LOBBY")
	calleeSid := reg.Register(callee, "user", false, "LOBBY")
	call, _, err := calls.Dial(callerSid, caller, callee)
	if err != nil {
		t.Fatalf("Dial(%s, %s): %v", caller, callee, err)
	}
	if _, _, err := calls.Answer(call.ID, calleeSid, callee); err != nil {
		t.Fatalf("Answer(%s): %v", callee, err)
	}
	return call, callerSid, calleeSid
}

// Finding 6: a self-dial was admitted by every gate — it created a real pending
// call that rang the caller's own bell every 10s.
func TestDial_RejectsSelfCall(t *testing.T) {
	reg := registry.New()
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	calls := NewCalls(reg)

	_, _, err := calls.Dial(aliceSid, "alice", "alice")
	if !errors.Is(err, ErrSelfCall) {
		t.Fatalf("Dial to self: want ErrSelfCall, got %v", err)
	}
	if len(calls.calls) != 0 {
		t.Fatalf("a rejected self-dial must not create a call, got %d", len(calls.calls))
	}
}

// The registry is exact-match keyed, so a differently-cased self-dial would
// otherwise fall through to "not connected" rather than naming the real cause.
func TestDial_RejectsSelfCallRegardlessOfCase(t *testing.T) {
	reg := registry.New()
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	calls := NewCalls(reg)

	if _, _, err := calls.Dial(aliceSid, "alice", "ALICE"); !errors.Is(err, ErrSelfCall) {
		t.Fatalf("Dial to self (mixed case): want ErrSelfCall, got %v", err)
	}
}

// Finding 6, ADD half: %ADD <self> rang you into your own call.
func TestAdd_RejectsSelfCall(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call, _, _ := activeCall(t, calls, reg, "alice", "bob")

	if err := calls.Add(call.ID, "alice", "alice"); !errors.Is(err, ErrSelfCall) {
		t.Fatalf("Add self: want ErrSelfCall, got %v", err)
	}
}

// Findings 1 and 2 — the reported bug. Two separate active calls; a participant
// in call B rings a participant in call A. Dial's busy check never saw this
// because doDial reroutes in-call DIAL to Add, which had no busy check at all.
func TestAdd_RejectsCalleeInAnotherCall(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	activeCall(t, calls, reg, "alice", "bob")                 // call A
	callB, _, _ := activeCall(t, calls, reg, "carol", "dave") // call B

	err := calls.Add(callB.ID, "dave", "bob")
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Add of a callee already in another call: want ErrBusy, got %v", err)
	}
	if _, ringing := calls.pendingAdds[addKey{callID: callB.ID, callee: "bob"}]; ringing {
		t.Fatal("a refused Add must not leave a ring outstanding")
	}
}

// Finding 5: the busy scan only considered CallActive, so someone already being
// rung (pending, unanswered) read as free. The second ring would overwrite the
// callee's pendingCallID and strand the first caller's call permanently.
func TestDial_RejectsCalleeAlreadyBeingRung(t *testing.T) {
	reg := registry.New()
	bobSid := reg.Register("bob", "user", false, "LOBBY")
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	reg.Register("carol", "user", false, "LOBBY")
	calls := NewCalls(reg)

	if _, _, err := calls.Dial(bobSid, "bob", "carol"); err != nil {
		t.Fatalf("first Dial: %v", err)
	}
	_, _, err := calls.Dial(aliceSid, "alice", "carol")
	if !errors.Is(err, ErrBeingRung) {
		t.Fatalf("Dial of a callee mid-ring: want ErrBeingRung, got %v", err)
	}
}

// Finding 4: a DIAL and a conference ADD could target the same person at once,
// producing two ring goroutines; whichever call lost the race rang forever.
func TestDial_RejectsCalleeBeingAddedToConference(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call, _, _ := activeCall(t, calls, reg, "alice", "bob")
	reg.Register("carol", "user", false, "LOBBY")
	daveSid := reg.Register("dave", "user", false, "LOBBY")

	if err := calls.Add(call.ID, "alice", "carol"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, _, err := calls.Dial(daveSid, "dave", "carol")
	if !errors.Is(err, ErrBeingRung) {
		t.Fatalf("Dial of a callee being ADDed: want ErrBeingRung, got %v", err)
	}
}

// Finding 8 (gap E), resolved as a consequence of the shared predicate: one ring
// per callee at a time, with no per-call exception. Refusing the second ADD makes
// the old cross-cancellation (first inviter's keystroke killing the second
// inviter's ring) unreachable, since only one ring can ever exist.
func TestAdd_RejectsSecondAddOfSameTarget(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call, _, _ := activeCall(t, calls, reg, "alice", "bob")
	reg.Register("carol", "user", false, "LOBBY")

	if err := calls.Add(call.ID, "alice", "carol"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := calls.Add(call.ID, "bob", "carol"); !errors.Is(err, ErrBeingRung) {
		t.Fatalf("second Add of same target: want ErrBeingRung, got %v", err)
	}
	if n := len(calls.pendingAdds); n != 1 {
		t.Fatalf("want exactly 1 outstanding ring, got %d", n)
	}
}

// Finding 7: ADD of someone already in this very call rang an existing
// participant, who could not answer. Now covered by the session-aware busy
// check (their session is a participant, hence not ringable).
func TestAdd_RejectsExistingParticipant(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call, _, _ := activeCall(t, calls, reg, "alice", "bob")

	if err := calls.Add(call.ID, "alice", "bob"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Add of an existing participant: want ErrBusy, got %v", err)
	}
}

// Finding 3: hangupLocked never touched pendingAdds, so tearing the call down
// with a conference ring outstanding left the ring goroutine running — ringing
// the target every 10s forever for a call that no longer exists.
func TestAdd_RingStopsWhenCallIsTornDown(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call, aliceSid, bobSid := activeCall(t, calls, reg, "alice", "bob")
	reg.Register("carol", "user", false, "LOBBY")

	if err := calls.Add(call.ID, "alice", "carol"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ring, ok := calls.pendingAdds[addKey{callID: call.ID, callee: "carol"}]
	if !ok {
		t.Fatal("Add should have registered an outstanding ring")
	}
	if ring.inviter != "alice" {
		t.Fatalf("outstanding ring should record its inviter: want alice, got %q", ring.inviter)
	}

	// Everyone leaves before carol answers — the call is deleted.
	calls.Hangup(call.ID, aliceSid)
	calls.Hangup(call.ID, bobSid)

	if _, still := calls.calls[call.ID]; still {
		t.Fatal("call should be gone once the last participant leaves")
	}
	if !isStopClosed(ring.stop) {
		t.Fatal("ring goroutine's stop channel must be closed when the call is torn down")
	}
	if n := len(calls.pendingAdds); n != 0 {
		t.Fatalf("pendingAdds must be empty after teardown, got %d", n)
	}
}

// ---- Finding 11: per-session routing --------------------------------------------

// Finding 11 fix — flips the old admission-only tripwire to works-end-to-end.
// An account with one session already in a call can place a SECOND call from
// another session, AND that call completes: the answer reaches the DIALING
// session's channel and no sibling steals it. The old version of this test
// (TestDial_AdmitsCallerWhoseAccountIsAlreadyInACall) only asserted Dial
// returned no error, which is exactly the gap that let finding 11 hide behind a
// green suite — "the guard permits X" is not "X works". This drives X.
func TestDial_SecondSessionCallCompletes(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	// alice session 1 is in a call with bob.
	_, alice1, _ := activeCall(t, calls, reg, "alice", "bob")
	// alice session 2 (idle) and the new callee register.
	alice2 := reg.Register("alice", "user", false, "LOBBY")
	carolSid := reg.Register("carol", "user", false, "LOBBY")

	// Clear alice1's own EventAnswer (from her first call) so a later drain of
	// alice1 sees only what the SECOND call delivers.
	drainEvents(t, reg, alice1)

	call, callerP, err := calls.Dial(alice2, "alice", "carol")
	if err != nil {
		t.Fatalf("alice's second session must be able to dial while another is in a call: %v", err)
	}
	if callerP.SessionID != alice2 {
		t.Fatalf("caller participant should be alice's dialing session, got %q", callerP.SessionID)
	}

	if _, _, err := calls.Answer(call.ID, carolSid, "carol"); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// The answer reached the dialing session (alice2), not the sibling (alice1).
	if evs := drainEvents(t, reg, alice2); !containsType(evs, registry.EventAnswer) {
		t.Fatalf("dialing session alice2 must receive EventAnswer, got %v", evs)
	}
	if evs := drainEvents(t, reg, alice1); containsType(evs, registry.EventAnswer) {
		t.Fatal("sibling session alice1 must NOT receive alice2's EventAnswer (finding 11)")
	}
	if calls.calls[call.ID].State != CallActive {
		t.Fatal("the second call should be active after answer")
	}
}

// A callee with one busy session and one idle session is dialable: admitted, and
// the ring reaches ONLY the idle session. This is the one-call-per-session
// behavior on the callee side.
func TestDial_AdmittedWhenCalleeHasAnIdleSession(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	_, _, bobBusy := activeCall(t, calls, reg, "alice", "bob") // bob session 1 busy
	bobIdle := reg.Register("bob", "user", false, "LOBBY")     // bob session 2 idle
	daveSid := reg.Register("dave", "user", false, "LOBBY")

	// bobBusy still holds the EventRing from activeCall's setup dial (a live
	// session's Bubble Tea loop would have consumed it; a unit test does not).
	// Drain it so the assertion below reflects only what dave's dial delivers.
	drainEvents(t, reg, bobBusy)

	if _, _, err := calls.Dial(daveSid, "dave", "bob"); err != nil {
		t.Fatalf("dialing bob with an idle session should be admitted: %v", err)
	}
	if evs := drainEvents(t, reg, bobIdle); !containsType(evs, registry.EventRing) {
		t.Fatalf("bob's idle session should have been rung, got %v", evs)
	}
	if evs := drainEvents(t, reg, bobBusy); containsType(evs, registry.EventRing) {
		t.Fatal("bob's busy session must NOT be rung")
	}
}

// A callee is busy only when EVERY session is in a call. With bob's single
// session already in one, a new dial is refused.
func TestDial_BusyWhenAllSessionsInCalls(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	activeCall(t, calls, reg, "alice", "bob") // bob's only session is in a call
	daveSid := reg.Register("dave", "user", false, "LOBBY")

	if _, _, err := calls.Dial(daveSid, "dave", "bob"); !errors.Is(err, ErrBusy) {
		t.Fatalf("dialing a callee whose only session is in a call: want ErrBusy, got %v", err)
	}
}

// The account-level admission seam flagged in review: ONE ring per callee even though
// the ring fans out to multiple idle sessions. bob has TWO idle sessions; alice
// dials him (rings both); carol's concurrent dial must be refused and create no
// second call — otherwise two overlapping rings would clobber each session's
// single pending-call slot (findings 4/8). Two idle sessions is essential: with
// one, this would pass even if admission regressed to per-session.
func TestDial_OneRingPerCalleeAcrossIdleSessions(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	carolSid := reg.Register("carol", "user", false, "LOBBY")
	bob1 := reg.Register("bob", "user", false, "LOBBY")
	bob2 := reg.Register("bob", "user", false, "LOBBY")

	if _, _, err := calls.Dial(aliceSid, "alice", "bob"); err != nil {
		t.Fatalf("alice's dial should be admitted: %v", err)
	}
	// Both idle sessions were rung (fan-out).
	if evs := drainEvents(t, reg, bob1); !containsType(evs, registry.EventRing) {
		t.Fatalf("bob session 1 should have been rung, got %v", evs)
	}
	if evs := drainEvents(t, reg, bob2); !containsType(evs, registry.EventRing) {
		t.Fatalf("bob session 2 should have been rung, got %v", evs)
	}
	// carol's concurrent dial to bob is refused — one ring per callee account.
	if _, _, err := calls.Dial(carolSid, "carol", "bob"); !errors.Is(err, ErrBeingRung) {
		t.Fatalf("second concurrent dial to bob: want ErrBeingRung, got %v", err)
	}
	if n := len(calls.calls); n != 1 {
		t.Fatalf("want exactly 1 call (alice->bob), got %d", n)
	}
}

// First-answer-wins: bob's two sessions are both rung; the first to answer joins
// the call, the second is rejected with ErrAlreadyAnswered (not silently made a
// duplicate conference participant), and the losing session is sent a ring
// retract (EventAnswerElsewhere).
func TestAnswer_DoubleAnswerFirstWins(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	aliceSid := reg.Register("alice", "user", false, "LOBBY")
	bob1 := reg.Register("bob", "user", false, "LOBBY")
	bob2 := reg.Register("bob", "user", false, "LOBBY")

	call, _, err := calls.Dial(aliceSid, "alice", "bob")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	drainEvents(t, reg, bob2) // discard bob2's EventRing

	// bob1 answers — wins.
	if _, _, err := calls.Answer(call.ID, bob1, "bob"); err != nil {
		t.Fatalf("first answer should win: %v", err)
	}
	// The losing session bob2 is told the call was answered elsewhere.
	if evs := drainEvents(t, reg, bob2); !containsType(evs, registry.EventAnswerElsewhere) {
		t.Fatalf("losing session bob2 should receive EventAnswerElsewhere, got %v", evs)
	}
	// bob2 answers the now-active call anyway (races the retract) — rejected.
	if _, _, err := calls.Answer(call.ID, bob2, "bob"); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("second answer: want ErrAlreadyAnswered, got %v", err)
	}
	// Exactly two participants: alice + bob1. bob2 was never appended.
	if parts := calls.Participants(call.ID); len(parts) != 2 {
		t.Fatalf("want 2 participants (alice, bob1), got %v", parts)
	}
}

// Regression for the misattribution found in live testing: the pending-ring
// cancellation named whoever left the call LAST, not whoever started the ring.
//
// alice and bob are in a call; alice rings carol into it; alice drops, then bob
// drops. Alice's departure leaves bob behind, so the call isn't empty and no
// teardown runs. Bob's departure empties it and triggers the cleanup — which had
// only hangupLocked's departing-participant username in scope and used that. So
// carol was told "bob cancelled the call" about a ring bob had nothing to do
// with. The leave order is the whole point of the test: with bob leaving last,
// naming the departing participant and naming the inviter give different answers.
func TestHangup_PendingRingCancellationNamesInviterNotLastToLeave(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call, aliceSid, bobSid := activeCall(t, calls, reg, "alice", "bob")
	carolSid := reg.Register("carol", "user", false, "LOBBY")

	if err := calls.Add(call.ID, "alice", "carol"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	drainEvents(t, reg, carolSid) // discard the EventRing

	calls.HangupSession(aliceSid) // inviter leaves first — bob remains, no teardown
	calls.HangupSession(bobSid)   // last one out — this triggers the ring cleanup

	var cancel *registry.PhoneEvent
	for _, e := range drainEvents(t, reg, carolSid) {
		if e.Type == registry.EventHangup && e.Callee == "carol" {
			ev := e
			cancel = &ev
		}
	}
	if cancel == nil {
		t.Fatal("carol should have been told her pending ring was cancelled")
	}
	if cancel.Caller != "alice" {
		t.Fatalf("cancellation must name the inviter: want Caller=alice, got %q", cancel.Caller)
	}
}
