# Retro VAX-BBS

A modern, self-hosted, retro VAX/VMS-style multi-user shell, built on
`wish` + `bubbletea` + `lipgloss` over SSH. See `docs/design-doc.md` and
`docs/open-questions.md` for the full architecture and decision history —
keep those up to date as design decisions get made; this README is just
the "how do I run this" doc. For operating a live instance, see
`docs/admin-guide.md`.

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
- [x] Account & auth
  - [x] SQLite schema + argon2id password hashing
  - [x] Closed-mode (admin-created) accounts + real `wish` login auth
  - [x] Registration modes (invite-only / open-with-approval)
  - [x] Account lockout
  - [x] Per-IP rate limiting
- [x] Dual-listener split (public / admin)
- [x] `WHO` / `FINGER`
  - [x] `WHO` (registry-backed, with alias `SHOW USERS`)
  - [x] `FINGER <user>`
- [x] PHONE app — v1 complete
- [x] Admin commands (APPROVE, DENY, KICK, BAN, UNBAN, UNLOCK, DELETE USER, RESET PASSWORD, EXPIRE PASSWORD, LIST USERS, LIST PENDING, INVITE CREATE)
- [x] SET PLAN / SET PLAN CLEAR
- [x] SET PASSWORD (self-service password change)
- [x] Lobby HELP expansion
- [x] Lobby scrollback (PgUp / PgDn / End)
- [x] Docker packaging

## ⚠️ Security status — read before running anywhere but your laptop

**Real password authentication, account lockout, per-IP rate limiting,
and the dual-listener public/admin split exist now** (argon2id, checked
against SQLite-stored accounts; 5 failed attempts locks the account for
15 minutes; connection rate limited to 1 sustained/min per IP with a
burst of 5), but **the server still binds to localhost by default.**

To expose the public port safely: set `SSH_HOST=0.0.0.0` (or a specific
interface) and forward `SSH_PORT` to the internet. Set `ADMIN_HOST` to a
VPN interface address (WireGuard/Tailscale) and **never** forward
`ADMIN_PORT` to the internet — the VPN is the gate.

See `docs/admin-guide.md` for the full security hardening checklist and
deployment guidance.

## Running it

For `closed` mode (default), create accounts with `cmd/adduser` first.
On a fresh clone, `data/` doesn't exist yet (it's gitignored) — create it
before the first run, or `adduser`/`server` will fail to open the database:

```bash
mkdir -p data

# Regular user account
go run ./cmd/adduser -username alice -password 'pick-something-decent'

# Admin account (connects via the admin listener only)
go run ./cmd/adduser -username sysop -password 'admin-password' -role admin
```

Then run the server:

```bash
go run ./cmd/server
```

Two listeners start: the public one on port 2222, the admin one on 2223.

```bash
# Regular user — public listener
ssh -p 2222 alice@localhost

# Admin — admin listener only (will be rejected on port 2222)
ssh -p 2223 sysop@localhost
```

Try `HELP`, `WHO`, `SHOW USERS`, `FINGER <username>`, `TIME`, `SHOW TIME`,
`PHONE <username>`, `SET PLAN`, `SET PASSWORD`, `LOGOUT`. Resize your terminal mid-session
— Bubble Tea picks up `WindowSizeMsg` natively, which is the original
VAX/VMS terminal-resize problem, solved for free by the stack.

For `invite-only` or `open-with-approval` modes, users SSH in as the
special username `new` (any password) and are walked through a
registration TUI. See `docs/admin-guide.md` for full registration mode
documentation.

The SSH host key is generated on first run at `data/ssh_host_ed25519`
(0600 permissions, directory created at 0700), and the account database
lives alongside it at `data/retro-vax-bbs.db`. Both are gitignored —
don't commit either. Under Docker, `data/` is the container's `/data`
mount rather than a bare-metal relative path — see Docker / Unraid below.

## Configuration

The server reads configuration from environment variables, with safe
defaults for local development:

| Variable | Default | Description |
|---|---|---|
| `SSH_HOST` | `localhost` | Public listener bind host |
| `SSH_PORT` | `2222` | Public listener bind port |
| `ADMIN_HOST` | `localhost` | Admin listener bind host (set to VPN interface in production) |
| `ADMIN_PORT` | `2223` | Admin listener bind port (never forward to internet) |
| `RATELIMIT_PER_MINUTE` | `1` | New connections per minute per IP |
| `RATELIMIT_BURST` | `5` | Burst allowance before rate kicks in |
| `RATELIMIT_MAX_IPS` | `1000` | Number of IPs to track simultaneously |
| `AUTH_TIMEOUT_SECONDS` | `120` | Seconds before unauthenticated connections are closed (0 to disable) |
| `REGISTRATION_MODE` | `closed` | Account registration: `closed`, `invite-only`, or `open-with-approval` |
| `PENDING_EXPIRY_DAYS` | `7` | Days before unreviewed pending accounts are auto-deleted (0 to disable) |

The burst default of 5 is intentional — concurrent sessions from one
account (e.g. PHONE in one window, checking WHO in another) are a core
feature, and opening a few sessions in quick succession shouldn't trigger
the limiter for a legitimate user.

## Docker / Unraid

A `Dockerfile` and `docker-compose.yml` are provided at the repo root, and
an Unraid Community Applications template is at `unraid-template.xml`.
This section is a quick-start only — for full deployment and security
guidance (especially around the admin listener), see the Docker/Unraid
section of `docs/admin-guide.md` before exposing anything to a real
network.

```bash
docker compose up -d
```

Pre-built images are published to `ghcr.io/klingon00/retro-vax-bbs` on each
version tag push. Note the tag naming: pushing git tag `v0.3.0` publishes
the image as `0.3.0` — **no `v` prefix** (the publish workflow strips it via
`${GITHUB_REF_NAME#v}`). Pull by the bare version, not the git tag:

```bash
docker pull ghcr.io/klingon00/retro-vax-bbs:0.3.0
```

See [GitHub Releases](https://github.com/klingon00/retro-vax-bbs/releases)
for version history and upgrade notes before pulling an older tag.

The image bakes in `SSH_HOST=0.0.0.0` so the public listener is reachable
out of the box. `ADMIN_HOST` is deliberately **not** given a container
default — how you restrict the admin listener (Tailscale, WireGuard,
VLAN, Docker host-networking, etc.) is your call. The provided
`docker-compose.yml` sets `ADMIN_HOST=0.0.0.0`, which looks unsafe but
isn't: in Docker's default bridge network mode it's required just for the
admin listener to be reachable at all (the app's own `localhost` default
binds to the container's own loopback, unreachable via Docker's port
forwarding), and it provides no security by itself either way — that
comes entirely from scoping the admin port's host-IP mapping. Read the
admin-guide Docker/Unraid section before forwarding port 2223 anywhere.

Bootstrap the first admin account one of two ways:

- **Unraid / WebUI-only setups**: set `BOOTSTRAP_ADMIN_USERNAME` and
  `BOOTSTRAP_ADMIN_PASSWORD` as container variables (both, or leave both
  blank) and start the container — it creates the account itself on first
  boot. This exists specifically because the final image has no shell, so
  Unraid's WebUI "Console" button can't reach it for a `docker exec`. See
  `docs/admin-guide.md`'s Docker/Unraid section for the recovery-lever
  caveat and why you should clear both fields once you've confirmed login.
- **Compose / terminal access**: the same way the bare-metal quick-start
  does, just via `docker exec`:

```bash
docker exec -it retro-vax-bbs /adduser -username sysop -password '<strong-password>' -role admin
```

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
`bubbletea` / `lipgloss` / `log` / `keygen` / `bubbles`, Go's own
`golang.org/x/*` packages, and `modernc.org/sqlite`) are MIT or
BSD-3-Clause; none impose any additional obligations. Before public
release, consider adding a third-party notices file listing dependency
licenses — good practice, not yet done.

## Project layout

```
cmd/server/            — entrypoint: dual SSH listeners, middleware chain, auth wiring
cmd/adduser/           — CLI tool to seed admin-created accounts (closed registration mode)
internal/lobby/        — the command-loop shell (Bubble Tea model + dispatch)
internal/app/          — the modular app interface future apps (PHONE, mail) implement
internal/auth/         — argon2id password hashing
internal/phone/        — PHONE app (app.go, call.go, layout.go)
internal/registration/ — self-service registration TUI (invite-only / open-with-approval)
internal/registry/     — session registry for WHO and PHONE routing
internal/setplan/      — SET PLAN inline textarea editor (setplan.go, app.go)
internal/setpassword/  — SET PASSWORD / RESET PASSWORD / EXPIRE PASSWORD flows
internal/store/        — SQLite-backed account and invite persistence
docs/                  — design doc, open questions, admin guide
```

## Contributing

This repo ships two git hooks — `scripts/pre-commit` and
`scripts/commit-msg` — that block a commit if its staged content, added/
renamed file paths, or message contain a word from a local blocklist file
you create yourself. Git hooks aren't tracked, so install them once per
clone:

```bash
ln -s ../../scripts/pre-commit .git/hooks/pre-commit
ln -s ../../scripts/commit-msg .git/hooks/commit-msg
```

(Symlinking, not copying, matters here — both hooks resolve a shared
checker script at `scripts/lib/check-blocklist.sh` relative to the repo
root, and a symlink keeps that resolvable regardless of how `.git/hooks/`
references it.)

Then create `.git/hooks/pre-commit-blocklist` — one blocked word per line,
`#` comments allowed — with whatever you personally want kept out of this
public repo's history. That file is deliberately never tracked: it lives
entirely under `.git/hooks/`, which `git add` can never touch, and the
hook scripts themselves contain zero information about what they block —
not even a hash, since hashing a short/common word is trivially reversible
and wouldn't actually hide anything. Without this file, both hooks print a
warning and allow the commit anyway (the correct default for anyone else
cloning this repo, who has no reason to have this configured).

**Known limitation**: these are client-side hooks. They don't run for
`git rebase` or `git cherry-pick`, and `pre-commit` specifically doesn't
run for merge commits at all (only `commit-msg` does, checking the merge
commit's own message, not the content it merges in) — they're a safety net
for normal commits, not a substitute for reviewing history-rewriting
operations by hand.
