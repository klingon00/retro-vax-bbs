// Command server is the Retro VAX-BBS SSH entrypoint.
//
// Runs two SSH listeners on separate ports with a symmetric role-based
// partition:
//   - Public listener  — refuses admin-role accounts before checking
//     the password. Intended for eventual internet exposure once an
//     operator has set up appropriate network controls.
//   - Admin listener   — refuses non-admin accounts. Never published;
//     reachable only via the operator's VPN tunnel (WireGuard,
//     Tailscale, etc. — entirely the operator's setup).
//
// Enforcement is by network binding, not IP matching: the listeners bind
// to different addresses, so the separation cannot be fooled by spoofed
// source IPs or proxy headers.
//
// Both listeners share one SSH host key (identifies the server to
// clients, not clients to the server — sharing it between ports does
// not weaken the admin boundary).
//
// SECURITY: the dual-listener split is now implemented. Per the design
// doc, the remaining gate before safe internet exposure is operator
// network setup (don't forward the public port until you're ready, and
// never forward the admin port). Both listeners still default to
// localhost for local development.
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
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

const (
	// One host key shared by both listeners — identifies the server to
	// clients (MITM prevention), not clients to the server.
	hostKeyPath = "data/ssh_host_ed25519"
	dbPath      = "data/retro-vax-bbs.db"

	// dummyHash is verified against on fast-exit paths (user not found,
	// wrong-listener role check, inactive account) so every rejection
	// costs the same argon2id computation regardless of why. Prevents
	// username/role enumeration via response-time differences.
	dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$PBeQih8r5fJuNB0J6vk/XA$VT14oAs2u5DJILc+W5E+VwUpB17pcNC33Em2HeHt054"
)

// config holds all server settings resolved from environment variables.
type config struct {
	// Public listener — the port users connect to.
	publicHost string
	publicPort string

	// Admin listener — never published; operator routes to it via VPN.
	adminHost string
	adminPort string

	// Rate limiting applies independently to each listener.
	rateLimitPerMinute float64
	rateLimitBurst     int
	rateLimitMaxIPs    int
}

// loadConfig reads configuration from environment variables with safe
// defaults. Logs the effective config at startup.
//
// Environment variables:
//
//	SSH_HOST              — public listener bind host   (default: localhost)
//	SSH_PORT              — public listener bind port   (default: 2222)
//	ADMIN_HOST            — admin listener bind host    (default: localhost)
//	ADMIN_PORT            — admin listener bind port    (default: 2223)
//	RATELIMIT_PER_MINUTE  — connections/min per IP      (default: 1)
//	RATELIMIT_BURST       — burst allowance             (default: 5)
//	RATELIMIT_MAX_IPS     — IPs tracked in LRU cache    (default: 1000)
//
// In production the operator sets ADMIN_HOST to a VPN interface address
// (e.g. a WireGuard or Tailscale IP) so the admin port is reachable
// only from the tunnel. SSH_HOST can be set to 0.0.0.0 once the
// operator is ready to expose the public port.
func loadConfig() config {
	c := config{
		publicHost:         envOr("SSH_HOST", "localhost"),
		publicPort:         envOr("SSH_PORT", "2222"),
		adminHost:          envOr("ADMIN_HOST", "localhost"),
		adminPort:          envOr("ADMIN_PORT", "2223"),
		rateLimitPerMinute: envFloat("RATELIMIT_PER_MINUTE", 1.0),
		rateLimitBurst:     envInt("RATELIMIT_BURST", 5),
		rateLimitMaxIPs:    envInt("RATELIMIT_MAX_IPS", 1000),
	}
	log.Printf("config: public=%s admin=%s ratelimit=%.1f/min burst=%d maxIPs=%d",
		net.JoinHostPort(c.publicHost, c.publicPort),
		net.JoinHostPort(c.adminHost, c.adminPort),
		c.rateLimitPerMinute, c.rateLimitBurst, c.rateLimitMaxIPs)
	return c
}

func main() {
	cfg := loadConfig()

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalln("opening database:", err)
	}
	defer db.Close()

	// Each listener gets its own rate limiter — independent token
	// buckets, independent LRU caches. An attacker hammering the public
	// port doesn't consume the admin port's burst budget, and vice versa.
	publicLimiter := newLimiter(cfg)
	adminLimiter := newLimiter(cfg)

	publicSrv, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(cfg.publicHost, cfg.publicPort)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPasswordAuth(publicPasswordHandler(db)),
		wish.WithMiddleware(
			recovermw.Middleware(bm.Middleware(teaHandler)),
			rl.Middleware(publicLimiter),
			lm.StructuredMiddleware(),
		),
	)
	if err != nil {
		log.Fatalln("creating public server:", err)
	}

	adminSrv, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(cfg.adminHost, cfg.adminPort)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPasswordAuth(adminPasswordHandler(db)),
		wish.WithMiddleware(
			recovermw.Middleware(bm.Middleware(teaHandler)),
			rl.Middleware(adminLimiter),
			lm.StructuredMiddleware(),
		),
	)
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

	// Shut both down concurrently — no reason to serialize, and we want
	// the 10-second grace period to apply to both together, not each in
	// sequence.
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

// newLimiter constructs a rate limiter from config. Called once per
// listener so each has an independent token bucket.
func newLimiter(cfg config) rl.RateLimiter {
	return rl.NewRateLimiter(
		rate.Every(time.Duration(float64(time.Minute)/cfg.rateLimitPerMinute)),
		cfg.rateLimitBurst,
		cfg.rateLimitMaxIPs,
	)
}

// publicPasswordHandler authenticates users on the public listener.
// Rejects admin-role accounts before checking the password — an attacker
// who correctly guesses an admin password gets the same rejection as
// someone with a wrong password, so the public listener leaks nothing
// about whether a guessed admin password was correct.
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

		// Role check before password — admin accounts are not permitted
		// on the public listener regardless of password correctness.
		if user.Role == "admin" {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("public auth failure: admin account %q rejected on public listener from %s",
				username, ctx.RemoteAddr())
			return false
		}

		return completeAuth(db, user, password, "public", ctx)
	}
}

// adminPasswordHandler authenticates users on the admin listener.
// Rejects non-admin accounts — mirrors the public listener's logic,
// symmetric partition by role.
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

		// Non-admin accounts are not permitted on the admin listener.
		if user.Role != "admin" {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("admin auth failure: non-admin account %q rejected on admin listener from %s",
				username, ctx.RemoteAddr())
			return false
		}

		return completeAuth(db, user, password, "admin", ctx)
	}
}

// completeAuth runs the shared checks (status, lockout, password
// verification, counter management) that apply identically on both
// listeners once the role gate has passed. The listener label is used
// only for log messages.
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
	log.Printf("%s auth success: %q from %s", listener, username, ctx.RemoteAddr())
	return true
}

// teaHandler builds the per-session lobby Model. Shared by both
// listeners — the lobby itself has no concept of which port the session
// arrived on, and doesn't need to.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	_, _, active := s.Pty()
	if !active {
		return nil, nil
	}
	m := lobby.New(s.User())
	return m, []tea.ProgramOption{tea.WithAltScreen()}
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
