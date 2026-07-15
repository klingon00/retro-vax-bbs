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

// dialAndAnswer sets up an active 2-party call between alice (caller) and bob
// (callee). Only bob needs to be registered — Dial requires the callee to be
// connected; the caller does not. Returns the call plus both participants.
func dialAndAnswer(t *testing.T) (*Calls, *Call, *Participant, *Participant) {
	t.Helper()
	reg := registry.New()
	reg.Register("bob", "user", false, "LOBBY")
	calls := NewCalls(reg)

	call, callerP, err := calls.Dial("alice", "bob")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_, calleeP, err := calls.Answer(call.ID, "bob") // active; also closes stopRing
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	return calls, call, callerP, calleeP
}

func TestHangup_ClosesDepartingParticipantOnly(t *testing.T) {
	calls, call, callerP, calleeP := dialAndAnswer(t)

	calls.Hangup(call.ID, "alice")

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

func TestHangupUser_RemovesFromActiveCallWithoutCallID(t *testing.T) {
	calls, call, callerP, _ := dialAndAnswer(t)

	// Mimic a dropped SSH session: session teardown knows the username but not
	// the callID, so it calls HangupUser.
	calls.HangupUser("alice")

	if !isChanClosed(callerP.IncomingChar) {
		t.Fatal("HangupUser should close the departed user's IncomingChar")
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

	calls.Hangup(call.ID, "alice") // removes + closes callerP
	calls.HangupUser("alice")      // no-op: alice already gone
	calls.Hangup(call.ID, "alice") // no-op: alice not a participant

	if !isChanClosed(callerP.IncomingChar) {
		t.Fatal("caller channel should remain closed after repeated hangups")
	}
}

func TestReject_ClosesCallerChannel(t *testing.T) {
	reg := registry.New()
	reg.Register("bob", "user", false, "LOBBY")
	calls := NewCalls(reg)

	call, callerP, err := calls.Dial("alice", "bob") // pending
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
// whom it registers. Unlike dialAndAnswer it registers the caller too, so the
// caller can also be a ring target in admission tests.
func activeCall(t *testing.T, calls *Calls, reg *registry.Registry, caller, callee string) *Call {
	t.Helper()
	reg.Register(caller, "user", false, "LOBBY")
	reg.Register(callee, "user", false, "LOBBY")
	call, _, err := calls.Dial(caller, callee)
	if err != nil {
		t.Fatalf("Dial(%s, %s): %v", caller, callee, err)
	}
	if _, _, err := calls.Answer(call.ID, callee); err != nil {
		t.Fatalf("Answer(%s): %v", callee, err)
	}
	return call
}

// Finding 6: a self-dial was admitted by every gate — it created a real pending
// call that rang the caller's own bell every 10s.
func TestDial_RejectsSelfCall(t *testing.T) {
	reg := registry.New()
	reg.Register("alice", "user", false, "LOBBY")
	calls := NewCalls(reg)

	_, _, err := calls.Dial("alice", "alice")
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
	reg.Register("alice", "user", false, "LOBBY")
	calls := NewCalls(reg)

	if _, _, err := calls.Dial("alice", "ALICE"); !errors.Is(err, ErrSelfCall) {
		t.Fatalf("Dial to self (mixed case): want ErrSelfCall, got %v", err)
	}
}

// Finding 6, ADD half: %ADD <self> rang you into your own call.
func TestAdd_RejectsSelfCall(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call := activeCall(t, calls, reg, "alice", "bob")

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
	activeCall(t, calls, reg, "alice", "bob")           // call A
	callB := activeCall(t, calls, reg, "carol", "dave") // call B

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
	reg.Register("carol", "user", false, "LOBBY")
	calls := NewCalls(reg)

	if _, _, err := calls.Dial("bob", "carol"); err != nil {
		t.Fatalf("first Dial: %v", err)
	}
	_, _, err := calls.Dial("alice", "carol")
	if !errors.Is(err, ErrBeingRung) {
		t.Fatalf("Dial of a callee mid-ring: want ErrBeingRung, got %v", err)
	}
}

// Finding 4: a DIAL and a conference ADD could target the same person at once,
// producing two ring goroutines; whichever call lost the race rang forever.
func TestDial_RejectsCalleeBeingAddedToConference(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call := activeCall(t, calls, reg, "alice", "bob")
	reg.Register("carol", "user", false, "LOBBY")

	if err := calls.Add(call.ID, "alice", "carol"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, _, err := calls.Dial("dave", "carol")
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
	call := activeCall(t, calls, reg, "alice", "bob")
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
// participant, who could not answer. Covered by the predicate's participant scan
// rather than a bespoke check.
func TestAdd_RejectsExistingParticipant(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	call := activeCall(t, calls, reg, "alice", "bob")

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
	call := activeCall(t, calls, reg, "alice", "bob")
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
	calls.Hangup(call.ID, "alice")
	calls.Hangup(call.ID, "bob")

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

// Identity granularity guard — asserts ADMISSION ONLY, and deliberately nothing
// more. Admission is account-level and the caller's own call membership is never
// consulted, so a dial from an account that already has another session in a call
// is not refused. If this goes red, the predicate has started enforcing
// one-call-per-account (finding 10) as a side effect.
//
// What this test does NOT assert — and must not be read as asserting — is that
// the admitted call then works. It does not: per-account event routing means the
// other session steals the EventAnswer and this one hangs up on the next
// keystroke (finding 11, found in live testing after this test was written and
// taken as evidence the capability worked). "The guard permits X" and "X works"
// are different claims; this test only makes the first. Proving the second needs
// a live two-session pass, not a Dial return value.
func TestDial_AdmitsCallerWhoseAccountIsAlreadyInACall(t *testing.T) {
	reg := registry.New()
	calls := NewCalls(reg)
	activeCall(t, calls, reg, "alice", "bob") // alice's other session is in a call
	reg.Register("carol", "user", false, "LOBBY")

	call, callerP, err := calls.Dial("alice", "carol")
	if err != nil {
		t.Fatalf("alice must still dial from a second session while in a call: %v", err)
	}
	if call == nil || callerP == nil {
		t.Fatal("Dial returned no call/participant despite a nil error")
	}
}

// drainEvents collects whatever is queued on a user's notify channel without
// blocking. The channel is buffered (size 8) and never closed, so a default
// branch is the only safe way to stop.
func drainEvents(t *testing.T, reg *registry.Registry, username string) []registry.PhoneEvent {
	t.Helper()
	ch, _ := reg.Events(username)
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
	call := activeCall(t, calls, reg, "alice", "bob")
	reg.Register("carol", "user", false, "LOBBY")

	if err := calls.Add(call.ID, "alice", "carol"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	drainEvents(t, reg, "carol") // discard the EventRing

	calls.HangupUser("alice") // inviter leaves first — bob remains, no teardown
	calls.HangupUser("bob")   // last one out — this triggers the ring cleanup

	var cancel *registry.PhoneEvent
	for _, e := range drainEvents(t, reg, "carol") {
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
