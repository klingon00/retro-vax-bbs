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
  - [ ] Account lockout
  - [ ] Per-IP rate limiting
- [ ] Dual-listener split (public / admin)
- [ ] `WHO` / `FINGER` (real implementation — registry-backed)
- [ ] PHONE app
- [ ] Docker packaging

## ⚠️ Security status — read before running anywhere but your laptop

**Real password authentication exists now** (argon2id, checked against
SQLite-stored accounts), but **account lockout, per-IP rate limiting,
and the dual-listener public/admin split are not implemented yet.** A
user can retry a wrong password indefinitely with no lockout, and
there's no protection against connection flooding. The server binds to
`localhost:2222` specifically so this is safe for local development —
**do not** change that to `0.0.0.0` or forward a port to it until
lockout and rate limiting land.

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
