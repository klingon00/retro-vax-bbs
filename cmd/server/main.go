// Command server is the VMS/PHONE revival SSH entrypoint.
//
// SECURITY: this scaffold has NO AUTHENTICATION wired up yet. wish's
// default behavior with no auth handlers configured is to accept any
// username/password combination. It is bound to localhost specifically
// so that is safe for local development, but DO NOT change the host
// constant below or otherwise expose this build on a network until the
// auth milestone (argon2id, account states, lockout) lands.
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

	"github.com/klingon00/retro-vms-bbs/internal/lobby"
)

const (
	// Localhost-only on purpose — see the package-level SECURITY note.
	// The design doc's dual-listener split (public listener refusing
	// admin accounts, admin listener refusing everyone else) is a later
	// milestone; this is one unauthenticated listener for scaffolding.
	host = "localhost"
	port = "2222"

	// keygen (called by wish.WithHostKeyPath) creates this file and its
	// parent directory on first run, with 0600/0700 permissions, if it
	// doesn't already exist. Keep this directory out of git — it's a
	// private key, not a config file. See .gitignore.
	hostKeyPath = "data/ssh_host_ed25519"
)

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(hostKeyPath),
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

	log.Printf("starting SSH server on %s (NO AUTH YET — local dev only, do not expose)", s.Addr)
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
