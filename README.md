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

This is the initial scaffold: a single SSH listener, the lobby command
loop, and three commands (`HELP`, `WHO` stub, `LOGOUT`). Per the build
order in `docs/open-questions.md`:

- [x] Project scaffolding
- [x] Lobby shell / command dispatcher
- [ ] Account & auth (registration modes, argon2id, lockout, rate limiting)
- [ ] Dual-listener split (public / admin)
- [ ] `WHO` / `FINGER` (real implementation — registry-backed)
- [ ] PHONE app
- [ ] Docker packaging

## ⚠️ Security status — read before running anywhere but your laptop

**There is no authentication yet.** `wish.NewServer` with no auth handler
configured accepts any username/password. The server binds to
`localhost:2222` specifically so this is safe for local development —
**do not** change that to `0.0.0.0` or forward a port to it until the
auth milestone above is done.

## Running it

```bash
go run ./cmd/server
# in another terminal:
ssh -p 2222 anyusername@localhost
```

Try `HELP`, `WHO`, `LOGOUT`. Resize your terminal mid-session — Bubble
Tea picks up `WindowSizeMsg` natively, which is the original VAX/VMS
terminal-resize problem, solved for free by the stack.

The SSH host key is generated on first run at `data/ssh_host_ed25519`
(0600 permissions, directory created at 0700). It's gitignored — don't
commit it. If you delete it, your SSH client will warn about a changed
host key on next connect; that's expected for a dev box, just remove the
old entry from your `known_hosts`.

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
`bubbletea` / `lipgloss` / `log` / `keygen`, and Go's own `golang.org/x/*`
packages) are MIT or BSD-3-Clause; none impose any additional
obligations. Before public release, consider adding a third-party
notices file listing dependency licenses — good practice, not yet done.

## Project layout

```
cmd/server/        — entrypoint: wish server setup, middleware chain
internal/lobby/     — the command-loop shell (Bubble Tea model + dispatch)
internal/app/       — the modular app interface future apps (PHONE, mail) implement
docs/                — design doc + open questions, copied in for git history
```
