// Package registry tracks active lobby sessions for WHO, FINGER, and
// PHONE call routing. It is the shared, concurrent-safe data structure
// the lobby model comment has anticipated since the scaffold: "Anything
// that genuinely needs to be cross-session (the WHO list, PHONE call
// routing) will live behind an explicit registry passed into New()."
//
// Presence is account-level (keyed by username); event delivery is
// per-session. One account can have several concurrent SSH sessions, and
// each gets its OWN notify channel — so a PHONE control event (ring, answer,
// hangup, reject) is delivered to a specific session, not raced for by all of
// an account's sessions on a single shared channel. WHO/FINGER/KICK stay
// account-level; only the notify/done event path is per-session.
package registry

import (
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/klingon00/retro-vax-bbs/internal/debuglog"
)

// EventType identifies what kind of PHONE event a PhoneEvent carries.
type EventType int

const (
	EventRing            EventType = iota // incoming call — answer or reject
	EventHangup                           // caller hung up before answer, or mid-call
	EventReject                           // callee explicitly rejected the call
	EventAnswer                           // callee answered — call is now live
	EventRinging                          // advisory: someone in the call is ringing another user
	EventAdminNotify                      // one-shot notification delivered to admin lobby sessions
	EventAnswerElsewhere                  // ring retracted: another session of this account answered
)

// String renders an event type as its constant name so diagnostic logs read as
// "send EventAnswer call=..." rather than "send 3 call=...". Distinguishing
// EventHangup from EventAnswerElsewhere at a glance is the whole point of
// having separate types; a log that prints both as integers gives that up.
// strconv rather than fmt in the default case: it is a straight int-to-string
// conversion with no formatting verb to interpret, so it says what it does more
// directly than Sprintf would. (This predates the session-lifecycle logging
// below, which does need fmt — the choice here is now style, not necessity.)
func (e EventType) String() string {
	switch e {
	case EventRing:
		return "EventRing"
	case EventHangup:
		return "EventHangup"
	case EventReject:
		return "EventReject"
	case EventAnswer:
		return "EventAnswer"
	case EventRinging:
		return "EventRinging"
	case EventAdminNotify:
		return "EventAdminNotify"
	case EventAnswerElsewhere:
		return "EventAnswerElsewhere"
	default:
		return "EventType(" + strconv.Itoa(int(e)) + ")"
	}
}

// PhoneEvent is sent over a session's notify channel when something
// call-related happens that the session's Bubble Tea program needs to
// react to. The channel is buffered (size 8) so senders never block on
// a slow receiver.
type PhoneEvent struct {
	Type   EventType
	CallID string // opaque identifier for the call; stable across ring/answer/hangup
	Caller string // username who initiated the call
	Callee string // username being called
}

// sessionState is the per-session delivery state: one SSH session's notify
// and done channels. Every session of an account has its own, so events
// addressed to a specific session (via SendToSession) reach only that session
// and are never stolen by a sibling session sharing the account.
type sessionState struct {
	// username is the owning account, so Unregister can find the account
	// entry from a sessionID alone.
	username string

	// notify carries PHONE events (ring, hangup, reject, answer) to this
	// session's Bubble Tea program. Buffered so senders don't block. Never
	// closed — it has lock-free non-blocking senders (SendToSession /
	// NotifyAdmins), so closing it could race a send. done is the shutdown
	// signal instead.
	notify chan PhoneEvent

	// done is closed by Unregister when this session departs, waking the
	// goroutine blocked in waitForPhoneEvent so it exits instead of leaking.
	// Signal-only: nothing ever sends on it, so closing is always race-free.
	done chan struct{}
}

// entry tracks account-level presence and all sessions for one account.
type entry struct {
	role         string
	adminVisible bool
	currentApp   string // e.g. "LOBBY", "PHONE", "MAIL" — account-level display label

	// kick, when non-nil, terminates the user's active SSH session. Set by
	// sessionMiddleware; used by the KICK admin command. Account-level and
	// last-writer-wins: with several concurrent sessions, the most recent
	// SetKick overwrites the prior func, so KICK only closes the most-recently-
	// registered session. Known limitation, tracked in the PHONE audit report.
	kick func()

	// sessions holds every active session for this account, keyed by sessionID.
	sessions map[string]*sessionState
}

// SessionView is a display-ready snapshot of one account's presence.
type SessionView struct {
	Username   string
	Count      int
	CurrentApp string
}

// Registry tracks all currently active authenticated sessions.
// Safe for concurrent use — one goroutine per SSH session.
type Registry struct {
	mu sync.RWMutex

	// accounts is keyed by username; the source of truth for presence
	// (WHO/FINGER) and for enumerating an account's sessions (ring fan-out).
	accounts map[string]*entry

	// sessionIndex is keyed by sessionID (global, unique), so Events,
	// SendToSession, and Unregister can resolve a session in O(1) without
	// knowing its account. Kept in lockstep with accounts[*].sessions.
	sessionIndex map[string]*sessionState

	// nextID mints unique session IDs. Monotonic, never reused, so a
	// reconnect can never collide with a live session.
	nextID uint64
}

// New returns an initialized, empty Registry.
func New() *Registry {
	return &Registry{
		accounts:     make(map[string]*entry),
		sessionIndex: make(map[string]*sessionState),
	}
}

// Register records a new session for username and returns its opaque
// sessionID. The caller must pass that ID to Events/Unregister and thread it
// to the PHONE layer so events can be routed to this specific session. If the
// account already has sessions, its entry (role/adminVisible/currentApp) is
// reused and only the new session's channels are created.
func (r *Registry) Register(username, role string, adminVisible bool, initialApp string) string {
	// LIFO-deferred emit, same pattern and same reason as internal/phone/call.go:
	// registered before the unlock defer so it runs after it, keeping the log
	// write outside r.mu.
	var logLine string
	defer func() {
		if logLine != "" {
			debuglog.Logf("%s", logLine)
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	sid := strconv.FormatUint(r.nextID, 10)

	ss := &sessionState{
		username: username,
		notify:   make(chan PhoneEvent, 8),
		done:     make(chan struct{}),
	}

	e, ok := r.accounts[username]
	if !ok {
		e = &entry{
			role:         role,
			adminVisible: adminVisible,
			currentApp:   initialApp,
			sessions:     make(map[string]*sessionState),
		}
		r.accounts[username] = e
	}
	e.sessions[sid] = ss
	r.sessionIndex[sid] = ss

	// The session→account mapping every other log line depends on: once this is
	// recorded, a bare "session=7" elsewhere in the log is resolvable to a
	// person. The session count is what makes a multi-session account legible —
	// "alice now has 2 sessions" is the precondition for every finding-11
	// scenario, and its absence is the first thing to check when a scenario
	// behaves as though only one session exists.
	if debuglog.Enabled() {
		logLine = fmt.Sprintf("register session=%s user=%s role=%s app=%s (account now has %d session(s))",
			sid, username, role, initialApp, len(e.sessions))
	}
	return sid
}

// Unregister removes the session identified by sessionID. It closes that
// session's done channel — unblocking its waitForPhoneEvent goroutine so it
// returns instead of leaking — then removes the session from both indexes,
// deleting the account entry only when its last session departs. done is
// closed exactly once: the sessionIndex delete happens before the close and
// shares this critical section, so a later Unregister for an already-removed
// session hits the !ok early return and never re-closes.
func (r *Registry) Unregister(sessionID string) {
	// LIFO-deferred emit, same pattern as Register. Unlike Register this
	// function has two exits, and both are instrumented: a session departure
	// should never be silent, and the early one is the more interesting of the
	// two — reaching it means an unregister arrived for a session the registry
	// does not know, i.e. a double-unregister or a stale session ID.
	var logLine string
	defer func() {
		if logLine != "" {
			debuglog.Logf("%s", logLine)
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	ss, ok := r.sessionIndex[sessionID]
	if !ok {
		if debuglog.Enabled() {
			logLine = fmt.Sprintf("unregister session=%s: UNKNOWN session (double-unregister or stale id)",
				sessionID)
		}
		return
	}
	delete(r.sessionIndex, sessionID)
	close(ss.done)

	// The four lines of control flow below are unchanged from before this
	// logging existed; only the guarded blocks are new. The session count is
	// the fact that matters when reading a drop: an account whose other
	// sessions are still live must stay present (and dialable), and these lines
	// are what prove the account entry outlived the session rather than being
	// torn down with it.
	if e, ok := r.accounts[ss.username]; ok {
		delete(e.sessions, sessionID)
		if len(e.sessions) == 0 {
			delete(r.accounts, ss.username)
			if debuglog.Enabled() {
				logLine = fmt.Sprintf("unregister session=%s user=%s: 0 sessions remain, account entry removed (last session out)",
					sessionID, ss.username)
			}
		} else if debuglog.Enabled() {
			logLine = fmt.Sprintf("unregister session=%s user=%s: %d session(s) remain, account entry retained",
				sessionID, ss.username, len(e.sessions))
		}
	} else if debuglog.Enabled() {
		// Can't happen: Register writes sessionIndex and accounts[*].sessions
		// together under this same lock, and Unregister is the only remover.
		// Logged anyway precisely because it can't happen — if the two maps
		// ever disagree, the only symptom without this line is silence, which
		// reads exactly like "Unregister was never called". A can't-happen
		// state that stays invisible when it does happen is the expensive kind.
		logLine = fmt.Sprintf("unregister session=%s user=%s: account entry MISSING (registry desync — sessionIndex and accounts disagree)",
			sessionID, ss.username)
	}
}

// SetApp updates the account-level current-app label (shown by WHO). Any
// session of the account setting it wins; the label is display-only.
func (r *Registry) SetApp(username, app string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.accounts[username]; ok {
		e.currentApp = app
	}
}

// Events returns the receive ends of the notify and done channels for the
// session identified by sessionID, so the session's own Bubble Tea program can
// wait for an incoming event or for its teardown. Both come from the same
// sessionState under one RLock, so they are always a matched pair. done closes
// when this session departs. Both are nil if the session is unknown — callers
// must check (and treat a mismatch as impossible, since they share a struct).
func (r *Registry) Events(sessionID string) (<-chan PhoneEvent, <-chan struct{}) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ss, ok := r.sessionIndex[sessionID]; ok {
		return ss.notify, ss.done
	}
	return nil, nil
}

// SendToSession delivers event to exactly one session's notify channel. This
// is the point-to-point primitive PHONE uses for in-call events, where the
// target participant's session is known. A no-op if the session is gone (its
// channel is never closed, so the non-blocking send after RUnlock is safe even
// against a concurrent Unregister — the buffered channel is simply GC'd).
func (r *Registry) SendToSession(sessionID string, event PhoneEvent) {
	r.mu.RLock()
	ss, ok := r.sessionIndex[sessionID]
	// Copy the owning account out under the lock so the diagnostic lines below
	// need nothing from the registry once it is released. sessionState.username
	// is immutable today, but a log line is not a reason to depend on that.
	var owner string
	if ok {
		owner = ss.username
	}
	r.mu.RUnlock()

	// Everything below runs with r.mu already released, so no Logf here can
	// widen the critical section — see the note in package debuglog.
	if !ok {
		debuglog.Logf("send %v call=%s caller=%s callee=%s -> session=%s DROPPED (no such session)",
			event.Type, event.CallID, event.Caller, event.Callee, sessionID)
		return
	}
	select {
	case ss.notify <- event:
		debuglog.Logf("send %v call=%s caller=%s callee=%s -> session=%s user=%s delivered",
			event.Type, event.CallID, event.Caller, event.Callee, sessionID, owner)
	default:
		// Buffer full: the event is discarded and nobody is told. Downstream
		// this is indistinguishable from an event that was never sent — the
		// session simply never changes state, with no error and no trace. That
		// makes this the most valuable line in the file, and the reason a plain
		// "delivered" line is logged too: without its counterpart, a discard is
		// only conspicuous if you already knew to expect a delivery.
		debuglog.Logf("send %v call=%s caller=%s callee=%s -> session=%s user=%s DROPPED (notify buffer full)",
			event.Type, event.CallID, event.Caller, event.Callee, sessionID, owner)
	}
}

// SessionsOf returns a snapshot of the session IDs currently registered for
// username, so PHONE can fan a ring out to an account's sessions. Returns nil
// if the account is not connected. The caller decides which of these sessions
// are "ringable" — the registry has no notion of call membership.
func (r *Registry) SessionsOf(username string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.accounts[username]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(e.sessions))
	for sid := range e.sessions {
		ids = append(ids, sid)
	}
	return ids
}

// Connected reports whether username has at least one active session. Used by
// PHONE admission as a presence check (replacing the old "is the notify
// channel nil" idiom, which no longer maps to a single account channel).
func (r *Registry) Connected(username string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.accounts[username]
	return ok && len(e.sessions) > 0
}

// List returns a visibility-filtered, alphabetically sorted snapshot. Count is
// the account's concurrent-session count.
func (r *Registry) List(viewerRole string) []SessionView {
	r.mu.RLock()
	defer r.mu.RUnlock()

	views := make([]SessionView, 0, len(r.accounts))
	for username, e := range r.accounts {
		if e.role == "admin" && viewerRole != "admin" && !e.adminVisible {
			continue
		}
		views = append(views, SessionView{
			Username:   username,
			Count:      len(e.sessions),
			CurrentApp: e.currentApp,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		return views[i].Username < views[j].Username
	})
	return views
}

// Get returns session info for a specific username without visibility
// filtering. Used by FINGER and PHONE routing.
func (r *Registry) Get(username string) (SessionView, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.accounts[username]
	if !ok {
		return SessionView{}, false
	}
	return SessionView{
		Username:   username,
		Count:      len(e.sessions),
		CurrentApp: e.currentApp,
	}, true
}

// SetKick stores a function that terminates the given user's active SSH
// session. Called by sessionMiddleware when a session starts; the stored
// function calls ssh.Session.Exit(0) on the underlying connection. Account-
// level and last-writer-wins — see the entry.kick comment.
func (r *Registry) SetKick(username string, kick func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.accounts[username]; ok {
		e.kick = kick
	}
}

// Kick calls the stored kick function for username, forcibly closing their
// SSH session. Returns true if a session was found and kicked.
func (r *Registry) Kick(username string) bool {
	r.mu.RLock()
	e, ok := r.accounts[username]
	var kick func()
	if ok {
		kick = e.kick
	}
	r.mu.RUnlock()
	if kick != nil {
		kick()
		return true
	}
	return false
}

// NotifyAdmins sends a PhoneEvent to every connected admin session. Used to
// push registration and other admin-relevant notifications to the lobby
// without requiring admins to poll. Every session of every admin account is
// notified (not just one per account) so a multi-session admin sees it wherever
// they are looking.
func (r *Registry) NotifyAdmins(event PhoneEvent) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.accounts {
		if e.role != "admin" {
			continue
		}
		for _, ss := range e.sessions {
			select {
			case ss.notify <- event:
			default: // don't block if a session's channel is full
			}
		}
	}
}
