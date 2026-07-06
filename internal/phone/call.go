// Package phone implements the VAX-BBS Phone Facility.
package phone

import (
	"fmt"
	"sync"
	"time"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
)

const (
	RingInterval    = 10 * time.Second
	IncomingBufSize = 32
)

// CallState represents the lifecycle state of a call.
type CallState int

const (
	CallIdle    CallState = iota // in PHONE but not currently in a call
	CallPending                  // ringing, callee hasn't answered yet
	CallActive                   // connected, all participants can type
)

// CharEvent carries a rune and the sender's username through the
// per-participant incoming-character channel, so the recipient can
// attribute the character to the correct viewport. Without sender
// info, all remote chars look identical and get attributed to the
// wrong person in conference calls.
type CharEvent struct {
	R      rune
	Sender string
}

// Participant represents one person in a call.
type Participant struct {
	Username     string
	IncomingChar chan CharEvent // receives CharEvents typed by OTHER participants
}

// Call represents one phone call, pending or active.
type Call struct {
	ID           string
	State        CallState
	Caller       string         // who initiated the call
	Callee       string         // who is being called (pending state)
	participants []*Participant // ordered: caller first, then callees
	stopRing     chan struct{}  // close to stop the ring goroutine
}

// Calls is the process-wide call table. Thread-safe.
type Calls struct {
	mu          sync.Mutex
	calls       map[string]*Call
	pendingAdds map[string]chan struct{} // "callID:callee" → stop channel for ADD rings
	reg         *registry.Registry
}

// NewCalls creates an empty call table.
func NewCalls(reg *registry.Registry) *Calls {
	return &Calls{
		calls:       make(map[string]*Call),
		pendingAdds: make(map[string]chan struct{}),
		reg:         reg,
	}
}

// Dial initiates a call from caller to callee.
func (c *Calls) Dial(callerUsername, calleeUsername string) (*Call, *Participant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ch := c.reg.Notify(calleeUsername); ch == nil {
		return nil, nil, fmt.Errorf("%s is not connected", calleeUsername)
	}
	for _, call := range c.calls {
		if call.State == CallActive {
			for _, p := range call.participants {
				if p.Username == calleeUsername {
					return nil, nil, fmt.Errorf("%s is already in a call", calleeUsername)
				}
			}
		}
	}

	id := fmt.Sprintf("%s->%s@%d", callerUsername, calleeUsername, time.Now().UnixNano())

	callerP := &Participant{
		Username:     callerUsername,
		IncomingChar: make(chan CharEvent, IncomingBufSize),
	}

	call := &Call{
		ID:           id,
		State:        CallPending,
		Caller:       callerUsername,
		Callee:       calleeUsername,
		participants: []*Participant{callerP},
		stopRing:     make(chan struct{}),
	}
	c.calls[id] = call

	event := registry.PhoneEvent{
		Type:   registry.EventRing,
		CallID: id,
		Caller: callerUsername,
		Callee: calleeUsername,
	}
	c.sendEvent(calleeUsername, event)

	go func() {
		ticker := time.NewTicker(RingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.sendEvent(calleeUsername, event)
			case <-call.stopRing:
				return
			}
		}
	}()

	return call, callerP, nil
}

// Answer connects a callee to a call. Handles two cases:
//   - CallPending: standard answer — stops ringing, marks active, notifies caller.
//   - CallActive: conference join — adds participant, notifies everyone already in the call.
func (c *Calls) Answer(callID, calleeUsername string) (*Call, *Participant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		return nil, nil, fmt.Errorf("call %s not found", callID)
	}

	calleeP := &Participant{
		Username:     calleeUsername,
		IncomingChar: make(chan CharEvent, IncomingBufSize),
	}
	call.participants = append(call.participants, calleeP)

	if call.State == CallActive {
		// Conference join: stop the ADD ring goroutine if one is running,
		// then notify all existing participants that someone joined.
		key := callID + ":" + calleeUsername
		if stop, ok := c.pendingAdds[key]; ok {
			close(stop)
			delete(c.pendingAdds, key)
		}
		for _, p := range call.participants {
			if p.Username != calleeUsername {
				c.sendEvent(p.Username, registry.PhoneEvent{
					Type:   registry.EventAnswer,
					CallID: callID,
					Caller: call.Caller,
					Callee: calleeUsername,
				})
			}
		}
		return call, calleeP, nil
	}

	if call.State != CallPending {
		// Roll back the just-appended calleeP. No waitForChar goroutine is armed
		// until doAnswer succeeds (it returns on this error instead), so
		// calleeP.IncomingChar has no receiver to reap — dropping the slot lets
		// GC reclaim the channel; there is nothing to close.
		call.participants = call.participants[:len(call.participants)-1]
		return nil, nil, fmt.Errorf("call %s is in unexpected state", callID)
	}

	// Standard 2-party answer.
	close(call.stopRing)
	call.State = CallActive

	c.sendEvent(call.Caller, registry.PhoneEvent{
		Type:   registry.EventAnswer,
		CallID: callID,
		Caller: call.Caller,
		Callee: calleeUsername,
	})

	return call, calleeP, nil
}

// Reject cancels a pending call or declines a conference ADD invitation.
// Handles two cases:
//   - CallPending: standard 2-party rejection — stops the ring goroutine,
//     removes the call, notifies the caller.
//   - CallActive: conference ADD declined — stops the ADD ring goroutine
//     (in pendingAdds) without touching the main call; notifies all active
//     participants. Closing the main call's stopRing here would panic since
//     Answer already closed it when the call became active.
func (c *Calls) Reject(callID, calleeUsername string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		return fmt.Errorf("call %s not found", callID)
	}

	if call.State == CallActive {
		// Conference ADD rejection. Stop only the ADD ring goroutine.
		key := callID + ":" + calleeUsername
		if stop, ok := c.pendingAdds[key]; ok {
			close(stop)
			delete(c.pendingAdds, key)
		}
		// Notify all active participants that the invite was declined.
		for _, p := range call.participants {
			c.sendEvent(p.Username, registry.PhoneEvent{
				Type:   registry.EventReject,
				CallID: callID,
				Callee: calleeUsername, // who declined
			})
		}
		return nil
	}

	if call.State != CallPending {
		return fmt.Errorf("call %s is not in a rejectable state", callID)
	}

	// Standard 2-party pending call rejection. The caller leaves this path via
	// goIdle in its EventReject handler, not via Hangup, so close its
	// IncomingChar here to reap the caller's waitForChar goroutine. In a pending
	// call the caller is the only participant (the callee never created one —
	// that happens on Answer), and the call is deleted just below, so no later
	// BroadcastChar can target the closed channel. Safe under c.mu.
	close(call.stopRing)
	for _, p := range call.participants {
		close(p.IncomingChar)
	}
	delete(c.calls, callID)
	c.sendEvent(call.Caller, registry.PhoneEvent{
		Type:   registry.EventReject,
		CallID: callID,
		Caller: call.Caller,
		Callee: calleeUsername,
	})
	return nil
}

// Hangup removes a participant from a call by call ID. If the last participant
// leaves, the call is torn down and remaining participants are notified. Safe
// to call for a participant who has already left — hangupLocked is a no-op then.
func (c *Calls) Hangup(callID, username string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		return
	}
	c.hangupLocked(call, username)
}

// HangupUser removes username from whatever call they are currently in,
// regardless of call ID. Called from the session-teardown path: a dropped SSH
// connection never runs a HANGUP/EXIT command, so without this a mid-call
// disconnect would leave a phantom participant in the call and leak the
// departed session's waitForChar goroutine (its IncomingChar would never be
// closed). A user is only ever in one call at a time, so the first match is the
// only one; a no-op if they are in no call.
func (c *Calls) HangupUser(username string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, call := range c.calls {
		for _, p := range call.participants {
			if p.Username == username {
				c.hangupLocked(call, username)
				return
			}
		}
	}
}

// hangupLocked removes username from call: it closes their IncomingChar (waking
// their waitForChar goroutine, which returns on the !ok receive), notifies the
// remaining participants, and tears the call down ONLY if it is now empty. The
// caller must hold c.mu — the same lock BroadcastChar holds — so closing the
// channel here cannot race an in-flight send, and once the participant is out
// of the slice no later BroadcastChar can target the closed channel.
//
// Idempotent per participant: if username is not in call.participants (e.g. a
// clean HANGUP/EXIT already removed them and session-teardown HangupUser fires
// second), idx stays -1 and it returns without closing anything — so
// IncomingChar is never double-closed.
func (c *Calls) hangupLocked(call *Call, username string) {
	idx := -1
	for i, p := range call.participants {
		if p.Username == username {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	removed := call.participants[idx]
	call.participants = append(call.participants[:idx], call.participants[idx+1:]...)
	close(removed.IncomingChar)

	event := registry.PhoneEvent{
		Type:   registry.EventHangup,
		CallID: call.ID,
		Caller: username,
	}
	for _, p := range call.participants {
		c.sendEvent(p.Username, event)
	}

	if len(call.participants) == 0 {
		if call.State == CallPending {
			close(call.stopRing)
			if call.Callee != "" {
				c.sendEvent(call.Callee, registry.PhoneEvent{
					Type:   registry.EventHangup,
					CallID: call.ID,
					Caller: username,
					Callee: call.Callee,
				})
			}
		}
		delete(c.calls, call.ID)
	}
}

// Add invites an additional participant into an active call (conference).
// Rings the callee every RingInterval until they answer or the call ends.
// Also sends EventRinging to all current call participants (except the
// callee) so they see who is being rung.
func (c *Calls) Add(callID, callerUsername, calleeUsername string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		return fmt.Errorf("call %s not found", callID)
	}
	if call.State != CallActive {
		return fmt.Errorf("call %s is not active", callID)
	}
	if ch := c.reg.Notify(calleeUsername); ch == nil {
		return fmt.Errorf("%s is not connected", calleeUsername)
	}

	// Stop any existing pending-add ring for this person on this call.
	key := callID + ":" + calleeUsername
	if existing, ok := c.pendingAdds[key]; ok {
		close(existing)
	}
	stopRing := make(chan struct{})
	c.pendingAdds[key] = stopRing

	ringEvent := registry.PhoneEvent{
		Type:   registry.EventRing,
		CallID: callID,
		Caller: callerUsername,
		Callee: calleeUsername,
	}
	ringingEvent := registry.PhoneEvent{
		Type:   registry.EventRinging,
		CallID: callID,
		Caller: callerUsername,
		Callee: calleeUsername,
	}

	// Ring the callee immediately.
	c.sendEvent(calleeUsername, ringEvent)

	// Notify all other current participants (not the callee, not the caller)
	// that a ring is in progress so they can see who is being added.
	for _, p := range call.participants {
		if p.Username != callerUsername {
			c.sendEvent(p.Username, ringingEvent)
		}
	}

	go func() {
		ticker := time.NewTicker(RingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.sendEvent(calleeUsername, ringEvent)
				// Re-notify participants on each ring tick.
				c.mu.Lock()
				if call2, ok := c.calls[callID]; ok {
					for _, p := range call2.participants {
						if p.Username != callerUsername {
							c.sendEvent(p.Username, ringingEvent)
						}
					}
				}
				c.mu.Unlock()
			case <-stopRing:
				return
			}
		}
	}()

	return nil
}

// CancelAdd stops a pending conference ring initiated by callerUsername
// for calleeUsername. Notifies the callee that the ring was cancelled.
func (c *Calls) CancelAdd(callID, calleeUsername, callerUsername string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := callID + ":" + calleeUsername
	if stop, ok := c.pendingAdds[key]; ok {
		close(stop)
		delete(c.pendingAdds, key)
	}

	// Tell the callee the ring was cancelled so they can clear their prompt.
	c.sendEvent(calleeUsername, registry.PhoneEvent{
		Type:   registry.EventHangup,
		CallID: callID,
		Caller: callerUsername,
		Callee: calleeUsername, // non-empty = ring cancelled, not a departure
	})

	// Tell other call participants the ring was cancelled so they clear the
	// "X is ringing Y" notification. event.Callee non-empty distinguishes
	// this from a normal participant departure in the receiver's handler.
	call, ok := c.calls[callID]
	if !ok {
		return
	}
	for _, p := range call.participants {
		if p.Username != callerUsername {
			c.sendEvent(p.Username, registry.PhoneEvent{
				Type:   registry.EventHangup,
				CallID: callID,
				Caller: callerUsername,
				Callee: calleeUsername,
			})
		}
	}
}

// BroadcastChar sends a rune typed by sender to all other participants.
// CharEvent carries the sender's username so recipients can attribute
// the character to the correct viewport.
func (c *Calls) BroadcastChar(callID, senderUsername string, r rune) {
	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		return
	}
	for _, p := range call.participants {
		if p.Username != senderUsername {
			select {
			case p.IncomingChar <- CharEvent{R: r, Sender: senderUsername}:
			default:
			}
		}
	}
}

// Participants returns a snapshot of participant usernames for the given call.
func (c *Calls) Participants(callID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	call, ok := c.calls[callID]
	if !ok {
		return nil
	}
	names := make([]string, len(call.participants))
	for i, p := range call.participants {
		names[i] = p.Username
	}
	return names
}

func (c *Calls) sendEvent(username string, event registry.PhoneEvent) {
	ch := c.reg.Notify(username)
	if ch == nil {
		return
	}
	select {
	case ch <- event:
	default:
	}
}
