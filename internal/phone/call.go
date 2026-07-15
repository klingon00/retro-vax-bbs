// Package phone implements the VAX-BBS Phone Facility.
package phone

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
)

// Admission errors. Dial and Add both return these (wrapped with the target's
// username where one is relevant) so callers can classify with errors.Is and
// render a message with ErrorMessage, rather than string-matching.
var (
	// ErrSelfCall means caller and callee are the same account.
	ErrSelfCall = errors.New("you cannot phone yourself")
	// ErrNotConnected means the callee has no active session.
	ErrNotConnected = errors.New("is not connected")
	// ErrBusy means the callee is already a participant in a call.
	ErrBusy = errors.New("is already in a call")
	// ErrBeingRung means the callee already has an unanswered ring pending —
	// distinct from ErrBusy so the message can say so accurately.
	ErrBeingRung = errors.New("is already being called")
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

// addKey identifies one outstanding conference ring: which call invited whom.
// A struct key rather than a "callID:callee" string so both the admission scan
// (match on callee) and hangupLocked's teardown (match on callID) compare a
// field instead of parsing a composite string.
type addKey struct {
	callID string
	callee string
}

// pendingRing is one outstanding conference ring: the stop channel for its
// goroutine, plus who started it. The inviter is retained because addKey records
// who was rung and from which call, but not who did the ringing — and teardown
// has to attribute the cancellation to someone. Without it, hangupLocked has
// only its own departing-participant argument in scope, which named whoever left
// the call last and told the callee a bystander had cancelled a ring they never
// placed.
type pendingRing struct {
	stop    chan struct{}
	inviter string
}

// Calls is the process-wide call table. Thread-safe.
type Calls struct {
	mu          sync.Mutex
	calls       map[string]*Call
	pendingAdds map[addKey]*pendingRing // outstanding ADD rings
	reg         *registry.Registry
}

// NewCalls creates an empty call table.
func NewCalls(reg *registry.Registry) *Calls {
	return &Calls{
		calls:       make(map[string]*Call),
		pendingAdds: make(map[addKey]*pendingRing),
		reg:         reg,
	}
}

// admitLocked reports why caller may not place a ring to callee, or nil if the
// ring is allowed. It is the single admission rule for Dial and Add — the only
// two entry points that start a ring — so a rule added here covers both with no
// second list to hand-sync. c.mu must be held.
//
// Identity is account-level (username) throughout, matching the registry, which
// is keyed by username with one notify channel shared by all of an account's
// sessions. The caller's own call membership is deliberately NOT consulted:
// checking it would refuse a dial from an account that merely has *another*
// session in a call, which multi-session support explicitly allows. That
// omission is also why "one user, one call" stays unenforced — see finding 10
// of docs/audits/audit-2026-07-13-phone-call-admission.md.
func (c *Calls) admitLocked(caller, callee string) error {
	// EqualFold: the registry is exact-match keyed, so `DIAL ALICE` typed by
	// alice would otherwise fall through to "ALICE is not connected". Naming
	// yourself in any case is a self-call, and saying so is more useful.
	if strings.EqualFold(caller, callee) {
		return ErrSelfCall
	}
	if ch := c.reg.Notify(callee); ch == nil {
		return fmt.Errorf("%s %w", callee, ErrNotConnected)
	}
	for _, call := range c.calls {
		// Participant of any call, pending or active. A pending call's only
		// participant is its caller, so this also covers a callee who is
		// mid-ring on their own outbound call.
		for _, p := range call.participants {
			if p.Username == callee {
				return fmt.Errorf("%s %w", callee, ErrBusy)
			}
		}
		// Callee of a pending call: already being rung by someone else's DIAL.
		if call.State == CallPending && call.Callee == callee {
			return fmt.Errorf("%s %w", callee, ErrBeingRung)
		}
	}
	// Already being rung into a conference by an ADD — from any call, including
	// this one. One ring per callee at a time, with no per-call exception.
	for key := range c.pendingAdds {
		if key.callee == callee {
			return fmt.Errorf("%s %w", callee, ErrBeingRung)
		}
	}
	return nil
}

// ErrorMessage renders an admission error as a VAX/VMS-style facility message.
// Both the lobby (phoneDialCommand) and PHONE (doDial/doAddToCall) route errors
// through here so the error→ident mapping has one definition.
func ErrorMessage(err error, target string) string {
	switch {
	case errors.Is(err, ErrSelfCall):
		return "%PHONE-E-SELF, You cannot phone yourself."
	case errors.Is(err, ErrBusy):
		return fmt.Sprintf("%%PHONE-E-BUSY, %s is already in a call.", target)
	case errors.Is(err, ErrBeingRung):
		return fmt.Sprintf("%%PHONE-E-BUSY, %s is already being called.", target)
	case errors.Is(err, ErrNotConnected):
		return fmt.Sprintf("%%PHONE-E-NOLOGIN, %s is not connected.", target)
	default:
		return fmt.Sprintf("%%PHONE-E-NOLOGIN, %v", err)
	}
}

// Dial initiates a call from caller to callee.
func (c *Calls) Dial(callerUsername, calleeUsername string) (*Call, *Participant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.admitLocked(callerUsername, calleeUsername); err != nil {
		return nil, nil, err
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
		key := addKey{callID: callID, callee: calleeUsername}
		if ring, ok := c.pendingAdds[key]; ok {
			close(ring.stop)
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
		key := addKey{callID: callID, callee: calleeUsername}
		if ring, ok := c.pendingAdds[key]; ok {
			close(ring.stop)
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
		// Stop any conference rings this call still had outstanding. Their
		// goroutines key off stopRing alone, so without this they outlive the
		// call and ring their target every RingInterval forever — for a call
		// that no longer exists and can never be answered.
		for k, ring := range c.pendingAdds {
			if k.callID == call.ID {
				close(ring.stop)
				delete(c.pendingAdds, k)
				c.sendEvent(k.callee, registry.PhoneEvent{
					Type:   registry.EventHangup,
					CallID: call.ID,
					// The inviter, NOT `username`: username is whoever happened
					// to leave last, who may have had nothing to do with this
					// ring. The callee is told "<Caller> cancelled the call",
					// so naming the wrong person is a visible lie.
					Caller: ring.inviter,
					Callee: k.callee, // non-empty = ring cancelled, not a departure
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
	if err := c.admitLocked(callerUsername, calleeUsername); err != nil {
		return err
	}

	// No "stop the existing ring for this person" step: admitLocked refuses a
	// callee who already has a ring outstanding from any call, so reaching here
	// means there is nothing to stop.
	key := addKey{callID: callID, callee: calleeUsername}
	stopRing := make(chan struct{})
	c.pendingAdds[key] = &pendingRing{stop: stopRing, inviter: callerUsername}

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
				// Ring under the lock, and only while the call still exists.
				// hangupLocked closes stopRing on teardown, so this is belt and
				// suspenders — but it means no future path that drops a call
				// can leave this goroutine ringing someone into a call that is
				// gone. The ring itself used to sit outside this guard, which
				// only ever protected the participant re-notify below.
				c.mu.Lock()
				call2, ok := c.calls[callID]
				if !ok {
					c.mu.Unlock()
					return
				}
				c.sendEvent(calleeUsername, ringEvent)
				for _, p := range call2.participants {
					if p.Username != callerUsername {
						c.sendEvent(p.Username, ringingEvent)
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

	key := addKey{callID: callID, callee: calleeUsername}
	if ring, ok := c.pendingAdds[key]; ok {
		close(ring.stop)
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
