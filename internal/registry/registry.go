// Package registry tracks active lobby sessions for WHO, FINGER, and
// PHONE call routing. It is the shared, concurrent-safe data structure
// the lobby model comment has anticipated since the scaffold: "Anything
// that genuinely needs to be cross-session (the WHO list, PHONE call
// routing) will live behind an explicit registry passed into New()."
package registry

import (
	"sort"
	"sync"
)

// EventType identifies what kind of PHONE event a PhoneEvent carries.
type EventType int

const (
	EventRing        EventType = iota // incoming call — answer or reject
	EventHangup                       // caller hung up before answer, or mid-call
	EventReject                       // callee explicitly rejected the call
	EventAnswer                       // callee answered — call is now live
	EventRinging                      // advisory: someone in the call is ringing another user
	EventAdminNotify                  // one-shot notification delivered to admin lobby sessions
)

// PhoneEvent is sent over a session's Notify channel when something
// call-related happens that the session's Bubble Tea program needs to
// react to. The channel is buffered (size 8) so senders never block on
// a slow receiver.
type PhoneEvent struct {
	Type   EventType
	CallID string // opaque identifier for the call; stable across ring/answer/hangup
	Caller string // username who initiated the call
	Callee string // username being called
}

// entry tracks active sessions for one account.
type entry struct {
	role         string
	adminVisible bool
	count        int    // number of concurrently active sessions
	currentApp   string // e.g. "LOBBY", "PHONE", "MAIL"

	// notify is the channel through which PHONE events (ring, hangup,
	// reject, answer) are delivered to this session's Bubble Tea program.
	// Buffered so senders don't block. Created when the first session
	// registers; all concurrent sessions for the same user share it.
	notify chan PhoneEvent

	// kick, when non-nil, terminates the user's active SSH session.
	// Set by sessionMiddleware; used by the KICK admin command.
	kick func()
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
	mu       sync.RWMutex
	sessions map[string]*entry
}

// New returns an initialized, empty Registry.
func New() *Registry {
	return &Registry{
		sessions: make(map[string]*entry),
	}
}

// Register records a new session for username. If the user already has
// active sessions, increments their count and reuses the existing notify
// channel. adminVisible and initialApp are only set on the first session.
func (r *Registry) Register(username, role string, adminVisible bool, initialApp string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[username]; ok {
		e.count++
	} else {
		r.sessions[username] = &entry{
			role:         role,
			adminVisible: adminVisible,
			count:        1,
			currentApp:   initialApp,
			notify:       make(chan PhoneEvent, 8),
		}
	}
}

// Unregister decrements the session count for username and removes the
// entry (and its notify channel) when the count reaches zero.
func (r *Registry) Unregister(username string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sessions[username]
	if !ok {
		return
	}
	e.count--
	if e.count <= 0 {
		delete(r.sessions, username)
	}
}

// SetApp updates the current app label for the user's session(s).
func (r *Registry) SetApp(username, app string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[username]; ok {
		e.currentApp = app
	}
}

// Notify returns the PhoneEvent channel for the given username, so that
// PHONE infrastructure can send ring/hangup/etc. events directly to that
// user's session. Returns nil if the user is not connected — callers
// must check.
func (r *Registry) Notify(username string) chan<- PhoneEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.sessions[username]; ok {
		return e.notify
	}
	return nil
}

// Events returns the receive end of the PhoneEvent channel for the given
// username, so that the user's own Bubble Tea program can poll for
// incoming events. Returns nil if not connected.
func (r *Registry) Events(username string) <-chan PhoneEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.sessions[username]; ok {
		return e.notify
	}
	return nil
}

// List returns a visibility-filtered, alphabetically sorted snapshot.
func (r *Registry) List(viewerRole string) []SessionView {
	r.mu.RLock()
	defer r.mu.RUnlock()

	views := make([]SessionView, 0, len(r.sessions))
	for username, e := range r.sessions {
		if e.role == "admin" && viewerRole != "admin" && !e.adminVisible {
			continue
		}
		views = append(views, SessionView{
			Username:   username,
			Count:      e.count,
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
	e, ok := r.sessions[username]
	if !ok {
		return SessionView{}, false
	}
	return SessionView{
		Username:   username,
		Count:      e.count,
		CurrentApp: e.currentApp,
	}, true
}

// SetKick stores a function that terminates the given user's active SSH
// session. Called by sessionMiddleware when a session starts; the stored
// function calls ssh.Session.Exit(0) on the underlying connection.
func (r *Registry) SetKick(username string, kick func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[username]; ok {
		e.kick = kick
	}
}

// Kick calls the stored kick function for username, forcibly closing their
// SSH session. Returns true if a session was found and kicked.
func (r *Registry) Kick(username string) bool {
	r.mu.RLock()
	e, ok := r.sessions[username]
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

// NotifyAdmins sends a PhoneEvent to every connected admin session.
// Used to push registration and other admin-relevant notifications
// to the lobby without requiring admins to poll.
func (r *Registry) NotifyAdmins(event PhoneEvent) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.sessions {
		if e.role != "admin" || e.notify == nil {
			continue
		}
		select {
		case e.notify <- event:
		default: // don't block if admin channel is full
		}
	}
}
