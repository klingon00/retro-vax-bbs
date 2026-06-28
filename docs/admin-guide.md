# Retro VAX-BBS — Administrator Guide

This guide covers everything needed to operate a Retro VAX-BBS instance:
initial setup, configuration, account management, moderation, and emergency
procedures.

For architecture decisions and design rationale, see `design-doc.md`. For
implementation notes and known issues, see `open-questions.md`.

---

## Quick-start checklist

1. Build the binary: `go build ./cmd/server`
2. Create at least one admin account: `go run ./cmd/adduser -username sysop -password '<strong-password>' -role admin`
3. Set environment variables for your deployment (see Configuration below)
4. Run the server: `go run ./cmd/server` (or the compiled binary)
5. Connect to the admin listener to verify: `ssh -p 2223 sysop@localhost`

---

## Listeners and network exposure

The server runs **two SSH listeners** with a symmetric role-based partition:

| Listener | Default port | Accepts | Rejects |
|---|---|---|---|
| Public | `SSH_PORT` (2222) | `user`-role accounts | `admin`-role accounts |
| Admin | `ADMIN_PORT` (2223) | `admin`-role accounts | `user`-role accounts |

**The admin listener must never be forwarded to the internet.** Bind
`ADMIN_HOST` to a VPN interface (WireGuard, Tailscale, etc.) and only
forward `SSH_PORT` publicly. The public listener rejects admin accounts
before even checking the password — right-password-wrong-listener and
wrong-password produce identical responses.

---

## Configuration reference

All configuration is via environment variables. Safe defaults for local
development are built in; set these for any public-facing deployment.

### Network

| Variable | Default | Description |
|---|---|---|
| `SSH_HOST` | `localhost` | Public listener bind address. Set to `0.0.0.0` or a specific interface for internet-facing deployments. |
| `SSH_PORT` | `2222` | Public listener port. |
| `ADMIN_HOST` | `localhost` | Admin listener bind address. **Set to your VPN interface address in production. Never `0.0.0.0`.** |
| `ADMIN_PORT` | `2223` | Admin listener port. Never forward this to the internet. |

### Rate limiting

| Variable | Default | Description |
|---|---|---|
| `RATELIMIT_PER_MINUTE` | `1.0` | New SSH connections per minute per IP (sustained rate). |
| `RATELIMIT_BURST` | `5` | Burst allowance above the sustained rate. 5 supports a user opening a few sessions quickly (e.g., PHONE in one window, checking something in another) without triggering the limiter. |
| `RATELIMIT_MAX_IPS` | `1000` | Number of source IPs tracked simultaneously for rate limiting. |

### Authentication

| Variable | Default | Description |
|---|---|---|
| `AUTH_TIMEOUT_SECONDS` | `120` | Seconds before an unauthenticated connection is closed. Set to `0` to disable. Only applies during the pre-auth phase — authenticated sessions have no idle timeout. |

### Registration

| Variable | Default | Description |
|---|---|---|
| `REGISTRATION_MODE` | `closed` | Controls self-service account creation. See **Registration modes** below. Valid values: `closed`, `invite-only`, `open-with-approval`. |
| `PENDING_EXPIRY_DAYS` | `7` | Days before an unreviewed pending account is automatically deleted. Prevents username squatting on open-with-approval systems. Set to `0` to disable auto-expiry. |

---

## Registration modes

### `closed` (default)

Only the admin creates accounts using `cmd/adduser`. No self-service
registration. Correct for private systems where you know every user
personally and will hand them credentials directly.

```bash
# Create a regular user account
go run ./cmd/adduser -username alice -password 'their-password'

# Create another admin account
go run ./cmd/adduser -username sysop2 -password 'their-password' -role admin
```

### `invite-only`

Users SSH in as the special username `new` with any password. They are
presented with a registration form that asks for a username, an **invite
code**, and a password. A valid invite code activates the account
immediately — no admin approval step.

**When to use:** Private communities where you trust invited members. The
invite code acts as the approval; you distribute it out-of-band (email,
message, etc.) to people you want to let in.

**Generating invite codes:**

```
INVITE CREATE            — 1 use, no expiry
INVITE CREATE 5          — 5 uses, no expiry
INVITE CREATE 3 7d       — 3 uses, expires in 7 days
INVITE CREATE 1 24h      — 1 use, expires in 24 hours
LIST INVITES             — show all codes and remaining uses
```

Invite codes are generated in the format `word-word-NN` (e.g.,
`swift-oak-42`). They are short, memorable, and safe to communicate
verbally or in a message. The user enters the code inside the registration
TUI — they only need the server's address (IP or hostname) and the code;
no credentials in advance.

### `open-with-approval`

Users SSH in as `new` and submit a registration request with their desired
username, optional email address, and password. The account sits in
`pending` status until an admin reviews it.

**When to use:** Semi-public communities where you want to vet requests
but don't want to individually send invite codes. Equivalent to "request
to join" on many platforms.

**Squatting protection:** Pending accounts are automatically deleted after
`PENDING_EXPIRY_DAYS` (default 7) days if not reviewed. This prevents
malicious actors from squatting usernames indefinitely. Admins can also
`REJECT USER <username>` immediately to free a name.

---

## Admin command reference

Admin commands are only available on the admin listener. All admin actions
are logged with the admin's username regardless of visibility settings.

### Account approval

| Command | Description |
|---|---|
| `LIST PENDING` | Show all pending account requests with username, email, and submission date. |
| `APPROVE <username>` | Activate a pending account. The user can log in immediately with the password they set during registration. |
| `REJECT USER <username>` | Delete a pending account request. The username becomes available again. |

### Account maintenance

| Command | Description |
|---|---|
| `UNLOCK <username>` | Clear a login lockout (triggered after 5 consecutive failed password attempts; normally lifts after 15 minutes). |

### Moderation

| Command | Description |
|---|---|
| `KICK <username>` | Immediately disconnect a user's active session. They can reconnect right away. Use for troubleshooting or asking someone to reconnect. |
| `BAN <username> <duration>` | Suspend an account for the specified duration. Disconnects them if currently online. See duration format below. |
| `UNBAN <username>` | Lift a ban and restore the account to active. |

**Ban duration format:**

| Format | Example | Meaning |
|---|---|---|
| `Ns` | `30s` | 30 seconds |
| `Nm` | `15m` | 15 minutes |
| `Nh` | `2h` | 2 hours |
| `Nd` | `7d` | 7 days |
| `Nw` | `2w` | 2 weeks |
| `perm` | `perm` | Permanent (until manually unbanned) |

Timed bans auto-lift on the user's next login attempt after the duration
expires — no admin action needed.

### Invite code management

| Command | Description |
|---|---|
| `INVITE CREATE` | Generate a code with 1 use and no expiry. |
| `INVITE CREATE <N>` | Generate a code with N uses and no expiry. |
| `INVITE CREATE <N> <duration>` | Generate a code with N uses, expiring after the given duration (same format as BAN). |
| `LIST INVITES` | Show all invite codes, remaining uses, and expiry dates. |

---

## Notifications for admins

When a new account is registered (open-with-approval mode), admins receive
a notification in their lobby session:

```
%VAX-BBS-I-REG, alice has requested an account.
  Type LIST PENDING to review.
```

At login, if there are pending accounts awaiting review, the count is
shown in the welcome banner:

```
%VAX-BBS-I-PEND, 3 account registration(s) awaiting approval.
  Type LIST PENDING to review.
```

These are one-time notifications — they don't repeat or ring. Future
versions will add opt-in external push notifications (email, webhook) and
in-system mail.

---

## Host key and database

Both the SSH host key and the SQLite database live in the `data/`
directory, which is created automatically on first run:

```
data/
  ssh_host_ed25519       — SSH host key (0600 permissions)
  retro-vax-bbs.db       — SQLite database
```

Both are gitignored. **Back up `data/` before any major upgrade.**

The database schema migrates automatically at startup using `ALTER TABLE
ADD COLUMN` (additive only). No data is lost on upgrade. The migration is
idempotent — safe to run repeatedly.

---

## Security hardening checklist

For any internet-facing deployment:

- [ ] Set `SSH_HOST` to a specific interface (not `localhost`); set `ADMIN_HOST` to your VPN interface address
- [ ] Never forward `ADMIN_PORT` to the internet
- [ ] Use a strong passphrase for admin accounts
- [ ] Consider reducing `RATELIMIT_PER_MINUTE` (e.g., `0.5`) for brute-force protection
- [ ] Set `PENDING_EXPIRY_DAYS` to a shorter window (e.g., `2`) for open-with-approval deployments
- [ ] Back up `data/` regularly — it contains the SSH host key and all user accounts
- [ ] Consider adding OS-level firewall rules to restrict who can reach `SSH_PORT`

The server provides defense-in-depth: per-IP rate limiting, per-account
lockout after 5 failed attempts, argon2id password hashing, and the
dual-listener split are all always active regardless of deployment mode.

---

## Emergency procedures

### All admin accounts are banned

If all admin accounts have been suspended (e.g., a mistake during testing),
no admin can log in through normal means. Recovery requires direct SQLite
access on the server:

```bash
sqlite3 data/retro-vax-bbs.db \
  "UPDATE users SET status='active', banned_until=NULL WHERE role='admin'"
```

Restart the server afterward.

### Forgot an admin password

Use `cmd/adduser` to create a new admin account:

```bash
go run ./cmd/adduser -username newadmin -password 'new-password' -role admin
```

Or reset an existing account's password directly:

```bash
# Get the argon2id hash of the new password
go run ./cmd/adduser -username existingadmin -password 'new-password'
# This will fail with "duplicate username" but prints the hash it would
# have stored; use that hash in a manual UPDATE:
sqlite3 data/retro-vax-bbs.db \
  "UPDATE users SET password_hash='<hash>' WHERE username='existingadmin'"
```

A cleaner `cmd/resetpw` tool is planned for a future release.

### Database corruption

Restore from backup. SQLite WAL mode minimizes corruption risk, but
hardware failures can cause it. Keep regular backups of `data/`.

---

## Logging

The server logs auth events (successes and failures with source IP),
periodic maintenance (pending account purges, etc.), and server lifecycle
events to stdout. Failures are logged with enough detail for external
tooling (fail2ban, log aggregation) to act on them.

Auth failure log format:
```
public auth failure: wrong password for "alice" from 1.2.3.4:54321
public auth failure: unknown user "hacker" from 1.2.3.4:54321
public auth failure: admin account "sysop" rejected on public listener from 1.2.3.4:54321
```

For fail2ban integration, write a filter matching `auth failure` in the
server's log output and point it at `SSH_PORT`.
