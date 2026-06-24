// Command server is the Retro VAX-BBS SSH entrypoint.
//
// Runs two SSH listeners on separate ports with a symmetric role-based
// partition:
//   - Public listener  — refuses admin-role accounts before checking
//     the password. Intended for eventual internet exposure once an
//     operator has set up appropriate network controls.
//   - Admin listener   — refuses non-admin accounts. Never published;
//     reachable only via the operator's VPN tunnel.
//
// Both listeners share one SSH host key and one session registry.
// The registry tracks active sessions for WHO and future PHONE routing.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	bm "github.com/charmbracelet/wish/bubbletea"
	lm "github.com/charmbracelet/wish/logging"
	rl "github.com/charmbracelet/wish/ratelimiter"
	recovermw "github.com/charmbracelet/wish/recover"
	"golang.org/x/time/rate"

	"github.com/klingon00/retro-vax-bbs/internal/auth"
	"github.com/klingon00/retro-vax-bbs/internal/lobby"
	"github.com/klingon00/retro-vax-bbs/internal/registry"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

const (
	hostKeyPath = "data/ssh_host_ed25519"
	dbPath      = "data/retro-vax-bbs.db"

	dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$PBeQih8r5fJuNB0J6vk/XA$VT14oAs2u5DJILc+W5E+VwUpB17pcNC33Em2HeHt054"
)

// contextKey is an unexported type for ssh.Context keys set by this
// package. Using a named type (rather than a plain string) prevents
// accidental collision with keys set by wish or other middleware.
type contextKey string

const (
	roleKey     contextKey = "role"
	authDoneKey contextKey = "authDone"
)

type config struct {
	publicHost string
	publicPort string
	adminHost  string
	adminPort  string

	rateLimitPerMinute float64
	rateLimitBurst     int
	rateLimitMaxIPs    int

	// authTimeoutSecs is how long a connection has to complete
	// authentication before being closed. 0 disables the timeout.
	// Applies only during the pre-auth phase — once a session is
	// authenticated there is no idle timeout, so users can remain
	// connected waiting for friends indefinitely.
	authTimeoutSecs int
}

func loadConfig() config {
	c := config{
		publicHost:         envOr("SSH_HOST", "localhost"),
		publicPort:         envOr("SSH_PORT", "2222"),
		adminHost:          envOr("ADMIN_HOST", "localhost"),
		adminPort:          envOr("ADMIN_PORT", "2223"),
		rateLimitPerMinute: envFloat("RATELIMIT_PER_MINUTE", 1.0),
		rateLimitBurst:     envInt("RATELIMIT_BURST", 5),
		rateLimitMaxIPs:    envInt("RATELIMIT_MAX_IPS", 1000),
		authTimeoutSecs:    envInt("AUTH_TIMEOUT_SECONDS", 120),
	}
	log.Printf("config: public=%s admin=%s ratelimit=%.1f/min burst=%d maxIPs=%d authTimeout=%ds",
		net.JoinHostPort(c.publicHost, c.publicPort),
		net.JoinHostPort(c.adminHost, c.adminPort),
		c.rateLimitPerMinute, c.rateLimitBurst, c.rateLimitMaxIPs,
		c.authTimeoutSecs)
	return c
}

func main() {
	cfg := loadConfig()

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalln("opening database:", err)
	}
	defer db.Close()

	reg := registry.New()
	globalDB = db
	globalReg = reg // set before listeners start; read-only after that

	publicLimiter := newLimiter(cfg)
	adminLimiter := newLimiter(cfg)

	sessionMW := sessionMiddleware(db, reg)

	// Build the pre-auth timeout option once; used by both listeners.
	// If authTimeoutSecs is 0, no timeout is applied (wish.NewServer
	// accepts a nil option gracefully... actually we just skip it).
	var authTimeoutOpt ssh.Option
	if cfg.authTimeoutSecs > 0 {
		authTimeoutOpt = preAuthTimeout(time.Duration(cfg.authTimeoutSecs) * time.Second)
	}

	publicOpts := []ssh.Option{
		wish.WithAddress(net.JoinHostPort(cfg.publicHost, cfg.publicPort)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPasswordAuth(publicPasswordHandler(db)),
		wish.WithMiddleware(
			bm.Middleware(teaHandler),
			sessionMW,
			recovermw.Middleware(),
			rl.Middleware(publicLimiter),
			lm.StructuredMiddleware(),
		),
	}
	if authTimeoutOpt != nil {
		publicOpts = append(publicOpts, authTimeoutOpt)
	}

	publicSrv, err := wish.NewServer(publicOpts...)
	if err != nil {
		log.Fatalln("creating public server:", err)
	}

	adminOpts := []ssh.Option{
		wish.WithAddress(net.JoinHostPort(cfg.adminHost, cfg.adminPort)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPasswordAuth(adminPasswordHandler(db)),
		wish.WithMiddleware(
			bm.Middleware(teaHandler),
			sessionMW,
			recovermw.Middleware(),
			rl.Middleware(adminLimiter),
			lm.StructuredMiddleware(),
		),
	}
	if authTimeoutOpt != nil {
		adminOpts = append(adminOpts, authTimeoutOpt)
	}

	adminSrv, err := wish.NewServer(adminOpts...)
	if err != nil {
		log.Fatalln("creating admin server:", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	log.Printf("public listener: %s (refuses admin-role accounts)", publicSrv.Addr)
	log.Printf("admin listener:  %s (refuses non-admin accounts; bind to VPN interface in production)",
		adminSrv.Addr)

	go func() {
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Fatalln("public server:", err)
		}
	}()
	go func() {
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Fatalln("admin server:", err)
		}
	}()

	<-done
	log.Println("stopping servers")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	shutdownErrs := make(chan error, 2)
	go func() { shutdownErrs <- publicSrv.Shutdown(ctx) }()
	go func() { shutdownErrs <- adminSrv.Shutdown(ctx) }()
	for i := 0; i < 2; i++ {
		if err := <-shutdownErrs; err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}
}

// sessionMiddleware returns a wish.Middleware that registers the
// authenticated session in the WHO registry and stores the user's role
// in the ssh.Context for teaHandler to read. The registration is
// deferred-unregistered so it is always cleaned up when the session ends,
// regardless of how it exits.
func sessionMiddleware(db *store.Store, reg *registry.Registry) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			user, err := db.GetUserByUsername(s.User())
			if err != nil {
				// Shouldn't happen — auth already verified this user —
				// but degrade gracefully rather than refusing the session.
				log.Printf("session middleware: could not look up %q: %v", s.User(), err)
				next(s)
				return
			}
			// Store role in ssh.Context so teaHandler can build the
			// lobby model without a second DB lookup.
			s.Context().SetValue(roleKey, user.Role)

			reg.Register(s.User(), user.Role, user.AdminVisible, "LOBBY")
			defer reg.Unregister(s.User())
			next(s)
		}
	}
}

// teaHandler builds the per-session lobby Model, reading the user's role
// from the ssh.Context set by sessionMiddleware. The registry pointer is
// captured from the outer scope via the closure — same instance shared by
// all sessions on both listeners.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	_, _, active := s.Pty()
	if !active {
		return nil, nil
	}

	role, _ := s.Context().Value(roleKey).(string)
	if role == "" {
		role = "user" // safe default; sessionMiddleware should always set this
	}

	// reg is captured from main's scope via the closure over sessionMW.
	// This works because teaHandler is defined inside the outer scope
	// that has access to reg... except Go closures capture variables, not
	// values. Since teaHandler is a package-level function here, we need
	// a different approach.
	//
	// The registry is passed via a package-level variable set in main.
	// This is the one deliberate exception to the "no package-level state"
	// rule — the registry IS the shared state, and making it available
	// to teaHandler without a package-level variable would require
	// restructuring main into a struct, which is more complexity than
	// the clarity it would bring at this scale.
	m := lobby.New(s.User(), role, globalReg, globalDB)
	return m, []tea.ProgramOption{tea.WithAltScreen()}
}

// globalDB and globalReg are set once in main before any listeners
// start, then treated as read-only by the rest of the program. They
// exist as package-level variables only because wish's teaHandler API
// (func(ssh.Session) (tea.Model, []tea.ProgramOption)) cannot easily
// receive additional parameters without wrapping everything in a struct.
// Both database/sql and the Registry are safe for concurrent use from
// multiple goroutines.
var (
	globalDB  *store.Store
	globalReg *registry.Registry
)

func newLimiter(cfg config) rl.RateLimiter {
	return rl.NewRateLimiter(
		rate.Every(time.Duration(float64(time.Minute)/cfg.rateLimitPerMinute)),
		cfg.rateLimitBurst,
		cfg.rateLimitMaxIPs,
	)
}

// preAuthTimeout returns a wish.Option that closes connections that do
// not complete authentication within d. It works by setting ConnCallback
// — which fires before the SSH handshake — to start a goroutine that
// races a timer against an "auth done" signal.
//
// On successful authentication, completeAuth signals the done channel,
// and the goroutine exits without closing the connection. After that
// point, there is no idle timeout: authenticated users can remain
// connected indefinitely, which is correct for a multi-user system where
// people stay logged in waiting for others.
//
// On failed or abandoned auth (wrong password, client just holds the
// TCP connection open), the timer fires and closes the connection
// silently. There is nothing useful to log — we don't know the username
// for a connection that never sent a password, and a connection that
// failed auth 3 times already has log entries from the passwordHandler.
//
// The ctx.Done() case exits the goroutine cleanly if the connection is
// closed for any other reason before the timer fires (e.g. client
// disconnected normally after a failed attempt).
func preAuthTimeout(d time.Duration) ssh.Option {
	return func(s *ssh.Server) error {
		s.ConnCallback = func(ctx ssh.Context, conn net.Conn) net.Conn {
			authDone := make(chan struct{})
			ctx.SetValue(authDoneKey, authDone)
			go func() {
				select {
				case <-time.After(d):
					// Server-side resources (goroutine, file descriptor) are
					// freed immediately when conn.Close() is called here. The
					// client's terminal may still show the password prompt until
					// the user types something — this is a known limitation of
					// how OpenSSH blocks on /dev/tty for password input rather
					// than polling the socket, so it doesn't notice the server
					// FIN until it next tries to write. From the server's
					// perspective the connection is cleaned up.
					conn.Close()
				case <-authDone:
					// auth completed; no timeout from here — authenticated
					// sessions may remain connected indefinitely
				case <-ctx.Done():
					// connection already gone; nothing to do
				}
			}()
			return conn
		}
		return nil
	}
}

func publicPasswordHandler(db *store.Store) ssh.PasswordHandler {
	return func(ctx ssh.Context, password string) bool {
		username := ctx.User()
		user, err := db.GetUserByUsername(username)
		if errors.Is(err, store.ErrNotFound) {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("public auth failure: unknown user %q from %s", username, ctx.RemoteAddr())
			return false
		}
		if err != nil {
			log.Printf("public auth error looking up %q from %s: %v", username, ctx.RemoteAddr(), err)
			return false
		}
		if user.Role == "admin" {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("public auth failure: admin account %q rejected on public listener from %s",
				username, ctx.RemoteAddr())
			return false
		}
		return completeAuth(db, user, password, "public", ctx)
	}
}

func adminPasswordHandler(db *store.Store) ssh.PasswordHandler {
	return func(ctx ssh.Context, password string) bool {
		username := ctx.User()
		user, err := db.GetUserByUsername(username)
		if errors.Is(err, store.ErrNotFound) {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("admin auth failure: unknown user %q from %s", username, ctx.RemoteAddr())
			return false
		}
		if err != nil {
			log.Printf("admin auth error looking up %q from %s: %v", username, ctx.RemoteAddr(), err)
			return false
		}
		if user.Role != "admin" {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("admin auth failure: non-admin account %q rejected on admin listener from %s",
				username, ctx.RemoteAddr())
			return false
		}
		return completeAuth(db, user, password, "admin", ctx)
	}
}

func completeAuth(db *store.Store, user *store.User, password, listener string, ctx ssh.Context) bool {
	username := ctx.User()
	if user.Status != "active" {
		_, _ = auth.VerifyPassword(password, dummyHash)
		log.Printf("%s auth failure: account %q not active (status=%s) from %s",
			listener, username, user.Status, ctx.RemoteAddr())
		return false
	}
	if user.LockedUntil.Valid && user.LockedUntil.Time.After(time.Now()) {
		_, _ = auth.VerifyPassword(password, dummyHash)
		log.Printf("%s auth failure: account %q locked until %s, from %s",
			listener, username, user.LockedUntil.Time.Format(time.RFC3339), ctx.RemoteAddr())
		return false
	}
	if !user.PasswordHash.Valid {
		_, _ = auth.VerifyPassword(password, dummyHash)
		log.Printf("%s auth failure: account %q has no password set, from %s",
			listener, username, ctx.RemoteAddr())
		return false
	}
	ok, err := auth.VerifyPassword(password, user.PasswordHash.String)
	if err != nil {
		log.Printf("%s auth error verifying %q from %s: %v", listener, username, ctx.RemoteAddr(), err)
		return false
	}
	if !ok {
		if err := db.RecordFailedAttempt(user.ID); err != nil {
			log.Printf("%s auth: error recording failed attempt for %q: %v", listener, username, err)
		}
		log.Printf("%s auth failure: wrong password for %q from %s", listener, username, ctx.RemoteAddr())
		return false
	}
	if err := db.ClearFailedAttempts(user.ID); err != nil {
		log.Printf("%s auth: error clearing failed attempts for %q: %v", listener, username, err)
	}
	if err := db.UpdateLastLogin(user.ID); err != nil {
		log.Printf("%s auth: error updating last login for %q: %v", listener, username, err)
	}

	// Signal the pre-auth timeout goroutine that authentication is done.
	// From this point, there is no idle timeout — the session can remain
	// open indefinitely.
	if ch, ok := ctx.Value(authDoneKey).(chan struct{}); ok {
		select {
		case <-ch: // already closed (shouldn't happen)
		default:
			close(ch)
		}
	}

	log.Printf("%s auth success: %q from %s", listener, username, ctx.RemoteAddr())
	return true
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: %s=%q is not a valid integer, using default %d", key, v, fallback)
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("config: %s=%q is not a valid number, using default %g", key, v, fallback)
		return fallback
	}
	return f
}
