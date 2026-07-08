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
	"strings"
	"syscall"
	"time"
	// time/tzdata embeds the IANA zone DB so TZ=<zone> resolves inside the
	// distroless image (which ships no /etc/localtime and may lack
	// /usr/share/zoneinfo); it's a fallback only — bare-metal still uses system
	// zoneinfo first, so behavior there is unchanged. This makes local-time
	// display (TIME/WHO/FINGER/LIST) honor the operator's TZ in Docker/Unraid
	// instead of silently falling back to UTC. See docs/admin-guide.md.
	_ "time/tzdata"

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
	"github.com/klingon00/retro-vax-bbs/internal/phone"
	"github.com/klingon00/retro-vax-bbs/internal/registration"
	"github.com/klingon00/retro-vax-bbs/internal/registry"
	"github.com/klingon00/retro-vax-bbs/internal/setpassword"
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
	roleKey               contextKey = "role"
	authDoneKey           contextKey = "authDone"
	regModeKey            contextKey = "regMode"            // set when username=="new"
	mustChangePasswordKey contextKey = "mustChangePassword" // set when EXPIRE PASSWORD is pending
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

	// registrationMode controls self-service account creation.
	// "closed"              — admin creates all accounts via cmd/adduser.
	// "invite-only"         — user SSHs as "new", provides invite code; account activates immediately.
	// "open-with-approval"  — user SSHs as "new", account sits pending until admin APPROVEs.
	registrationMode string

	// pendingExpiryDays: pending accounts older than this are auto-deleted
	// to prevent username squatting. 0 = never expire.
	pendingExpiryDays int
}

func loadConfig() config {
	regMode := envOr("REGISTRATION_MODE", "closed")
	switch regMode {
	case "closed", "invite-only", "open-with-approval":
	default:
		log.Printf("config: unknown REGISTRATION_MODE %q, defaulting to 'closed'", regMode)
		regMode = "closed"
	}
	c := config{
		publicHost:         envOr("SSH_HOST", "localhost"),
		publicPort:         envOr("SSH_PORT", "2222"),
		adminHost:          envOr("ADMIN_HOST", "localhost"),
		adminPort:          envOr("ADMIN_PORT", "2223"),
		rateLimitPerMinute: envFloat("RATELIMIT_PER_MINUTE", 1.0),
		rateLimitBurst:     envInt("RATELIMIT_BURST", 5),
		rateLimitMaxIPs:    envInt("RATELIMIT_MAX_IPS", 1000),
		authTimeoutSecs:    envInt("AUTH_TIMEOUT_SECONDS", 120),
		registrationMode:   regMode,
		pendingExpiryDays:  envInt("PENDING_EXPIRY_DAYS", 7),
	}
	log.Printf("config: public=%s admin=%s ratelimit=%.1f/min burst=%d maxIPs=%d authTimeout=%ds registration=%s pendingExpiry=%dd",
		net.JoinHostPort(c.publicHost, c.publicPort),
		net.JoinHostPort(c.adminHost, c.adminPort),
		c.rateLimitPerMinute, c.rateLimitBurst, c.rateLimitMaxIPs,
		c.authTimeoutSecs, c.registrationMode, c.pendingExpiryDays)
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
	globalReg = reg
	globalCalls = phone.NewCalls(reg)
	globalRegMode = cfg.registrationMode
	if cfg.pendingExpiryDays > 0 {
		globalPendingExpiry = time.Duration(cfg.pendingExpiryDays) * 24 * time.Hour
	}

	// Wire the password hashing function into the registration package so
	// it can hash passwords without a direct import of internal/auth.
	registration.SetHashFn(auth.HashPassword)

	// Purge expired pending accounts at startup, then every 6 hours.
	if globalPendingExpiry > 0 {
		if n, err := db.PurgeExpiredPendingAccounts(globalPendingExpiry); err != nil {
			log.Printf("startup: purge expired pending accounts: %v", err)
		} else if n > 0 {
			log.Printf("startup: purged %d expired pending account(s)", n)
		}
		go func() {
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			for range t.C {
				if n, err := db.PurgeExpiredPendingAccounts(globalPendingExpiry); err != nil {
					log.Printf("periodic purge: %v", err)
				} else if n > 0 {
					log.Printf("periodic purge: removed %d expired pending account(s)", n)
				}
			}
		}()
	}

	bootstrapAdminAccount(db)

	publicLimiter := newLimiter(cfg)
	adminLimiter := newLimiter(cfg)

	sessionMW := sessionMiddleware(db, reg, globalCalls)

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
		wish.WithPasswordAuth(publicPasswordHandler(db, cfg.registrationMode)),
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
func sessionMiddleware(db *store.Store, reg *registry.Registry, calls *phone.Calls) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			// Registration sessions connect as "new" — no DB account exists yet.
			// Just run the handler; teaHandler routes them to the registration TUI.
			if strings.EqualFold(s.User(), "new") {
				next(s)
				return
			}

			user, err := db.GetUserByUsername(s.User())
			if err != nil {
				log.Printf("session middleware: could not look up %q: %v", s.User(), err)
				next(s)
				return
			}
			s.Context().SetValue(roleKey, user.Role)
			s.Context().SetValue(mustChangePasswordKey, user.MustChangePassword)

			reg.Register(s.User(), user.Role, user.AdminVisible, "LOBBY")
			// Store a kick function so admin KICK command can close this session.
			reg.SetKick(s.User(), func() { s.Exit(0) })

			// Teardown cleanup. Go runs defers last-registered-first (LIFO), so
			// this ordering is deliberate: Unregister is registered FIRST and
			// HangupUser SECOND, which means at session end HangupUser runs
			// BEFORE Unregister. HangupUser removes this user from any active
			// PHONE call — closing their IncomingChar to reap the waitForChar
			// goroutine and notifying the other participants — and Unregister
			// then removes this account's registry entry and closes its done
			// channel, reaping the waitForPhoneEvent goroutine. A mid-call SSH
			// *drop* runs neither HANGUP nor EXIT, so these defers are the only
			// thing that tears down a call and its goroutines on an abrupt
			// disconnect.
			defer reg.Unregister(s.User())
			defer calls.HangupUser(s.User())
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

	// Registration flow: username "new" was authenticated as a guest.
	// Use inline rendering (no alt screen) — simpler for a sequential
	// prompt form and avoids the BubbleTea v1.x sacrifice-line issue.
	if regMode, ok := s.Context().Value(regModeKey).(string); ok && regMode != "" {
		m := registration.New(regMode, globalDB, globalReg, globalPendingExpiry)
		return m, nil
	}

	// Mandatory password change (admin ran EXPIRE PASSWORD): same
	// inline-rendering, no-alt-screen treatment as registration, and for
	// the same reason — this codebase has no way to swap the root model
	// mid-session, so once the password is changed the user is told to
	// reconnect rather than handed off into the lobby inline.
	if mustChange, ok := s.Context().Value(mustChangePasswordKey).(bool); ok && mustChange {
		m := setpassword.NewForced(globalDB, s.User())
		return m, nil
	}

	role, _ := s.Context().Value(roleKey).(string)
	if role == "" {
		role = "user"
	}
	m := lobby.New(s.User(), role, globalReg, globalDB, globalCalls, s, globalPendingExpiry)
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
	globalDB            *store.Store
	globalReg           *registry.Registry
	globalCalls         *phone.Calls
	globalRegMode       string        // "closed", "invite-only", or "open-with-approval"
	globalPendingExpiry time.Duration // 0 = never auto-expire pending accounts
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
				// time.NewTimer + defer Stop() rather than time.After, so the
				// timer is released immediately on the auth-done / ctx-done
				// paths instead of lingering until d elapses (up to
				// AUTH_TIMEOUT_SECONDS) after the goroutine has already exited.
				timer := time.NewTimer(d)
				defer timer.Stop()
				select {
				case <-timer.C:
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

func publicPasswordHandler(db *store.Store, regMode string) ssh.PasswordHandler {
	return func(ctx ssh.Context, password string) bool {
		username := ctx.User()

		// "new" is the registration entry point. Accept it when registration
		// is not closed; store the mode for teaHandler to pick up.
		if strings.EqualFold(username, "new") {
			if regMode == "closed" {
				log.Printf("public auth: registration rejected (closed mode) from %s", ctx.RemoteAddr())
				return false
			}
			ctx.SetValue(regModeKey, regMode)
			log.Printf("public auth: registration from %s (mode=%s)", ctx.RemoteAddr(), regMode)
			return true
		}

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
		// Auto-lift expired timed bans before normal auth proceeds.
		if user.Status == "suspended" {
			if lifted, _ := db.CheckAndLiftExpiredBan(user.ID); lifted {
				user.Status = "active"
			}
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
		if user.Status == "suspended" {
			if lifted, _ := db.CheckAndLiftExpiredBan(user.ID); lifted {
				user.Status = "active"
			}
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

// bootstrapAdminAccount lets Docker/Unraid operators create the first admin
// account via env vars instead of `docker exec ... /adduser` — the final
// image is distroless (no shell), so Unraid's WebUI "Console" button can't
// reach it. Read directly via os.Getenv rather than through loadConfig's
// config struct, since loadConfig logs every field of it and this must
// never appear in a log line.
//
// Gated on zero *usable* admins (CountUsableAdmins), not zero accounts,
// which also makes this a deliberate emergency-recovery lever covering two
// distinct scenarios: every admin account deleted (fresh CreateUser path
// below), or every admin account banned but still present (the recovery
// path below — resets the matching account's password and lifts its ban).
// Docker/Unraid has no other recovery path for either case (docs/admin-
// guide.md's bare-metal emergency procedures don't reach a shell-less
// image), so this is intentional, not an oversight — operators should
// clear both vars after first login only if they're confident they won't
// need that lever.
func bootstrapAdminAccount(db *store.Store) {
	username := os.Getenv("BOOTSTRAP_ADMIN_USERNAME")
	password := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")

	if username == "" && password == "" {
		return
	}
	if username == "" || password == "" {
		log.Fatalln("config: BOOTSTRAP_ADMIN_USERNAME and BOOTSTRAP_ADMIN_PASSWORD must both be set together (or both left unset)")
	}
	if strings.EqualFold(username, "new") {
		log.Fatalln(`config: BOOTSTRAP_ADMIN_USERNAME cannot be "new" — that username is reserved for self-registration and could never log in`)
	}

	usable, err := db.CountUsableAdmins()
	if err != nil {
		log.Fatalf("bootstrap admin: counting usable admins: %v", err)
	}
	if usable > 0 {
		log.Printf("bootstrap admin: BOOTSTRAP_ADMIN_USERNAME/PASSWORD set but ignored — %d usable admin account(s) already exist", usable)
		return
	}

	existing, err := db.GetUserByUsername(username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No exact match — but before creating a fresh account, check
		// whether an existing account matches under a different case.
		// Usernames aren't case-insensitive-unique in this schema, so an
		// exact-match miss here doesn't mean "no such account" as
		// unambiguously as it does elsewhere; silently creating a
		// look-alike duplicate would leave the real (differently-cased)
		// account exactly as locked-out as before, with no error to
		// signal anything went differently than intended.
		if ciMatch, ciErr := db.GetUserByUsernameCI(username); ciErr == nil {
			log.Fatalf("bootstrap admin: BOOTSTRAP_ADMIN_USERNAME %q does not exactly match any account, but differs only in case from existing account %q (role=%s) — refusing to guess whether these are the same account; set BOOTSTRAP_ADMIN_USERNAME to match the stored username's exact case to recover it", username, ciMatch.Username, ciMatch.Role)
		} else if !errors.Is(ciErr, store.ErrNotFound) {
			log.Fatalf("bootstrap admin: checking for a case-insensitive match on %q: %v", username, ciErr)
		}

		hash, herr := auth.HashPassword(password)
		if herr != nil {
			log.Fatalf("bootstrap admin: hashing password: %v", herr)
		}
		if _, err := db.CreateUser(username, hash, "admin"); err != nil {
			log.Fatalf("bootstrap admin: creating account %q: %v", username, err)
		}
		log.Printf("bootstrap admin: created initial admin account %q", username)

	case err != nil:
		log.Fatalf("bootstrap admin: looking up account %q: %v", username, err)

	case existing.Role != "admin":
		log.Fatalf("bootstrap admin: account %q already exists with role %q — refusing to touch a non-admin account; choose a different BOOTSTRAP_ADMIN_USERNAME", username, existing.Role)

	case existing.Status != "suspended":
		// "active" is unreachable here (CountUsableAdmins would have
		// counted it). "pending" should never occur for role=admin.
		// Either way, fail loud rather than guess.
		log.Fatalf("bootstrap admin: account %q is an admin but has status %q, not %q — refusing to modify it automatically", username, existing.Status, "suspended")

	default: // status == "suspended": the intended recovery case
		hash, herr := auth.HashPassword(password)
		if herr != nil {
			log.Fatalf("bootstrap admin: hashing password: %v", herr)
		}
		if err := db.SetPassword(username, hash); err != nil {
			log.Fatalf("bootstrap admin: resetting password for %q: %v", username, err)
		}
		if err := db.UnbanUser(username); err != nil {
			log.Fatalf("bootstrap admin: reactivating %q: %v", username, err)
		}
		log.Printf("bootstrap admin: recovered admin account %q (password reset, ban lifted)", username)
	}
}
