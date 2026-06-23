// Package registry tracks active lobby sessions for the WHO command and
// future PHONE call routing. It is the shared, concurrent-safe data
// structure the lobby model comment has anticipated since the scaffold:
// "Anything that genuinely needs to be cross-session (the WHO list,
// PHONE call routing) will live behind an explicit registry passed into
// New(), once one exists — not smuggled in as a package-level variable."
package registry

import (
	"sort"
	"sync"
)

// entry tracks active sessions for one account.
type entry struct {
	role         string
	adminVisible bool
	count        int    // number of concurrently active sessions
	currentApp   string // e.g. "LOBBY", "PHONE", "MAIL"
}

// SessionView is a display-ready snapshot of one account's presence,
// returned by List. Exported so the lobby package can use it directly.
type SessionView struct {
	Username   string
	Count      int    // 1 = single session, >1 = concurrent sessions
	CurrentApp string // what the user (or their primary session) is doing
}

// Registry tracks all currently active authenticated lobby sessions.
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
// active sessions (concurrent windows), increments their count rather
// than creating a duplicate entry. adminVisible is read from the
// account's stored preference at connect time. initialApp is the app
// the session starts in — always "LOBBY" at connect time; updated later
// via SetApp when the user launches PHONE, MAIL, etc.
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
		}
	}
}

// SetApp updates the current app for a user's session. Called by app
// launchers (PHONE, MAIL, etc.) when a user enters or exits an app.
// With the current aggregated-by-username registry design, this updates
// the display for all of a user's sessions — per-session app tracking
// is a future refinement when multi-app concurrent sessions need it.
func (r *Registry) SetApp(username, app string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[username]; ok {
		e.currentApp = app
	}
}

// Unregister decrements the session count for username and removes the
// entry entirely when the count reaches zero (last window closed).
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

// List returns a visibility-filtered, alphabetically sorted snapshot of
// active sessions for a viewer with the given role. Visibility rules per
// the design doc:
//   - Regular users (role="user") see: non-admin accounts + any admin
//     who has opted into visibility (admin_visible=true).
//   - Admins (role="admin") see everyone — including invisible admins.
//
// This is a snapshot; the registry continues to change as sessions
// connect and disconnect. Callers should not cache the result.
func (r *Registry) List(viewerRole string) []SessionView {
	r.mu.RLock()
	defer r.mu.RUnlock()

	views := make([]SessionView, 0, len(r.sessions))
	for username, e := range r.sessions {
		if e.role == "admin" && viewerRole != "admin" && !e.adminVisible {
			continue // invisible admin; non-admin viewer cannot see them
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

// Get returns the session info for a specific username, regardless of
// visibility rules. Used by FINGER, which applies its own visibility
// check based on the target user's role and admin_visible setting.
// Returns (SessionView, true) if the user has active sessions,
// (SessionView{}, false) if they are not currently connected.
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
