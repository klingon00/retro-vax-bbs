// Command server is the Retro VAX-BBS SSH entrypoint.
//
// SECURITY: real password authentication is now wired in (argon2id,
// checked against SQLite-stored accounts), but account lockout, per-IP
// rate limiting, and the dual-listener public/admin split are NOT yet
// implemented — those are the next milestones. It is bound to localhost
// specifically so this is safe for local development, but DO NOT change
// the host constant below or otherwise expose this build on a network
// until lockout and rate limiting land.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	bm "github.com/charmbracelet/wish/bubbletea"
	lm "github.com/charmbracelet/wish/logging"
	recovermw "github.com/charmbracelet/wish/recover"

	"github.com/klingon00/retro-vax-bbs/internal/auth"
	"github.com/klingon00/retro-vax-bbs/internal/lobby"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

const (
	// Localhost-only on purpose — see the package-level SECURITY note.
	// The design doc's dual-listener split (public listener refusing
	// admin accounts, admin listener refusing everyone else) is a later
	// milestone; this is one listener for now.
	host = "localhost"
	port = "2222"

	// keygen (called by wish.WithHostKeyPath) creates this file and its
	// parent directory on first run, with 0600/0700 permissions, if it
	// doesn't already exist. Keep this directory out of git — it's a
	// private key, not a config file. See .gitignore.
	hostKeyPath = "data/ssh_host_ed25519"

	// SQLite database file. Same data/ directory as the host key, same
	// gitignore coverage (*.db).
	dbPath = "data/retro-vax-bbs.db"

	// dummyHash is verified against on a username-not-found, so that
	// rejecting a nonexistent user costs the same argon2id computation
	// as rejecting a wrong password on a real one. Without this, a
	// not-found response returns near-instantly while a wrong-password
	// response takes ~0.5s — a timing side channel that lets an attacker
	// enumerate valid usernames by measuring response time, even though
	// every rejection already carries an identical message. The design
	// doc's no-enumeration rule is about message content; this extends
	// the same principle to timing, which is just as real a leak.
	dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$PBeQih8r5fJuNB0J6vk/XA$VT14oAs2u5DJILc+W5E+VwUpB17pcNC33Em2HeHt054"
)

func main() {
	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalln("opening database:", err)
	}
	defer db.Close()

	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPasswordAuth(passwordHandler(db)),
		wish.WithMiddleware(
			// Composed innermost-to-outermost: the LAST entry in this
			// list runs first (outermost), per wish's own doc comment
			// on Middleware. So the order below is:
			//   1. lm.StructuredMiddleware() logs connect
			//   2. recoverMW catches any panic from the tea program
			//   3. bm.Middleware runs the actual lobby tea.Program
			//   4. (unwind) recoverMW's defer fires if anything panicked
			//   5. lm.StructuredMiddleware() logs disconnect
			//
			// recoverMW is session-level defense in depth — a backstop
			// for a panic outside the command dispatch loop entirely
			// (e.g. during PTY/window setup). The dispatch()-level
			// recover() in internal/lobby/commands.go is the one doing
			// the real work of keeping a single bad command from ending
			// a session; see the comment there.
			recovermw.Middleware(bm.Middleware(teaHandler)),
			lm.StructuredMiddleware(),
		),
	)
	if err != nil {
		log.Fatalln(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	log.Printf("starting SSH server on %s (auth: argon2id, no lockout/rate-limiting yet — local dev only, do not expose)", s.Addr)
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Fatalln(err)
		}
	}()

	<-done
	log.Println("stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalln(err)
	}
}

// passwordHandler returns a wish/ssh PasswordHandler closing over the
// store. Every rejection path — user not found, account not active,
// wrong password — returns false uniformly and is logged identically in
// shape (so the log itself doesn't become an enumeration vector either),
// per the design doc's auth-failure logging and no-enumeration
// requirements. Lockout (failed_attempts/locked_until) is deliberately
// not implemented here yet — this slice proves the auth path works; the
// next slice adds lockout on top of it.
func passwordHandler(db *store.Store) ssh.PasswordHandler {
	return func(ctx ssh.Context, password string) bool {
		username := ctx.User()

		user, err := db.GetUserByUsername(username)
		if errors.Is(err, store.ErrNotFound) {
			// Burn the same argon2id cost as a real wrong-password check
			// would, against a fixed dummy hash, so this path isn't
			// distinguishable by timing. Result is always false; only
			// the cost matters here.
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("auth failure: unknown user %q from %s", username, ctx.RemoteAddr())
			return false
		}
		if err != nil {
			log.Printf("auth error looking up %q from %s: %v", username, ctx.RemoteAddr(), err)
			return false
		}

		if user.Status != "active" {
			// Still run the verification so a pending/suspended account
			// doesn't leak its status via timing either — same
			// reasoning as the not-found case above.
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("auth failure: account %q not active (status=%s) from %s", username, user.Status, ctx.RemoteAddr())
			return false
		}

		if !user.PasswordHash.Valid {
			// Account exists but has no password set yet (e.g. pending
			// first-login flow, not built yet). Reject, same timing
			// treatment.
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("auth failure: account %q has no password set, from %s", username, ctx.RemoteAddr())
			return false
		}

		ok, err := auth.VerifyPassword(password, user.PasswordHash.String)
		if err != nil {
			log.Printf("auth error verifying %q from %s: %v", username, ctx.RemoteAddr(), err)
			return false
		}
		if !ok {
			log.Printf("auth failure: wrong password for %q from %s", username, ctx.RemoteAddr())
			return false
		}

		log.Printf("auth success: %q from %s", username, ctx.RemoteAddr())
		return true
	}
}

// teaHandler builds the per-session lobby Model. wish gives every
// connected session its own tea.Program, so there is no shared mutable
// state between sessions here by construction — anything cross-session
// (the WHO list, PHONE call routing) will need an explicit registry
// passed in here once it exists, not a package-level variable.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	_, _, active := s.Pty()
	if !active {
		// No PTY means no terminal — e.g. `ssh host -p 2222 some-command`
		// instead of an interactive session. Bubble Tea needs a PTY;
		// refuse cleanly rather than letting bm.Middleware error out.
		return nil, nil
	}
	m := lobby.New(s.User())
	return m, []tea.ProgramOption{tea.WithAltScreen()}
}
