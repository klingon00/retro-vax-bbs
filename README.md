# Retro VMS-BBS

A modern, self-hosted, retro VMS/BBS-style multi-user shell, built on
`wish` + `bubbletea` + `lipgloss` over SSH. "Retro" is doing real work in
the name — this is a love letter to early-90s VAX/VMS terminal culture,
not an attempt to reimplement OpenVMS itself. See `docs/design-doc.md`
and `docs/open-questions.md` for the full architecture and decision
history — keep those up to date as design decisions get made; this
README is just the "how do I run this" doc.

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

This project's module path is `github.com/YOURNAME/retro-vms-bbs`. If
you ever need to rename it again (new GitHub username, fork, etc.):

```bash
go mod edit -module github.com/NEWUSER/NEWREPO
grep -rl 'github.com/YOURNAME/retro-vms-bbs' --include='*.go' . \
  | xargs sed -i 's#github.com/YOURNAME/retro-vms-bbs#github.com/NEWUSER/NEWREPO#g'
go build ./...
```

## A note on `go.mod`'s `replace` directives

This project was originally scaffolded in a sandboxed environment
without access to `proxy.golang.org` or `sum.golang.org` (the default Go
module proxy and checksum database). To fetch dependencies anyway,
`go.mod` contains `replace` directives that redirect `golang.org/x/*`
packages to their `github.com/golang/*` mirrors, fetched directly via
git rather than through the proxy, with `GOSUMDB=off`.

**What this means for you:**

- The `go.sum` in this repo has hashes that were computed locally but
  **never cross-checked against the public sumdb transparency log**. For
  well-known dependencies from the official Charm and Go orgs the
  practical risk is low, but it's not the same guarantee a normal `go
  get` gives you.
- On your own machine, with normal internet access, run:
```bash
  GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod tidy
```
  This re-resolves everything through the real proxy and sumdb. If it
  completes without error and `git diff go.sum` shows only hash
  additions (not changes to existing entries), you're in good shape —
  that confirms what was fetched here matches what the canonical proxy
  serves. At that point you can also delete the `replace` lines for
  `golang.org/x/sys`, `x/crypto`, `x/sync`, `x/text`, and `x/exp` at the
  top of `go.mod` — they're a workaround for that sandbox's network
  restrictions, not a permanent project requirement.

## Project layout

```
cmd/server/        — entrypoint: wish server setup, middleware chain
internal/lobby/     — the command-loop shell (Bubble Tea model + dispatch)
internal/app/       — the modular app interface future apps (PHONE, mail) implement
docs/                — design doc + open questions, copied in for git history
```