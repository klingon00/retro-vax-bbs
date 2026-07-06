package lobby

import (
	"testing"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
)

func TestWaitForPhoneEvent_DoneCloseReturnsNil(t *testing.T) {
	events := make(chan registry.PhoneEvent) // never sent to
	done := make(chan struct{})

	cmd := waitForPhoneEvent(events, done)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd for a non-nil channel pair")
	}

	// Closing done at teardown must unblock the receive and yield a nil Msg so
	// the goroutine exits instead of leaking on the never-closed notify channel.
	close(done)
	if msg := cmd(); msg != nil {
		t.Fatalf("expected nil Msg when done is closed, got %v", msg)
	}
}

func TestWaitForPhoneEvent_DeliversEvent(t *testing.T) {
	events := make(chan registry.PhoneEvent, 1)
	done := make(chan struct{})
	events <- registry.PhoneEvent{Type: registry.EventRing, Caller: "carol"}

	cmd := waitForPhoneEvent(events, done)
	msg, ok := cmd().(phoneRingMsg)
	if !ok {
		t.Fatalf("expected a phoneRingMsg, got %T", cmd())
	}
	if msg.event.Caller != "carol" {
		t.Fatalf("expected event from carol, got %q", msg.event.Caller)
	}
}

func TestWaitForPhoneEvent_NilGuards(t *testing.T) {
	// Both nil (user not connected): nil Cmd.
	if waitForPhoneEvent(nil, nil) != nil {
		t.Fatal("expected nil Cmd when both channels are nil")
	}
	// Defensive guard: a non-nil events channel with a nil done must ALSO return
	// a nil Cmd. Otherwise select would block forever on the nil done arm,
	// silently disabling the shutdown signal and reintroducing the leak.
	events := make(chan registry.PhoneEvent)
	if waitForPhoneEvent(events, nil) != nil {
		t.Fatal("expected nil Cmd when done is nil (defensive guard)")
	}
}
