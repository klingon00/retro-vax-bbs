package lobby

import (
	"strings"
	"testing"
)

// TestResolveAbbrev_Admin covers resolution for an admin session: full commands
// pass through unchanged, abbreviations expand per-token, exact-match wins over
// prefix ambiguity, arguments keep their original case, and argument tokens are
// never themselves resolved as keywords.
func TestResolveAbbrev_Admin(t *testing.T) {
	cases := []struct{ in, want string }{
		// Full commands unchanged (idempotent).
		{"WHO", "WHO"},
		{"SHOW USERS", "SHOW USERS"},
		{"SET PLAN CLEAR", "SET PLAN CLEAR"},
		{"LIST PENDING", "LIST PENDING"},
		// Single-word abbreviations.
		{"WH", "WHO"},
		{"LO", "LOGOUT"},
		{"TI", "TIME"},
		{"HE", "HELP"},
		// Per-token multi-word abbreviations.
		{"LI P", "LIST PENDING"},
		{"LIST P", "LIST PENDING"},
		{"LI U", "LIST USERS"},
		{"LI I", "LIST INVITES"},
		{"DEL U alice", "DELETE USER alice"},
		{"RES PAS bob", "RESET PASSWORD bob"},
		{"EXP PAS bob", "EXPIRE PASSWORD bob"},
		{"CRE U carol admin", "CREATE USER carol admin"},
		{"INV C 5 7d", "INVITE CREATE 5 7d"},
		{"HE BAN", "HELP BAN"},
		// Exact-match wins over prefix ambiguity (SHOW USER vs SHOW USERS).
		{"SHOW USER", "SHOW USER"},
		{"SHOW USER dave", "SHOW USER dave"},
		{"SHOW USERS", "SHOW USERS"},
		// Argument case preserved.
		{"fi Alice", "FINGER Alice"},
		{"KICK Bob", "KICK Bob"},
		{"ki Bob", "KICK Bob"},
		// Argument tokens are NOT resolved even when they match a command word.
		{"KICK DELETE", "KICK DELETE"},
		{"BAN SHOW 2h", "BAN SHOW 2h"},
		// Pass-through: unknown or incomplete input returned unchanged, so it
		// falls through to dispatch's existing unknown-command handling.
		{"XYZZY", "XYZZY"},
		{"SET FOO", "SET FOO"},
		{"SET", "SET"},
	}
	for _, c := range cases {
		got, msg := resolveAbbrev(c.in, "admin")
		if msg != "" {
			t.Errorf("resolveAbbrev(%q, admin) unexpected ambiguity: %q", c.in, msg)
			continue
		}
		if got != c.want {
			t.Errorf("resolveAbbrev(%q, admin) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveAbbrev_AmbiguityAdmin checks that an ambiguous prefix returns an
// empty canonical and a message naming exactly the role-visible candidates.
func TestResolveAbbrev_AmbiguityAdmin(t *testing.T) {
	cases := []struct {
		in        string
		wantCands []string
	}{
		{"L", []string{"LIST", "LOGOUT"}},
		{"S", []string{"SET", "SHOW"}},
		{"DE", []string{"DELETE", "DENY"}},
		{"D", []string{"DELETE", "DENY", "DIAL"}},
		{"UN", []string{"UNBAN", "UNLOCK"}},
		{"P", []string{"PHONE", "PURGE"}},
		{"RE", []string{"REJECT", "RESET"}},
		{"A", []string{"ANSWER", "APPROVE"}},
		{"SHOW US", []string{"SHOW USER", "SHOW USERS"}},
	}
	for _, c := range cases {
		got, msg := resolveAbbrev(c.in, "admin")
		if msg == "" {
			t.Errorf("resolveAbbrev(%q, admin) = %q, expected an ambiguity message", c.in, got)
			continue
		}
		if got != "" {
			t.Errorf("resolveAbbrev(%q, admin) ambiguous but canonical = %q, want empty", c.in, got)
		}
		for _, cand := range c.wantCands {
			if !strings.Contains(msg, cand) {
				t.Errorf("resolveAbbrev(%q, admin) message %q missing candidate %q", c.in, msg, cand)
			}
		}
	}
}

// TestResolveAbbrev_UserRoleScoped is the security-relevant case (decision 3):
// admin commands are not candidates for a non-admin, so some prefixes that are
// ambiguous for an admin resolve cleanly for a user, and any abbreviation (or
// full spelling) of an admin command passes through untouched — indistinguishable
// from gibberish, preserving the anti-enumeration property.
func TestResolveAbbrev_UserRoleScoped(t *testing.T) {
	// Resolves that differ from the admin result because admin-only candidates
	// are filtered out first.
	resolved := []struct{ in, want string }{
		{"L", "LOGOUT"},  // LIST hidden -> L is unambiguous
		{"P", "PHONE"},   // PURGE hidden
		{"D", "DIAL"},    // DENY / DELETE hidden
		{"RE", "REJECT"}, // RESET hidden
		{"A", "ANSWER"},  // APPROVE hidden
	}
	for _, c := range resolved {
		got, msg := resolveAbbrev(c.in, "user")
		if msg != "" {
			t.Errorf("resolveAbbrev(%q, user) unexpected ambiguity: %q", c.in, msg)
			continue
		}
		if got != c.want {
			t.Errorf("resolveAbbrev(%q, user) = %q, want %q", c.in, got, c.want)
		}
	}

	// Admin commands leak nothing to a non-admin: no resolution, no ambiguity,
	// line returned unchanged (same as a typo).
	passthrough := []string{
		"BA", "KI", "UN", "BAN", "KICK alice", "DEL U alice",
		"LI P", "RES PAS x", "INV C 5", "APP alice",
	}
	for _, in := range passthrough {
		got, msg := resolveAbbrev(in, "user")
		if msg != "" {
			t.Errorf("resolveAbbrev(%q, user) leaked an ambiguity message: %q", in, msg)
		}
		if got != in {
			t.Errorf("resolveAbbrev(%q, user) = %q, want unchanged %q", in, got, in)
		}
	}
}

// TestResolveAbbrev_UserAmbiguityNoAdminLeak confirms that when a user's own
// prefix is genuinely ambiguous, the message names only user commands — never
// an admin one (decision 5).
func TestResolveAbbrev_UserAmbiguityNoAdminLeak(t *testing.T) {
	adminWords := []string{
		"LIST", "APPROVE", "DENY", "DELETE", "CREATE", "KICK", "BAN",
		"UNBAN", "UNLOCK", "RESET", "EXPIRE", "INVITE", "PURGE",
	}
	cases := []struct {
		in        string
		wantCands []string
	}{
		{"S", []string{"SET", "SHOW"}},                  // both user commands
		{"SET P", []string{"SET PASSWORD", "SET PLAN"}}, // both user commands
	}
	for _, c := range cases {
		got, msg := resolveAbbrev(c.in, "user")
		if msg == "" {
			t.Errorf("resolveAbbrev(%q, user) = %q, expected ambiguity", c.in, got)
			continue
		}
		for _, cand := range c.wantCands {
			if !strings.Contains(msg, cand) {
				t.Errorf("resolveAbbrev(%q, user) message %q missing %q", c.in, msg, cand)
			}
		}
		for _, w := range adminWords {
			if strings.Contains(msg, w) {
				t.Errorf("resolveAbbrev(%q, user) message %q leaked admin word %q", c.in, msg, w)
			}
		}
	}
}

// TestDispatch_Abbrev exercises the resolver through dispatch() itself (the real
// integration point) for the nil-safe paths: abbreviation resolving to a handler,
// an ambiguous prefix short-circuiting, and admin commands staying invisible to a
// non-admin whether typed in full or abbreviated.
func TestDispatch_Abbrev(t *testing.T) {
	admin := Model{role: "admin"}
	user := Model{role: "user"}

	// "HE" resolves to HELP and actually runs it.
	if out, _ := dispatch("HE", admin); !strings.Contains(out, "Available commands") {
		t.Errorf(`dispatch("HE", admin) = %q, want HELP output`, out)
	}

	// Ambiguous prefix returns the ambiguity message, runs nothing.
	if out, cmd := dispatch("L", admin); !strings.Contains(out, "Ambiguous") ||
		!strings.Contains(out, "LIST") || !strings.Contains(out, "LOGOUT") || cmd != nil {
		t.Errorf(`dispatch("L", admin) = %q (cmd=%v), want ambiguity naming LIST and LOGOUT`, out, cmd)
	}

	// A non-admin can't reach an admin command by abbreviation OR in full — both
	// give the same "not recognized" a typo gets, with no ambiguity leak.
	for _, in := range []string{"BA", "BAN", "KI", "KICK alice"} {
		out, _ := dispatch(in, user)
		if !strings.Contains(out, "not a recognized command") {
			t.Errorf(`dispatch(%q, user) = %q, want unrecognized-command response`, in, out)
		}
		if strings.Contains(out, "Ambiguous") {
			t.Errorf(`dispatch(%q, user) leaked an ambiguity message: %q`, in, out)
		}
	}
}
