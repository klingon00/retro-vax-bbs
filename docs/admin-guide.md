# Retro VAX-BBS — Administrator Guide

This guide covers everything needed to operate a Retro VAX-BBS instance:
initial setup, configuration, account management, moderation, and emergency
procedures.

For architecture decisions and design rationale, see `design-doc.md`. For
implementation notes and known issues, see `open-questions.md`.

---

## Quick-start checklist

1. Build the binary: `go build ./cmd/server`
2. Create the data directory (gitignored, doesn't exist on a fresh clone): `mkdir -p data`
3. Create your first admin account: `go run ./cmd/adduser -username sysop -password '<strong-password>' -role admin`
4. Set environment variables for your deployment (see Configuration below)
5. Run the server: `go run ./cmd/server` (or the compiled binary)
6. Connect to the admin listener to verify: `ssh -p 2223 sysop@localhost`
7. From here on, create additional accounts in-lobby with `CREATE USER` — see below. `cmd/adduser` is only needed again for this first-account bootstrap, or scripted/headless provisioning.

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

## Docker / Unraid deployment

A `Dockerfile`, `docker-compose.yml`, and an Unraid Community Applications
template (`unraid-template.xml`) are provided at the repo root. The image
is a multi-stage build producing a static binary in a minimal, shell-less
final image — consistent with the app's own "no exec/eval, no path to a
real shell" design.

**Quick-start:**

```bash
docker compose up -d
docker exec -it retro-vax-bbs /adduser -username sysop -password '<strong-password>' -role admin
ssh -p 2223 sysop@localhost
```

This is the container-world equivalent of the bare-metal quick-start
checklist above — `docker exec ... /adduser` replaces `go run
./cmd/adduser`, since both the running server and the one-shot `adduser`
binary share the same mounted `/data` volume.

### Timezone (local-time display)

The BBS shows **local wall-clock time** in `TIME`, `WHO`, `FINGER`, `LIST
USERS`, and `LIST PENDING` — matching authentic VAX/VMS, which showed users
system-local time, not UTC. Timestamps are always *stored* in UTC; this only
affects display formatting.

On **bare metal**, "local" is the host's configured timezone
(`/etc/localtime`) automatically — no action needed. **In a container**
(Docker or Unraid), there is no `/etc/localtime`, so local time defaults to
**UTC** unless you set the `TZ` environment variable to your IANA zone — e.g.
`TZ=America/New_York` or `TZ=Europe/London`. The server binary bundles the
IANA timezone database (via a `time/tzdata` import), so any zone name resolves
without adding OS packages to the shell-less image. Leave `TZ` unset or `UTC`
to keep every timestamp in UTC. Both `docker-compose.yml` and the Unraid
template expose `TZ` for this.

### Bootstrapping the first admin account without a shell (Unraid)

The `docker exec ... /adduser` step above requires a real terminal —
fine for compose users, but Unraid's whole workflow is WebUI-driven, and
its per-container "Console" button (which normally execs a shell into a
running container) can't work here at all, since the final image is
shell-less by design (see above). There's no way to reach `/adduser`
through Unraid's UI as documented.

Instead, set two container variables — `BOOTSTRAP_ADMIN_USERNAME` and
`BOOTSTRAP_ADMIN_PASSWORD` (both are exposed as WebUI fields in
`unraid-template.xml`, the password field masked) — and start the
container. At startup, if both are set and the database has zero accounts,
the server creates that account itself, logs the username (never the
password) on success, and continues starting normally. Leaving both unset
is the default and changes nothing.

Two things worth understanding before using this:

- **Both vars must be set together, or both left blank.** Setting only one
  is treated as a misconfiguration and the container fails to start with a
  clear log message, rather than starting in a half-configured state.
- **This is also your emergency recovery lever, not just a first-boot
  convenience.** The check is "does the database currently have zero
  *usable* admin accounts" (active, or suspended with a timed ban that's
  already lapsed) — not "is this the very first boot ever," and not merely
  "are there zero accounts." This covers two distinct disaster scenarios,
  both automatically:
  - **Every admin account deleted:** restart re-creates
    `BOOTSTRAP_ADMIN_USERNAME` as a fresh admin, same as before.
  - **Every admin account banned but still present (new):** if
    `BOOTSTRAP_ADMIN_USERNAME` exactly matches an existing, currently
    banned admin account, a restart instead **resets that account's
    password to `BOOTSTRAP_ADMIN_PASSWORD` and lifts its ban** — it does
    not create a duplicate account, and it does not touch any *other*
    banned admin (log back in as the recovered account and `UNBAN` the
    rest manually). Two things to get right when using this:
    - **A username that doesn't exactly match is never guessed at.** If
      `BOOTSTRAP_ADMIN_USERNAME` doesn't exactly match any account, but
      differs only in *case* from one that does exist (e.g. `klingon00`
      vs. a stored `Klingon00`), the server refuses to start rather than
      assuming they're the same account or silently creating a second,
      look-alike admin under the mismatched name. The fatal error names
      the existing account's exact stored username — set
      `BOOTSTRAP_ADMIN_USERNAME` to that value to recover it.
    - **Pointing this at an existing non-admin account is refused, loudly
      and repeatedly.** If the configured username belongs to a `user`-role
      account (or an admin in an unexpected state, e.g. `pending`), the
      server logs a fatal error and refuses to start, on every restart,
      rather than silently doing nothing or reassigning that account's
      role. If you hit either of these fatal cases in a crash-loop, either
      unset the bootstrap variables or correct `BOOTSTRAP_ADMIN_USERNAME`
      to the exact username the log message names.
  - The "Forgot an admin password" / "All admin accounts are banned"
    procedures further below assume bare-metal `sqlite3`/`go run
    ./cmd/adduser` access, which this shell-less image doesn't have at all
    — so for Docker/Unraid, this bootstrap mechanism doubles as the real
    recovery path for both scenarios above. Decide deliberately: clear both
    variables once you've confirmed login if you don't want that standing
    recovery option, or leave them set (understanding the trade-off) if you
    do. Either way, the password field being masked in Unraid's UI is
    cosmetic only — the value is still stored in plaintext in the
    template's saved config and in `docker inspect` output, so don't treat
    it as real secret storage.
- **If you're building your own custom template instead of using
  `unraid-template.xml` as-is**, make sure both of these fields have an
  empty `Default`. Unraid re-populates a field from its template `Default`
  on Apply whenever the WebUI field is left blank — so a non-empty default
  means clearing the field doesn't actually unset it. This was confirmed on
  real hardware: a non-empty default on the username field alone was enough
  to trip the "one set, one not" fatal-error guard above even after
  clearing both boxes in the UI, since the username kept silently
  reappearing. The same mistake on the password field would be worse — a
  known fixed password would silently apply instead of failing loud.

### Updating a manually-added container

This project isn't in Unraid's Community Applications catalog yet, so if
you added it via a custom/manual template rather than through CA, it does
**not** get Unraid's automatic "update available" badge — that check is
tied to a container being CA-registered. To actually pull a newer `:latest`
(or any new version tag), either `docker pull` the image and use Unraid's
"Force Update," or stop the container and re-Apply its template. A stale
manually-added container will otherwise sit on whatever image was present
at creation time indefinitely, with no visible signal that a newer one
exists.

### Template changes don't affect containers already created from them

Unraid generates a separate `my-<ContainerName>.xml` snapshot the first
time a container is created from a template, under
`/boot/config/plugins/dockerMan/templates-user/`. That snapshot — not the
original template file — is what Unraid reads on every subsequent
start/restart. Updating `unraid-template.xml` (locally, or by pulling a
newer version from this repo) has **zero effect** on an already-created
container, even after a Force Update or a fresh image pull — those only
refresh the image, not the container's config. This applies to *any*
template field, not just the icon: a new env var, a corrected `Default`,
anything.

**The catch that actually bites people**: deleting the container through
Unraid's UI does **not** delete this snapshot file. If you then recreate a
container with the same name, Unraid picks the stale snapshot back up
instead of regenerating it from the (updated) template — so even a full
remove-and-recreate cycle can silently fail to pick up a template change.
To actually apply a template edit to an existing container, delete
*both* the container *and* its `my-<ContainerName>.xml` snapshot before
recreating. This is a general Unraid behavior, not specific to this
project, but easy to get bitten by when iterating on template changes.

### The `/data` volume is required

The server has no fallback for a missing data directory. If `/data` isn't
mounted to a real, writable location before first boot, the container
will crash immediately on startup with:

```
opening database: enabling foreign keys: unable to open database file (14)
```

This happens *before* the SSH host key would otherwise be auto-generated,
so a missing volume never gets a chance to self-heal — mount it or the
container won't come up. (This corrects an over-optimistic claim earlier
in this guide, in "Host key and database" below: `data/` is only created
automatically if the directory chain up to it already exists. On a truly
fresh boot with nothing mounted, it isn't.)

### Network mode: this is the highest-stakes misconfiguration in this guide

`ADMIN_HOST`'s behavior under Docker depends entirely on which network
mode the container runs in — get this wrong and you can expose the admin
listener to the internet.

**Bridge mode (the default):** the container has its own network
namespace and cannot see host VPN/VLAN interfaces at all. Setting
`ADMIN_HOST` inside the container does **not** restrict anything in this
mode. However — **`ADMIN_HOST` must be set to `0.0.0.0` in this mode**,
which sounds backwards given the bare-metal advice below, but is required
for basic connectivity, not security: the app's own default of
`localhost` binds to the container's own loopback interface, which
Docker's bridge-mode port forwarding can never reach (forwarded traffic
always arrives via the container's `eth0`/bridge IP, never its loopback).
Leaving `ADMIN_HOST` at the app default doesn't fail closed — it fails
with a TCP-level connection reset on connect, which SSH clients often
report confusingly as a failed key exchange rather than a plain
"connection refused." The actual restriction has to come from which host
IP you bind the admin port's mapping to:

```bash
docker run -e ADMIN_HOST=0.0.0.0 -p 100.x.x.x:2223:2223 -p 2222:2222 ...
```

(`100.x.x.x` above is an example Tailscale IP — bind to whatever host
interface your VPN/VLAN presents.) The equivalent in Compose is a
host-IP-scoped entry in the `ports:` list instead of `"2223:2223"`. If you
publish the admin port without scoping it to a specific host IP, it is
reachable on every interface the host has — including a public one,
regardless of what `ADMIN_HOST` is set to.

**Host network mode (opt-in):** the container shares the host's network
namespace directly, so `ADMIN_HOST` behaves exactly as it does on bare
metal — set it to your real VPN interface IP, **not** `0.0.0.0`. If
you're switching a container from bridge mode to host mode, remember to
change `ADMIN_HOST` away from the `0.0.0.0` bridge-mode setting above —
left at `0.0.0.0` in host mode, it really would expose the admin listener
on every interface the host has, same as the bare-metal warning in the
Configuration reference below. On Unraid, this is a network-mode toggle
in the container's edit screen (advanced view), independent of anything
the Community Applications template specifies.

Which mode and which VPN/VLAN to use is entirely your call, same as on
bare metal — the app has no opinion on it either way.

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

No self-service registration. The admin provisions every account directly.
Correct for private systems where you know every user personally and will
hand them credentials directly.

The normal path is the in-lobby `CREATE USER` command, run from an admin
session on the admin listener:

```
CREATE USER alice              — creates a regular user account
CREATE USER sysop2 admin       — creates another admin account
```

`CREATE USER` prompts for the password with a masked input (like a login
prompt) rather than taking it as a command argument — the password never
appears in the command line or in the lobby's scrollback history. See
**Admin command reference** below.

`cmd/adduser` still exists as a CLI alternative, and is the *only* option
for the very first account (there's no admin session yet to run a lobby
command from) or for scripted/headless provisioning:

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
`DENY <username>` immediately to free a name.

---

## Admin command reference

Admin commands are only available on the admin listener. Every command
that changes state — creation, deletion, ban/unban, kick, approve/deny,
invite creation, pending purge — is written to the server log with the
admin's real username, regardless of that admin's `SET VISIBLE` setting.
Read-only commands (`LIST PENDING`, `LIST USERS`, `LIST INVITES`) are not
logged. See **Logging** below for the exact format.

### Account creation

| Command | Description |
|---|---|
| `CREATE USER <username> [role]` | Create an account directly. `role` is `user` (default) or `admin`. Opens a masked password prompt — see **Registration modes → closed** above. |

### Account approval

| Command | Description |
|---|---|
| `LIST PENDING` | Show all pending account requests with username, email, and submission date. |
| `APPROVE <username>` | Activate a pending account. The user can log in immediately with the password they set during registration. |
| `DENY <username>` | Delete a pending account request. The username becomes available again. |

### Account maintenance

| Command | Description |
|---|---|
| `DELETE USER <username>` | Permanently remove an account and free the username. Cannot be used on your own account, or on the last usable admin account. |
| `UNLOCK <username>` | Clear a login lockout (triggered after 5 consecutive failed password attempts; normally lifts after 15 minutes). |

### Password management

| Command | Description |
|---|---|
| `RESET PASSWORD <username>` | Set a user's password directly. Prompts for a masked new password and confirmation — no current-password check needed, since the admin is setting it, not verifying it. |
| `EXPIRE PASSWORD <username>` | Force a mandatory password change on the user's next login. Their current password still works for that one login, but the session goes straight into a password-change screen before the lobby loads — it cannot be skipped. |

Users can also change their own password at any time with `SET PASSWORD`
(asks for the current password first) — see the main command reference,
not admin-only.

### Moderation

| Command | Description |
|---|---|
| `KICK <username>` | Immediately disconnect a user's active session. They can reconnect right away. Use for troubleshooting or asking someone to reconnect. |
| `BAN <username> <duration>` | Suspend an account for the specified duration. Disconnects them if currently online. See duration format below. Refused if the target is the last usable admin account (self-bans are fine as long as another usable admin remains). |
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
directory. The SSH host key's *file* is generated automatically on first
run, but this requires the `data/` directory itself to already exist —
there's no fallback that creates the directory chain from scratch if it's
entirely missing. `data/` is gitignored, so a genuinely fresh clone
doesn't have one; the quick-start checklist above includes a `mkdir -p
data` step for exactly this reason. Under Docker, the same requirement
shows up as the volume-mount rule — see Docker / Unraid deployment above.

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

**Docker/Unraid note:** the `sqlite3` commands in this section assume
bare-metal shell access, which the shell-less Docker image doesn't have.
See the "All admin accounts are banned" section immediately below for the
Docker/Unraid-reachable alternatives — both the "every account has been
*deleted*" case and the "every admin account is banned but still present"
case now have a documented recovery path that works from a shell-less
image.

### All admin accounts are banned

If all admin accounts have been suspended (e.g., a mistake during testing),
no admin can log in through normal means. Also relevant: `BAN` and
`DELETE USER` both refuse an action that would drop the count of usable
admins to zero, so reaching this state today requires either a ban applied
before that guard existed, or a direct database edit — not a single normal
admin command.

**Bare metal (direct SQLite access):**

```bash
sqlite3 data/retro-vax-bbs.db \
  "UPDATE users SET status='active', banned_until=NULL WHERE role='admin'"
```

Restart the server afterward.

**Docker/Unraid (no shell in the image) — two options:**

1. **Fastest — mint a brand-new admin under a different name**, sidestepping
   the banned accounts entirely:
   ```bash
   docker exec -it retro-vax-bbs /adduser -username rescueadmin -password '<strong-password>' -role admin
   ```
   This works today regardless of any other admin's ban state — `adduser`
   is a separate one-shot binary with no ban check, baked into the image
   alongside the server. It creates a new identity rather than recovering
   an existing one; once logged in as `rescueadmin`, use `UNBAN` to restore
   the original accounts if you want them back.
2. **Recover a specific banned admin's original identity** via the
   bootstrap-env-var mechanism (see "Bootstrapping the first admin account
   without a shell" above): set `BOOTSTRAP_ADMIN_USERNAME` to that admin's
   exact (case-sensitive) username and `BOOTSTRAP_ADMIN_PASSWORD` to a new
   password, then restart the container. The server detects zero *usable*
   admins, finds the matching suspended admin account, resets its password,
   and lifts its ban — confirm via a `bootstrap admin: recovered admin
   account "..." (password reset, ban lifted)` log line. Log in with the
   new password, then `UNBAN` any other still-banned admins from that
   session.

### Forgot an admin password

**If another admin account is still usable:** log in as that admin and run
`RESET PASSWORD <name>` — prompts for a masked new password and
confirmation, no shell or SQLite access needed. (Recreating the account
via `DELETE USER` + `CREATE USER` still works too, but `RESET PASSWORD`
is simpler and doesn't require freeing and recreating the username.)

**If you're completely locked out** (no working admin account at all),
create a new one from the CLI:

```bash
go run ./cmd/adduser -username newadmin -password 'new-password' -role admin
```

Or reset an existing account's password directly. `cmd/adduser` refuses to
overwrite an existing username, so mint the hash under a throwaway name and
copy it over with SQL:

```bash
go run ./cmd/adduser -username tmp_pw_reset -password 'new-password'
sqlite3 data/retro-vax-bbs.db <<'SQL'
UPDATE users SET password_hash = (SELECT password_hash FROM users WHERE username = 'tmp_pw_reset')
  WHERE username = 'existingadmin';
DELETE FROM users WHERE username = 'tmp_pw_reset';
SQL
```

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

**Admin command audit log.** Every state-changing admin command logs a
line with the real admin username, regardless of that admin's visibility
setting:

```
admin action: sysop APPROVE alice
admin action: sysop DENY bob
admin action: sysop KICK troublemaker
admin action: sysop BAN troublemaker 24h
admin action: sysop UNBAN troublemaker
admin action: sysop UNLOCK alice
admin action: sysop DELETE USER bob
admin action: sysop EXPIRE PASSWORD alice
admin action: sysop INVITE CREATE 5 7d
admin action: sysop PURGE PENDING
```

`CREATE USER` and `RESET PASSWORD` each log twice: once when the command
is run (the admin may still cancel the password prompt), and once with
the actual outcome:

```
admin action: sysop CREATE USER alice
admin action: sysop CREATE USER alice (role=user) created

admin action: sysop RESET PASSWORD alice
admin action: sysop RESET PASSWORD alice (password updated)
```

or, if cancelled:

```
admin action: sysop CREATE USER alice
admin action: sysop CREATE USER alice (role=user) cancelled, no account created
```

These log the *attempt* (the command the admin ran, with its arguments),
not a guaranteed-successful mutation — e.g. `BAN` against a nonexistent
username still logs the attempt. Read-only commands (`LIST PENDING`,
`LIST USERS`, `LIST INVITES`) are not logged.
