package phone

import (
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
