# Retro VAX-BBS

A modern, self-hosted, retro VAX/VMS-style multi-user shell, built on
`wish` + `bubbletea` + `lipgloss` over SSH. See `docs/design-doc.md` and
`docs/open-questions.md` for the full architecture and decision history —
keep those up to date as design decisions get made; this README is just
the "how do I run this" doc.

## What this is (and isn't)

This is a hobbyist homage to early-1980s/90s DEC VAX/VMS terminal and BBS
culture — the command-line aesthetic, the PHONE utility, the
campus-cluster feel. It is **not** affiliated with, endorsed by, or
representing VMS Software, Inc. (VSI) or Hewlett Packard Enterprise
(HPE), who develop and support the actively-maintained, commercially
licensed OpenVMS operating system today. No code, trademarks, or
proprietary material from OpenVMS is used here — this is an independent,
non-commercial fan project, built from scratch in Go. The project is
named for VAX specifically (rather than VMS/OpenVMS) because VAX
hardware and branding have been discontinued for decades with no current
commercial product behind them, unlike OpenVMS, which remains actively
sold and developed.

## Status

Per the build order in `docs/open-questions.md`:

- [x] Project scaffolding
- [x] Lobby shell / command dispatcher
- [ ] Account & auth
  - [x] SQLite schema + argon2id password hashing
  - [x] Closed-mode (admin-created) accounts + real `wish` login auth
  - [ ] Registration modes (invite-only / open-with-approval)
  - [x] Account lockout
  - [x] Per-IP rate limiting
- [ ] Dual-listener split (public / admin)
- [ ] `WHO` / `FINGER` (real implementation — registry-backed)
- [ ] PHONE app
- [ ] Docker packaging

## ⚠️ Security status — read before running anywhere but your laptop

**Real password authentication, account lockout, and per-IP rate limiting
exist now** (argon2id, checked against SQLite-stored accounts; 5 failed
attempts locks the account for 15 minutes; connection rate limited to 1
sustained/min per IP with a burst of 5), but **the dual-listener
public/admin split is not implemented yet.** The server binds to
`localhost:2222` specifically so this is safe for local development —
**do not** change that to `0.0.0.0` or forward a port to it until the
listener split lands.

## Running it

First, create at least one account — there's no self-service
registration yet, so this is the only way in:

```bash
go run ./cmd/adduser -username YOURNAME -password 'pick-something-decent'
```

Then run the server:

```bash
go run ./cmd/server
# in another terminal:
ssh -p 2222 YOURNAME@localhost
```

You'll be prompted for the password you set above. Once in, try `HELP`,
`WHO`, `LOGOUT`. Resize your terminal mid-session — Bubble Tea picks up
`WindowSizeMsg` natively, which is the original VAX/VMS terminal-resize
problem, solved for free by the stack.

The SSH host key is generated on first run at `data/ssh_host_ed25519`
(0600 permissions, directory created at 0700), and the account database
lives alongside it at `data/retro-vax-bbs.db`. Both are gitignored —
don't commit either. If you delete the host key, your SSH client will
warn about a changed host key on next connect; that's expected for a dev
box, just remove the old entry from your `known_hosts`.

## Configuration

The server reads configuration from environment variables, with safe
defaults for local development. Set these to tune behaviour for your
deployment:

| Variable | Default | Description |
|---|---|---|
| `SSH_HOST` | `localhost` | Bind host |
| `SSH_PORT` | `2222` | Bind port |
| `RATELIMIT_PER_MINUTE` | `1` | New connections per minute per IP |
| `RATELIMIT_BURST` | `5` | Burst allowance before rate kicks in |
| `RATELIMIT_MAX_IPS` | `1000` | Number of IPs to track simultaneously |

The burst default of 5 is intentional — concurrent sessions from one
account (e.g. PHONE in one window, mail in another) are a core feature,
and opening a few sessions in quick succession shouldn't trigger the
limiter for a legitimate user.

## Module path

This project's module path is `github.com/klingon00/retro-vax-bbs`. If
you ever need to rename it again (new GitHub username, fork, etc.):

```bash
go mod edit -module github.com/NEWUSER/NEWREPO
grep -rl 'github.com/klingon00/retro-vax-bbs' --include='*.go' . \
  | xargs sed -i 's#github.com/klingon00/retro-vax-bbs#github.com/NEWUSER/NEWREPO#g'
go build ./...
```

## License

MIT — see `LICENSE`. All current dependencies (Charm's `wish` /
`bubbletea` / `lipgloss` / `log` / `keygen`, Go's own `golang.org/x/*`
packages, and `modernc.org/sqlite`) are MIT or BSD-3-Clause; none impose
any additional obligations. Before public release, consider adding a
third-party notices file listing dependency licenses — good practice,
not yet done.

## Project layout

```
cmd/server/        — entrypoint: wish server setup, middleware chain, auth wiring
cmd/adduser/        — CLI tool to seed admin-created accounts (closed registration mode)
internal/lobby/     — the command-loop shell (Bubble Tea model + dispatch)
internal/app/       — the modular app interface future apps (PHONE, mail) implement
internal/auth/       — argon2id password hashing
internal/store/     — SQLite-backed account persistence
docs/                — design doc + open questions, copied in for git history
```
