// Command server is the Retro VAX-BBS SSH entrypoint.
//
// SECURITY: real password authentication, account lockout, and per-IP
// rate limiting are now implemented. The dual-listener public/admin
// split is NOT yet implemented. The server binds to localhost by default
// and should not be exposed on a network until the listener split lands.
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
	hostKeyPath = "data/ssh_host_ed25519"
	dbPath      = "data/retro-vax-bbs.db"

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

// config holds server settings resolved from environment variables.
// All fields have safe defaults — running with no env vars set produces
// a working, reasonably secured server without any extra configuration.
// This maps cleanly onto the Unraid Community Apps deployment model,
// where operators set env vars through the template UI rather than
// editing config files.
type config struct {
	host string
	port string

	// Rate limiting — per IP, applied at the session level (after TCP
	// accept, before the lobby). Controls connection frequency, not
	// bandwidth.
	rateLimitPerMinute float64
	rateLimitBurst     int
	rateLimitMaxIPs    int
}

// loadConfig reads configuration from environment variables, falling
// back to defaults for anything not set. Logs the effective config at
// startup so the operator can confirm what is actually running.
//
// Environment variables:
//
//	SSH_HOST                  — bind host (default: localhost)
//	SSH_PORT                  — bind port (default: 2222)
//	RATELIMIT_PER_MINUTE      — new connections/min per IP (default: 1)
//	RATELIMIT_BURST           — burst allowance (default: 5)
//	RATELIMIT_MAX_IPS         — IPs to track in LRU cache (default: 1000)
//
// On RATELIMIT_BURST default of 5: concurrent sessions from a single
// account are a core feature (PHONE in one window, mail in another —
// true to the original VAX/VMS cluster experience). A burst of 5 gives
// a real user room to open several sessions in quick succession without
// hitting the limiter. A brute-forcer firing hundreds of connections per
// minute will still be stopped by the sustained rate.
func loadConfig() config {
	c := config{
		host:               envOr("SSH_HOST", "localhost"),
		port:               envOr("SSH_PORT", "2222"),
		rateLimitPerMinute: envFloat("RATELIMIT_PER_MINUTE", 1.0),
		rateLimitBurst:     envInt("RATELIMIT_BURST", 5),
		rateLimitMaxIPs:    envInt("RATELIMIT_MAX_IPS", 1000),
	}
	log.Printf("config: host=%s port=%s ratelimit=%.1f/min burst=%d maxIPs=%d",
		c.host, c.port, c.rateLimitPerMinute, c.rateLimitBurst, c.rateLimitMaxIPs)
	return c
}

func main() {
	cfg := loadConfig()

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalln("opening database:", err)
	}
	defer db.Close()

	// wish's ratelimiter middleware uses golang.org/x/time/rate (token
	// bucket) internally, keyed by remote IP address, backed by an LRU
	// cache so the memory footprint is bounded regardless of how many
	// distinct IPs connect over time.
	limiter := rl.NewRateLimiter(
		rate.Every(time.Duration(float64(time.Minute)/cfg.rateLimitPerMinute)),
		cfg.rateLimitBurst,
		cfg.rateLimitMaxIPs,
	)

	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(cfg.host, cfg.port)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPasswordAuth(passwordHandler(db)),
		wish.WithMiddleware(
			// Middleware runs outermost-first (last entry = first to run).
			// Order:
			//   1. lm.StructuredMiddleware() — logs connect/disconnect
			//   2. rl.Middleware()           — rate limit; fatal if exceeded
			//   3. recovermw.Middleware()    — session-level panic recovery
			//   4. bm.Middleware()           — runs the lobby tea.Program
			//
			// Rate limiting sits outside recover on purpose: a rate-limit
			// rejection should terminate the session immediately and cleanly,
			// not be caught and swallowed by the panic recovery layer.
			recovermw.Middleware(bm.Middleware(teaHandler)),
			rl.Middleware(limiter),
			lm.StructuredMiddleware(),
		),
	)
	if err != nil {
		log.Fatalln(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	log.Printf("starting SSH server on %s (auth: argon2id + lockout + rate limiting; dual-listener split not yet implemented)",
		s.Addr)
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

// envOr returns the value of the named environment variable, or
// fallback if the variable is not set or is empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt returns the named environment variable parsed as an integer,
// or fallback if the variable is unset, empty, or not a valid integer.
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

// envFloat returns the named environment variable parsed as a float64,
// or fallback if the variable is unset, empty, or not a valid number.
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

// passwordHandler returns a wish/ssh PasswordHandler closing over the
// store. Every rejection path returns false and is logged with the same
// shape. Lockout is enforced: a locked account is rejected before
// password verification even runs, and failed_attempts is incremented on
// every wrong password, clearing on success.
func passwordHandler(db *store.Store) ssh.PasswordHandler {
	return func(ctx ssh.Context, password string) bool {
		username := ctx.User()

		user, err := db.GetUserByUsername(username)
		if errors.Is(err, store.ErrNotFound) {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("auth failure: unknown user %q from %s", username, ctx.RemoteAddr())
			return false
		}
		if err != nil {
			log.Printf("auth error looking up %q from %s: %v", username, ctx.RemoteAddr(), err)
			return false
		}

		if user.Status != "active" {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("auth failure: account %q not active (status=%s) from %s", username, user.Status, ctx.RemoteAddr())
			return false
		}

		if user.LockedUntil.Valid && user.LockedUntil.Time.After(time.Now()) {
			_, _ = auth.VerifyPassword(password, dummyHash)
			log.Printf("auth failure: account %q locked until %s, from %s",
				username, user.LockedUntil.Time.Format(time.RFC3339), ctx.RemoteAddr())
			return false
		}

		if !user.PasswordHash.Valid {
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
			if err := db.RecordFailedAttempt(user.ID); err != nil {
				log.Printf("auth: error recording failed attempt for %q: %v", username, err)
			}
			log.Printf("auth failure: wrong password for %q from %s", username, ctx.RemoteAddr())
			return false
		}

		if err := db.ClearFailedAttempts(user.ID); err != nil {
			log.Printf("auth: error clearing failed attempts for %q: %v", username, err)
		}
		log.Printf("auth success: %q from %s", username, ctx.RemoteAddr())
		return true
	}
}

// teaHandler builds the per-session lobby Model. wish gives every
// connected session its own tea.Program, so there is no shared mutable
// state between sessions here by construction.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	_, _, active := s.Pty()
	if !active {
		return nil, nil
	}
	m := lobby.New(s.User())
	return m, []tea.ProgramOption{tea.WithAltScreen()}
}
