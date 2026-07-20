// Package phone implements the VAX-BBS Phone Facility.
package phone

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/klingon00/retro-vax-bbs/internal/debuglog"
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
	// ErrAlreadyAnswered means a pending call was already answered by another
	// session of the callee's account. Returned by Answer to the losing session
	// under first-answer-wins, so it can show a specific note rather than
	// mistaking the now-active call for a conference it may join.
	ErrAlreadyAnswered = errors.New("call was already answered")
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
	Username string
	// SessionID identifies the specific SSH session that is in this call, so
	// call events route to that session and not to a sibling session of the
	// same account. A call membership is per-session, not per-account.
	SessionID    string
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
// Admission is ACCOUNT-LEVEL; only the ring fan-out (in Dial/Add) is per-session.
// This split is load-bearing. The being-rung checks below key on the callee
// *username* and run BEFORE the session-level busy check, so at most one inbound
// ring per callee account exists at a time — which is what keeps "one ring per
// callee" true and stops a second concurrent caller from clobbering a rung
// session's single pending-call slot (findings 4/8). The busy check, by
// contrast, is per-session: the callee is "busy" only when EVERY one of their
// sessions is already in a call; if any session is idle, the ring is admitted
// and fans out to the idle one(s), enforcing one-call-per-session on the callee.
//
// The caller's own call membership is deliberately NOT consulted: a Dial only
// ever originates from an idle session (an in-call DIAL routes to Add instead),
// so "one call per session" holds on the caller side structurally, without a
// check here.
func (c *Calls) admitLocked(caller, callee string) error {
	// EqualFold: the registry is exact-match keyed, so `DIAL ALICE` typed by
	// alice would otherwise fall through to "ALICE is not connected". Naming
	// yourself in any case is a self-call, and saying so is more useful.
	if strings.EqualFold(caller, callee) {
		return ErrSelfCall
	}
	if !c.reg.Connected(callee) {
		return fmt.Errorf("%s %w", callee, ErrNotConnected)
	}
	// Being-rung (account-level, checked first, one ring per callee): the callee
	// is the target of a pending DIAL, or has an outstanding conference ADD ring
	// — from any call, with no per-call exception.
	for _, call := range c.calls {
		if call.State == CallPending && call.Callee == callee {
			return fmt.Errorf("%s %w", callee, ErrBeingRung)
		}
	}
	for key := range c.pendingAdds {
		if key.callee == callee {
			return fmt.Errorf("%s %w", callee, ErrBeingRung)
		}
	}
	// Busy (per-session): admit only if the callee has at least one ringable
	// (not-in-a-call) session. If all of their sessions are in calls, they are
	// busy. A callee mid-outbound-dial is a participant of their own pending
	// call, so that session is not ringable — covered here, no special case.
	if len(c.ringableSessionsLocked(callee)) == 0 {
		return fmt.Errorf("%s %w", callee, ErrBusy)
	}
	return nil
}

// ringableSessionsLocked returns the session IDs of username that are not
// currently a participant in any call — the sessions a new ring should reach.
// This is a delivery/fan-out helper and the input to the ErrBusy check; it is
// NOT the being-rung admission gate (that stays account-level in admitLocked).
// c.mu must be held.
func (c *Calls) ringableSessionsLocked(username string) []string {
	var out []string
	for _, sid := range c.reg.SessionsOf(username) {
		if !c.sessionInCallLocked(sid) {
			out = append(out, sid)
		}
	}
	return out
}

// sessionInCallLocked reports whether the given session is already a participant
// in any call, pending or active. c.mu must be held.
func (c *Calls) sessionInCallLocked(sid string) bool {
	for _, call := range c.calls {
		for _, p := range call.participants {
			if p.SessionID == sid {
				return true
			}
		}
	}
	return false
}

// ringLocked fans a ring out to every RINGABLE session of callee — those not
// already in a call. Both the initial ring and each re-ring tick go through
// here, and ringableSessionsLocked recomputes against the live call table each
// time, so a session that went busy since the last ring is skipped and a
// session that connected since is included. c.mu must be held.
// Returns the sessions it rang so callers can log the actual fan-out set
// without rescanning; callers that don't need it may ignore the result.
func (c *Calls) ringLocked(callee string, event registry.PhoneEvent) []string {
	sids := c.ringableSessionsLocked(callee)
	for _, sid := range sids {
		c.reg.SendToSession(sid, event)
	}
	return sids
}

// notifyAccountLocked sends event to EVERY session of username, regardless of
// call membership. Used for CallID-tagged ring retracts/cancels aimed at a
// callee who has not yet become a participant: each session filters on the
// CallID and ignores an event for a ring it is not showing, so an unfiltered
// fan-out is harmless and reaches whichever sessions were actually rung. c.mu
// must be held. (Unlike the old shared-channel design, a stray delivery here
// cannot steal an event from the intended session — each session has its own
// queue.)
func (c *Calls) notifyAccountLocked(username string, event registry.PhoneEvent) {
	for _, sid := range c.reg.SessionsOf(username) {
		c.reg.SendToSession(sid, event)
	}
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

// Dial initiates a call from caller to callee. callerSessionID identifies the
// specific session placing the call, so the eventual EventAnswer routes back to
// it and not to a sibling session of the caller's account.
func (c *Calls) Dial(callerSessionID, callerUsername, calleeUsername string) (*Call, *Participant, error) {
	// Deferred emit. This defer is registered BEFORE the unlock defer below,
	// and defers run LIFO, so it runs AFTER the unlock — the log write lands
	// outside c.mu. The order looks backwards (the line you want emitted last
	// is registered first) and reads like something to tidy up; it is load-
	// bearing. Do not move it below the Lock.
	//
	// This keeps THIS function's own log write out of the critical section. It
	// does not make the whole path lock-free: ringLocked below calls
	// registry.SendToSession while c.mu is still held, and that logs per
	// delivery. With PHONE_DEBUG_LOG unset none of it costs anything; with it
	// set, the fan-out lines do widen the hold. Accepted deliberately — see the
	// note in the Logging section of docs/open-questions.md.
	var logLine string
	defer func() {
		if logLine != "" {
			debuglog.Logf("%s", logLine)
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.admitLocked(callerUsername, calleeUsername); err != nil {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("dial REFUSED caller=%s session=%s -> callee=%s: %v",
				callerUsername, callerSessionID, calleeUsername, err)
		}
		return nil, nil, err
	}

	id := fmt.Sprintf("%s->%s@%d", callerUsername, calleeUsername, time.Now().UnixNano())

	callerP := &Participant{
		Username:     callerUsername,
		SessionID:    callerSessionID,
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
	// Ring every idle session of the callee account (first-answer-wins).
	rang := c.ringLocked(calleeUsername, event)
	if debuglog.Enabled() {
		logLine = fmt.Sprintf("dial ADMITTED caller=%s session=%s -> callee=%s call=%s rang=%v",
			callerUsername, callerSessionID, calleeUsername, id, rang)
	}

	go func() {
		ticker := time.NewTicker(RingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Re-ring under the lock, only while the call still exists, and
				// recompute which sessions are ringable — a session may have
				// gone busy, or a new one connected, since the last ring. Same
				// locked-and-guarded shape as Add's re-ring goroutine.
				c.mu.Lock()
				if _, ok := c.calls[id]; !ok {
					c.mu.Unlock()
					return
				}
				c.ringLocked(calleeUsername, event)
				c.mu.Unlock()
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
func (c *Calls) Answer(callID, calleeSessionID, calleeUsername string) (*Call, *Participant, error) {
	// Same LIFO-deferred emit as Dial, and safe for the same reason: Answer's
	// lock shape is identical — a single Lock with a single deferred Unlock as
	// the first two statements — so registering this defer above them still
	// puts the emit after the release. Verify that shape before copying this
	// into a function that locks differently.
	var logLine string
	defer func() {
		if logLine != "" {
			debuglog.Logf("%s", logLine)
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("answer FAILED callee=%s session=%s call=%s: no such call",
				calleeUsername, calleeSessionID, callID)
		}
		return nil, nil, fmt.Errorf("call %s not found", callID)
	}

	if call.State == CallActive {
		// An active call may only be joined via a genuine conference ADD invite,
		// proven by a matching pendingAdds entry. Without one, this is a SECOND
		// session of the original callee racing to answer a 2-party call another
		// session already answered — first-answer-wins, so reject it. Checked
		// before any participant is created, so a rejected answer leaves no trace.
		key := addKey{callID: callID, callee: calleeUsername}
		ring, invited := c.pendingAdds[key]
		if !invited {
			if debuglog.Enabled() {
				logLine = fmt.Sprintf("answer REJECTED callee=%s session=%s call=%s: ErrAlreadyAnswered (no ADD invite — a sibling session already answered)",
					calleeUsername, calleeSessionID, callID)
			}
			return nil, nil, ErrAlreadyAnswered
		}
		// Capture what the log line needs BEFORE the mutations below. delete
		// removes the map entry (the local pointer keeps the value alive, so
		// reading through it later works — but it reads as a use-after-delete
		// to a reviewer), and the participant append changes the count we want
		// to report. Same rule as Dial: bind the values the deferred emit
		// references at the point they are still true.
		inviter := ring.inviter
		close(ring.stop)
		delete(c.pendingAdds, key)

		calleeP := &Participant{
			Username:     calleeUsername,
			SessionID:    calleeSessionID,
			IncomingChar: make(chan CharEvent, IncomingBufSize),
		}
		call.participants = append(call.participants, calleeP)

		// Notify every existing participant (by their session) that someone
		// joined — all except the session that just joined.
		for _, p := range call.participants {
			if p.SessionID != calleeSessionID {
				c.reg.SendToSession(p.SessionID, registry.PhoneEvent{
					Type:   registry.EventAnswer,
					CallID: callID,
					Caller: call.Caller,
					Callee: calleeUsername,
				})
			}
		}
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("answer JOINED callee=%s session=%s call=%s inviter=%s participants=%d (conference)",
				calleeUsername, calleeSessionID, callID, inviter, len(call.participants))
		}
		return call, calleeP, nil
	}

	if call.State != CallPending {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("answer FAILED callee=%s session=%s call=%s: unexpected state %d",
				calleeUsername, calleeSessionID, callID, int(call.State))
		}
		return nil, nil, fmt.Errorf("call %s is in unexpected state", callID)
	}

	// Standard 2-party answer. The caller is the sole existing participant
	// (Dial created it; nothing appends to a pending call until now).
	callerP := call.participants[0]

	calleeP := &Participant{
		Username:     calleeUsername,
		SessionID:    calleeSessionID,
		IncomingChar: make(chan CharEvent, IncomingBufSize),
	}
	call.participants = append(call.participants, calleeP)

	close(call.stopRing)
	call.State = CallActive

	// Tell the caller's specific session the call is now live.
	c.reg.SendToSession(callerP.SessionID, registry.PhoneEvent{
		Type:   registry.EventAnswer,
		CallID: callID,
		Caller: call.Caller,
		Callee: calleeUsername,
	})

	// Retract the ring on the callee's OTHER sessions — they were all rung; this
	// one won. A distinct EventAnswerElsewhere (not EventHangup) so the losing
	// sessions can say "answered on another session" rather than falsely naming
	// the caller as having cancelled — the payload is otherwise identical to a
	// genuine caller-cancel. CallID-tagged so each clears only the ring it is
	// showing. The winning session is excluded (it goes active via its own path).
	// retracted is collected as we go rather than recomputed: it is the exact
	// set of sibling sessions that lost the race, which is the single most
	// useful fact this function can record for a finding-11 style fault. No
	// inviter is named here — a direct 2-party answer has no ADD invite, so
	// unlike the conference-join branch above there is nobody to attribute.
	var retracted []string
	for _, sid := range c.reg.SessionsOf(calleeUsername) {
		if sid != calleeSessionID {
			retracted = append(retracted, sid)
			c.reg.SendToSession(sid, registry.PhoneEvent{
				Type:   registry.EventAnswerElsewhere,
				CallID: callID,
				Caller: call.Caller,
				Callee: calleeUsername,
			})
		}
	}

	if debuglog.Enabled() {
		logLine = fmt.Sprintf("answer ACCEPTED callee=%s session=%s call=%s caller=%s caller-session=%s retracted=%v",
			calleeUsername, calleeSessionID, callID, call.Caller, callerP.SessionID, retracted)
	}
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
		// Notify all active participants (each by their session) that the invite
		// was declined.
		for _, p := range call.participants {
			c.reg.SendToSession(p.SessionID, registry.PhoneEvent{
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
	callerP := call.participants[0]
	close(call.stopRing)
	for _, p := range call.participants {
		close(p.IncomingChar)
	}
	delete(c.calls, callID)
	c.reg.SendToSession(callerP.SessionID, registry.PhoneEvent{
		Type:   registry.EventReject,
		CallID: callID,
		Caller: call.Caller,
		Callee: calleeUsername,
	})
	return nil
}

// Hangup removes the session identified by sessionID from a call by call ID. If
// the last participant leaves, the call is torn down and remaining participants
// are notified. Safe to call for a session that has already left — hangupLocked
// is a no-op then.
func (c *Calls) Hangup(callID, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		return
	}
	c.hangupLocked(call, sessionID)
}

// HangupSession removes the session identified by sessionID from whatever call
// it is currently in, regardless of call ID. Called from the session-teardown
// path: a dropped SSH connection never runs a HANGUP/EXIT command, so without
// this a mid-call disconnect would leave a phantom participant in the call and
// leak the departed session's waitForChar goroutine (its IncomingChar would
// never be closed). Keying on sessionID (not username) is essential for
// multi-session accounts: a dropped session must tear down ITS call, never a
// sibling session's. A session is only ever in one call at a time, so the first
// match is the only one; a no-op if it is in no call.
func (c *Calls) HangupSession(sessionID string) {
	// Same LIFO-deferred emit as Dial/Answer/Add; same lock shape, so the
	// ordering transplants. Every invocation sets exactly one logLine — a
	// teardown or a miss — so with logging on a session departure is never
	// silent. That is the point of instrumenting this function at all: from
	// the outside, a HangupSession that tore a call down and one that matched
	// nothing are indistinguishable, and telling them apart is the difference
	// between "the drop was handled" and "the drop had nothing to handle".
	var logLine string
	defer func() {
		if logLine != "" {
			debuglog.Logf("%s", logLine)
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, call := range c.calls {
		for _, p := range call.participants {
			if p.SessionID == sessionID {
				// Bound before hangupLocked runs: it removes this participant
				// and may tear the call down entirely, after which neither the
				// username nor the participant count is recoverable.
				if debuglog.Enabled() {
					logLine = fmt.Sprintf("hangup-session session=%s user=%s call=%s: tearing down (participants before=%d)",
						sessionID, p.Username, call.ID, len(call.participants))
				}
				c.hangupLocked(call, sessionID)
				return
			}
		}
	}

	// Fell through every call and every participant: this session held no call.
	// Unconditionally reached whenever the loops find no match — there is no
	// other exit from this function — so a session that was in nothing still
	// leaves a line, and the absence of a "tearing down" entry is never
	// ambiguous between "matched nothing" and "was never called at all".
	if debuglog.Enabled() {
		logLine = fmt.Sprintf("hangup-session session=%s: matched no call (session was not a participant in anything)",
			sessionID)
	}
}

// hangupLocked removes username from call: it closes their IncomingChar (waking
// their waitForChar goroutine, which returns on the !ok receive), notifies the
// remaining participants, and tears the call down ONLY if it is now empty. The
// caller must hold c.mu — the same lock BroadcastChar holds — so closing the
// channel here cannot race an in-flight send, and once the participant is out
// of the slice no later BroadcastChar can target the closed channel.
//
// Idempotent per session: if sessionID is not in call.participants (e.g. a
// clean HANGUP/EXIT already removed it and session-teardown HangupSession fires
// second), idx stays -1 and it returns without closing anything — so
// IncomingChar is never double-closed.
func (c *Calls) hangupLocked(call *Call, sessionID string) {
	idx := -1
	for i, p := range call.participants {
		if p.SessionID == sessionID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	removed := call.participants[idx]
	// The departing participant's username, for the "<who> has left" field. The
	// lookup is by session, but the event names a person.
	username := removed.Username
	call.participants = append(call.participants[:idx], call.participants[idx+1:]...)
	close(removed.IncomingChar)

	event := registry.PhoneEvent{
		Type:   registry.EventHangup,
		CallID: call.ID,
		Caller: username,
	}
	for _, p := range call.participants {
		c.reg.SendToSession(p.SessionID, event)
	}

	if len(call.participants) == 0 {
		if call.State == CallPending {
			close(call.stopRing)
			if call.Callee != "" {
				// The callee is being rung but is not yet a participant, so fan
				// out to their sessions; each clears the ring it is showing by
				// CallID.
				c.notifyAccountLocked(call.Callee, registry.PhoneEvent{
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
				c.notifyAccountLocked(k.callee, registry.PhoneEvent{
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
	// Same LIFO-deferred emit as Dial and Answer; same lock shape (one Lock, one
	// deferred Unlock, first two statements), so the ordering transplants.
	var logLine string
	defer func() {
		if logLine != "" {
			debuglog.Logf("%s", logLine)
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	call, ok := c.calls[callID]
	if !ok {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("add FAILED inviter=%s -> callee=%s call=%s: no such call",
				callerUsername, calleeUsername, callID)
		}
		return fmt.Errorf("call %s not found", callID)
	}
	if call.State != CallActive {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("add FAILED inviter=%s -> callee=%s call=%s: call not active (state %d)",
				callerUsername, calleeUsername, callID, int(call.State))
		}
		return fmt.Errorf("call %s is not active", callID)
	}
	// Same admitLocked as Dial — one predicate, two entry points (the fix for
	// audit findings 1-8). The refusal reasons are therefore identical to a
	// Dial's, which is why this line's format matches "dial REFUSED".
	if err := c.admitLocked(callerUsername, calleeUsername); err != nil {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("add REFUSED inviter=%s -> callee=%s call=%s: %v",
				callerUsername, calleeUsername, callID, err)
		}
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

	// Ring every ringable session of the callee immediately.
	rang := c.ringLocked(calleeUsername, ringEvent)
	if debuglog.Enabled() {
		// Recorded here rather than after the participant loop below: rang is
		// the ADD ring's fan-out set and is what a stale-ring investigation
		// needs. The pendingAdds entry keyed above is what hangupLocked later
		// cancels — naming the inviter here is what makes a finding-12 style
		// misattribution visible in the log rather than only on a terminal.
		logLine = fmt.Sprintf("add ADMITTED inviter=%s -> callee=%s call=%s rang=%v",
			callerUsername, calleeUsername, callID, rang)
	}

	// Notify all other current participants (not the callee, not the caller)
	// that a ring is in progress so they can see who is being added. Skipping by
	// username excludes all of the inviter's sessions, which is intended — they
	// initiated the ring.
	for _, p := range call.participants {
		if p.Username != callerUsername {
			c.reg.SendToSession(p.SessionID, ringingEvent)
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
				c.ringLocked(calleeUsername, ringEvent)
				for _, p := range call2.participants {
					if p.Username != callerUsername {
						c.reg.SendToSession(p.SessionID, ringingEvent)
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

	// Tell the callee the ring was cancelled so they can clear their prompt. The
	// callee is being rung but is not yet a participant, so fan out to their
	// sessions; each clears the ring it is showing by CallID.
	c.notifyAccountLocked(calleeUsername, registry.PhoneEvent{
		Type:   registry.EventHangup,
		CallID: callID,
		Caller: callerUsername,
		Callee: calleeUsername, // non-empty = ring cancelled, not a departure
	})

	// Tell other call participants (each by their session) the ring was
	// cancelled so they clear the "X is ringing Y" notification. event.Callee
	// non-empty distinguishes this from a normal participant departure in the
	// receiver's handler.
	call, ok := c.calls[callID]
	if !ok {
		return
	}
	for _, p := range call.participants {
		if p.Username != callerUsername {
			c.reg.SendToSession(p.SessionID, registry.PhoneEvent{
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
