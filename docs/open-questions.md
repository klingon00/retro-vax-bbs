# Retro VAX-BBS — Open Questions & Notes

Companion to the main design doc. This is the "still soft" stuff — things acknowledged but not yet designed in detail, plus a place to track what's actually been built.

## Not yet designed

- **Mail app** — interface contract exists (modular app framework), but no UX/content design yet.
- **Text game** — acknowledged as a future modular app, nothing scoped beyond that.
- **Color/emphasis** — opt-in negotiation agreed at a high level (both sender and receiver must opt in). `color_opt_in` column already in schema. Exact command syntax (e.g. `SET COLOR ON`) and which UI elements support emphasis: not yet detailed. Implementation path: sender wraps text in ANSI codes, receiver strips them if `color_opt_in = false`.
- **External notifications** — hook point reserved in the login/presence path, but the actual mechanism (webhook vs. ntfy-style push vs. something else), subscription command syntax, and notification rate-limiting are all undesigned.
- **Unraid Community Apps template** — not started. XML template, icon, port-mapping documentation, README for the listing: all pending.
- **CIDR-based admin allowlist** — documented as an alternative/complement to the dual-listener split, not implemented, not required (the listener split is the primary mechanism).
- **VAX/VMS-style command abbreviation** — agreed as a nice-to-have (shortest unambiguous prefix), not yet scoped into v1 build order.
- **Argon2id tuning** — rough starting params given (~64MB memory, 3 iterations) but not benchmarked against actual deployment hardware.
- **Third-party notices file** — license is MIT, all current and planned dependencies are MIT/BSD-3-Clause, but a proper notices file listing each dependency's license hasn't been created yet. Good practice before public/Unraid release.
- **PHONE: mute / do-not-disturb** — bell suppression for ring notifications and Ctrl-G. Future config flag; deferred.

## Decisions explicitly deferred on purpose

These came up but were intentionally pushed past v1 — don't reopen them without a reason:

- Color/emphasis terminal options
- External notification hooks
- Mail and text-game apps
- Unraid packaging
- **MFA for admin accounts** — considered, not implemented. Out of scope for a hobby project at this scale. Structural separation (dual-listener + VPN gate) is the primary admin access control mechanism. Worst-case recovery via backups.
- **PHONE: HOLD/UNHOLD** — useful but not essential for v1. Can be added later without touching the core call architecture.
- **PHONE: FACSIMILE** — file-sending during a call. Out of scope entirely for now.
- **PHONE: MAIL (in-call quick message)** — the PHONE-internal MAIL command for leaving a message when a callee isn't available. Deferred; the lobby already has a path to a future mail app.

## Build status

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
  - [x] `WHO` (real implementation — session registry-backed)
  - [x] `FINGER <user>`
- [x] PHONE app — **v1 complete** (see implementation notes below)
- [x] Admin commands — **complete** (see implementation notes below)
- [x] SET PLAN / SET PLAN CLEAR — **complete** (2026-06-28)
- [x] Lobby HELP expansion — **complete** (2026-06-28)
- [x] SET PASSWORD / RESET PASSWORD / EXPIRE PASSWORD — **complete** (2026-07-02)
- [x] Docker packaging — **complete** (2026-07-02, see implementation notes below)

---

**Scaffolding (done):** Go module set up; `wish` SSH server + `bubbletea`
lobby loop running and tested end-to-end over a real SSH client
(connect → `HELP` → `WHO` stub → `LOGOUT`). Lives at `cmd/server/main.go`,
`internal/lobby/`, `internal/app/`.

Implementation decisions made along the way, worth keeping on record:

- Command handlers return `(string, tea.Cmd)`, not just a string — gives
  a handler (e.g. `LOGOUT`) a constrained way to trigger a side effect
  like `tea.Quit` without opening the door to arbitrary handler behavior.
  The `commands` map is still the only path from user input to code.
- Crash isolation is three-layered: per-command `recover()` in
  `dispatch()` (a bad command returns an error and the prompt continues
  — closest to real DCL behavior), wish's session-level `recover`
  middleware as a backstop, and Bubble Tea's own internal panic recovery
  as a third layer.
- Bubble Tea `View()` functions use `\n` between lines, never `\r\n` — the
  renderer adds its own `\r` before each line during raw-terminal output,
  plus an erase-to-end-of-line cleanup after each line. A manual `\r\n`
  causes that cleanup to immediately wipe the text just written. Hit this
  for real during scaffolding testing — symptom was the prompt drifting
  down the screen with no visible command output. Worth remembering for
  every future `View()` (PHONE, mail, etc.).
- `go.sum` was originally generated in a sandboxed environment without
  access to the real Go module proxy/sumdb (`golang.org/x/*` redirected
  to GitHub mirrors, `GOSUMDB=off`). Re-verified against the real proxy
  and sumdb during initial local setup, and the workaround `replace`
  directives were removed — current `go.sum` is fully sumdb-verified.
- **Project renamed from "Retro VMS-BBS" to "Retro VAX-BBS"** (2026-06-20).
  OpenVMS is an actively-developed, commercially-licensed trademark (VSI,
  licensed by HPE — current releases, active roadmap). VAX hardware and
  branding have been discontinued for decades with no current commercial
  product behind them; VSI has stated it holds zero rights to anything
  VAX-specific. Lower practical trademark risk for a hobby project. An
  explicit non-affiliation disclaimer was added to the README and design
  doc. Historical/descriptive references to real VMS features (the PHONE
  utility, DCL command abbreviation, clustering) were updated to say
  "VAX/VMS" — accurate to DEC's own original product name before the
  1992 rename to OpenVMS — rather than scrubbed entirely.
- **License: MIT.** All dependencies as of this decision (Charm's `wish`
  / `bubbletea` / `lipgloss` / `log` / `keygen`, Go's own `golang.org/x/*`
  packages) are MIT or BSD-3-Clause — no copyleft, no constraint on
  license choice. MIT chosen for consistency with the whole dependency
  tree and minimal friction for forking/packaging later (Unraid, etc.).
  `modernc.org/sqlite` (BSD-style, pure Go, no CGo) is the planned driver
  for the auth milestone — preferred over `mattn/go-sqlite3` (MIT, but
  CGo-based) specifically because CGo would undercut the design doc's
  single-static-binary rationale for choosing Go in the first place.
- `closed` registration mode's entire account-creation path, for now, is
  the standalone `cmd/adduser` CLI tool — hashes the password and
  inserts directly. Deliberately a separate binary, not a server flag,
  since it has no business being reachable from the running server.
- Real `wish.WithPasswordAuth` wired in, replacing the old accept-anyone
  behavior. Caught and fixed a timing side channel while building this:
  checking "does the user exist" before running argon2id meant a
  nonexistent username returned near-instantly while a wrong password on
  a real account took ~0.5s — an enumeration vector via response timing,
  not just message content. Fixed by always running argon2id against a
  fixed dummy hash on the not-found/inactive paths, so every rejection
  costs the same regardless of why.
- Account lockout implemented: 5 consecutive wrong passwords sets
  `locked_until` to now + 15 minutes in the same atomic UPDATE as the
  counter increment. Locked accounts are rejected before argon2id even
  runs (no wasted compute, no counter extension past threshold — extending
  the lock on each attempt past threshold would let an attacker
  permanently lock a real user's account). Counter resets to 0 on
  successful login. Admin `UNLOCK` command calls `ClearFailedAttempts` to
  release early. Tested with in-memory SQLite in `internal/store/store_test.go`.
  Live end-to-end test confirmed: lock triggered at 5th attempt, correct
  password rejected during lock window, access restored after 15 minutes
  with counter reset confirmed.
- **OpenSSH client behavior:** default 3-attempts-per-connection means a
  5-attempt lockout triggers across ~2 SSH invocations, not 5 separate
  connections. Worth knowing for UX reasoning around the lockout
  threshold — not a bug, just how SSH clients work.
- Per-IP rate limiting implemented via `wish/ratelimiter` middleware
  (token bucket, `golang.org/x/time/rate`, LRU-cached by IP). Defaults:
  1 connection/min sustained, burst of 5. Burst of 5 chosen specifically
  to accommodate concurrent sessions (PHONE in one window, mail in
  another — a core feature per the design doc) without triggering the
  limiter for a legitimate user opening a few sessions in quick
  succession. All three parameters are tunable via environment variables
  (`RATELIMIT_PER_MINUTE`, `RATELIMIT_BURST`, `RATELIMIT_MAX_IPS`) for
  operator adjustment at deploy time, mapping cleanly onto the planned
  Unraid Community Apps template model.
- **Dual-listener split implemented.** Two `wish.Server` instances share
  one host key but bind to separate ports. Public listener
  (`SSH_HOST`/`SSH_PORT`, default `localhost:2222`) refuses admin-role
  accounts before checking the password — right-password-wrong-listener
  is indistinguishable from wrong-password by design. Admin listener
  (`ADMIN_HOST`/`ADMIN_PORT`, default `localhost:2223`) refuses non-admin
  accounts symmetrically. Each listener has its own independent rate
  limiter. Enforcement is by network binding, not IP matching. In
  production the operator binds `ADMIN_HOST` to a VPN interface
  (WireGuard/Tailscale); the app has no opinion on which VPN is used.
  `cmd/adduser` now accepts a `-role` flag (default `user`) to create
  admin accounts. Both listeners share one SSH host key — the host key
  identifies the server to clients, not clients to the server, so
  sharing it between ports does not weaken the admin boundary.
- **Command handler signature changed** from `func() (string, tea.Cmd)`
  to `func(m Model) (string, tea.Cmd)`. Handlers receive the current
  Model so they can access session context (role, registry) without
  package-level variables or closures. The closed-command-grammar
  guarantee is preserved — the `commands` map is still the only path
  from raw user input to executing code.
- **Command aliases and SHOW subcommands added.** `WHO` and `SHOW USERS`
  both dispatch to the same handler; `TIME` and `SHOW TIME` similarly.
  `SHOW` alone returns a helpful error listing valid keywords. HELP
  displays grouped alias pairs rather than flat command names. This
  captures VAX/VMS muscle memory without implementing a full DCL
  verb-noun parser — `SHOW USERS` is just a longer map key, not a
  grammar production.
- **WHO implemented** via `internal/registry` — a `sync.RWMutex`-
  protected map tracking active sessions keyed by username, with role
  and `admin_visible` stored at connect time. Regular users see
  non-admin accounts plus opted-in admins; admins see everyone.
  Concurrent sessions from one account show as "(N sessions)". The
  registry is created once in `main()`, stored in `globalReg`, and
  passed into `lobby.New()` via `teaHandler`. Session middleware
  (wrapping `bm.Middleware`) registers on connect and defers
  unregistration on disconnect.
- **TIME command added** — displays current server time in VAX/VMS
  format: `DD-MON-YYYY HH:MM:SS` (e.g. `22-JUN-2026 15:30:24`).
  Also accessible as `SHOW TIME`.
- **`last_login_at` now updated** on every successful login via
  `store.UpdateLastLogin()`, called from `completeAuth` in `cmd/server/main.go`.
  FINGER reads it and displays in VAX/VMS date format.
- **FINGER <username> implemented** (also: SHOW USER <username>). Applies the
  same visibility rules as WHO — invisible admins appear nonexistent to
  non-admin viewers. Shows: current connection status (with app and session
  count from the registry), last login time, and plan text. The store is
  now passed into `lobby.New()` via `globalDB` for commands that need DB
  access.
- **Argument dispatch added to dispatch().** A prefix-match table
  (`argCommands`) is checked before the exact-match `commands` map.
  `FINGER <username>` and `SHOW USER <username>` are the first entries.
  Admin commands (`APPROVE <user>`, `REJECT USER <user>`, etc.) use the
  same mechanism. The `commandHandler` signature is unchanged for
  no-argument commands.
- **Pre-auth connection timeout implemented** via `ConnCallback` — a
  goroutine races a timer against an "auth done" signal per connection.
  If authentication completes, the signal fires and the goroutine exits
  with no further effect; authenticated sessions have no idle timeout
  and can remain open indefinitely. If the timer fires first (connection
  held open without authenticating), it closes the connection silently
  — no log entry, since there is nothing useful to attribute. Controlled
  via `AUTH_TIMEOUT_SECONDS` env var (default: 120). Implemented without
  `wish.WithIdleTimeout`, which resets on every Read and would disconnect
  idle but authenticated Bubble Tea sessions. Note: the server-side
  connection is cleaned up on timeout, but the OpenSSH client may
  continue to show the password prompt until the user types (it blocks
  on /dev/tty, not the socket, while waiting for password input).

## PHONE app — v1 complete (2026-06-26)

Architecture decisions made before implementation, all confirmed as implemented:

- **Character-by-character, not line-by-line.** Every keypress routes
  immediately to all participants' viewports. This is the defining
  characteristic of the original — you see typos, backspaces, and
  corrections in real time. Resource cost is trivial (Go channel
  operations at typing speed are negligible).
- **Multi-party from the start.** Conference calls via `ADD <username>`
  are architecturally natural — fanout to a slice of channels instead
  of one. Viewport layout math divides screen height among N participants.
  No reason to artificially limit to two-party. Confirmed working with
  3-party calls.
- **Switch-hook character: `%` (percent, original default).** When inside
  PHONE, all keypresses go to the conversation EXCEPT those prefixed
  with `%`, which enter command mode. An info line below the command line
  shows the appropriate tip for the current state; it restores
  automatically when timed notifications clear.
- **Ring behavior matches originals:** rings every 10 seconds. At the
  callee's lobby, broadcasts "<caller> is phoning you (HH:MM:SS)".
  While ringing, the caller sees "Ringing <user>... (Press any key to
  cancel)". Any keypress cancels the pending ring and notifies both the
  callee and all other active call participants.
- **Packages:** `internal/phone/` with `app.go`, `call.go`, `layout.go`.
  Registry gained `CallNotify chan PhoneEvent` per session. The lobby
  Model gained `activeApp app.App` and delegates all input/view to it
  when non-nil.
- **v1 command set implemented:** `DIAL <user>` (also: `PHONE <user>`),
  `ANSWER`, `HANGUP` (or Ctrl-Z in-call), `REJECT`, `EXIT`, `ADD <user>`.
  `HOLD`/`UNHOLD`, `FACSIMILE`, and the PHONE-internal `MAIL` command
  remain explicitly deferred.
- **Registry App column** updates to "PHONE" when a session enters a
  call, back to "LOBBY" on hangup — WHO reflects real-time app state.
- **Keyboard shortcuts implemented:** Ctrl-G (broadcast BEL to all
  participants), Tab (insert 5 spaces), Ctrl-L (clear own viewport,
  broadcast via `\f`), Ctrl-U (clear current line, broadcast via `\x15`
  NAK so all participants see the cleared line simultaneously).
- **HELP** fills the own viewport with a full command/keyboard reference
  and persists until any keypress, which clears the viewport and returns
  to normal operation.

### BubbleTea v1.x rendering discoveries (critical for future apps)

Hard-won knowledge from implementing PHONE that applies to any future
full-screen app launched via the lobby delegation pattern:

- **BubbleTea v1.3.x + wish SSH: line 1 of View() is always off-screen.**
  In this stack (BubbleTea v1.3.10, wish v1.4.7), line 1 of any View()
  rendered via the lobby's `activeApp` delegation is placed at "row 0"
  — one row above the terminal's visible area. Every line shifts up by 1.
  Root cause is in how BubbleTea v1.x's cellbuf renderer positions the
  initial frame during the lobby→app transition. This may be fixed in
  future BubbleTea/wish versions; check before applying the workaround
  to new apps.

- **Workaround: sacrifice blank line + layout compensation.** Prepend
  `b.WriteString("\n")` as line 1 of every full-screen app's `View()`.
  This blank line is absorbed off-screen; the actual content starts at
  line 2, which appears at screen row 1. To keep total content at exactly
  `termHeight+1` (so only the sacrifice blank goes off-screen and content
  fills the terminal), the layout must compensate: use
  `available = termHeight - chromeRows - 1` in `Compute()`. Floor division
  of `available` by participant count can leave total content at
  `termHeight` in some combinations (sacrifice visible again) — fix by
  adding filler blank lines at the bottom of the viewport area to pad to
  exactly `termHeight+1`. See `internal/phone/layout.go` and the filler
  loop in `View()`.
  **Note for inline apps (SET PLAN, future mail compose):** the sacrifice
  blank is still needed, but the full-screen height compensation is not —
  the textarea has a fixed height and doesn't fill the terminal.

- **BubbleTea v1.x cellbuf renderer strips `\a` (BEL).** `\a` embedded
  in the View string is processed as a C0 control by the ANSI parser in
  `charmbracelet/x/ansi` and does not reach the terminal output. To ring
  the terminal bell, write `\a` directly to the SSH session's `io.Writer`
  from a `tea.Cmd`, bypassing the renderer entirely. The lobby stores the
  SSH session writer as `Model.out io.Writer` (passed from `teaHandler`
  via `lobby.New(... out io.Writer)`); phone models receive it via the
  same constructor chain. `ringBellCmd()` on both models captures `out`
  in a closure and writes `\a` to it.

- **EventHangup Callee field convention.** `EventHangup` is used for two
  distinct situations: a participant leaving an active call (Callee empty)
  and a pending ring being cancelled before answer (Callee non-empty,
  set to the username that was being rung). Receivers in CallActive state
  check `event.Callee != ""` to distinguish the two cases — a non-empty
  Callee means "clear the ring notification" rather than "remove a
  viewport". This convention is load-bearing; preserve it when adding
  new event handling.

- **Ctrl-U / line-clear sync via NAK (`\x15`).** Clearing only the local
  `Current` field on the sender's viewport leaves other participants'
  view of that line stale. Broadcast `\x15` (ASCII NAK, Ctrl-U) via
  `BroadcastChar` so all participants clear the sender's `Current` field
  simultaneously. Similarly, `\f` (form feed) clears the entire viewport
  on receipt. Both are handled in `charArrivedMsg` before falling through
  to normal character routing.

- **textarea.Focus() must be called before storing in the Model struct.**
  `Focus()` sets an internal `focus bool` field on the textarea value. If
  called via a method on an already-stored field (e.g. in `Init()`), it
  mutates a copy and the stored textarea remains unfocused — input is
  silently dropped. Fix: call `_ = ta.Focus()` on the local variable in
  `New()` before assigning it into the Model. The returned `tea.Cmd` is
  only a cursor-blink starter and can be safely discarded; `Init()`
  covers blinking with `textarea.Blink`.

## Registration modes — complete (2026-06-27)

- **Three modes** controlled by `REGISTRATION_MODE` env var (default: `closed`).
  `closed` is unchanged from before. The two new modes share a common
  entry point: SSHing as the special username `new` (any password) routes
  the connection to `internal/registration/` instead of the lobby.

- **Registration TUI** runs inline (no alt screen), avoiding the
  sacrifice-line rendering bug. State machine: username → email (optional,
  open-with-approval only) → email confirm (if email provided) → invite
  code (invite-only only) → password → password confirm → done. Password
  fields mask input with `*`. Email is asked twice when provided to catch
  typos; a mismatch sends the user back to re-enter both copies.

- **invite-only flow:** valid invite code activates the account immediately
  — no admin approval step. Password is set during registration.

- **open-with-approval flow:** account sits in `pending` status until an
  admin runs `APPROVE <username>`. The user logs in with the password they
  chose during registration; there is no "set password on first login" step.

- **Username squatting protection** for open-with-approval: pending accounts
  auto-expire after `PENDING_EXPIRY_DAYS` (default 7, 0=never). Two
  mechanisms: `CreatePendingAccount` pre-deletes expired pending accounts
  with the same username before inserting; `PurgeExpiredPendingAccounts`
  runs at startup and every 6 hours via a background goroutine. Both use
  the same expiry window from config.

- **Invite codes** are generated as `adjective-noun-NN` (e.g. `swift-oak-42`)
  using `crypto/rand` against curated 40-word adjective and noun lists plus
  a two-digit suffix (10–89), giving ~144,000 possible codes. Format is
  short and safe to communicate verbally or in a message.

- **Admin notifications:** connected admin lobby sessions receive an
  `EventAdminNotify` ring event (one-time, no repeat) when a new pending
  registration arrives. Admin login banner shows pending count if > 0.

## Admin commands — complete (2026-06-27)

All admin commands enforce role via a check inside each handler (not just
at the dispatch level) and are only reachable on the admin listener anyway.
All actions are logged with the admin's username.

Commands implemented: `APPROVE <user>`, `REJECT USER <user>`,
`DELETE USER <user>`, `LIST PENDING`, `LIST USERS`, `UNLOCK <user>`,
`KICK <user>`, `BAN <user> <duration>`, `UNBAN <user>`,
`INVITE CREATE [N] [duration]`, `LIST INVITES`, `PURGE PENDING`.

Key implementation notes:

- **KICK** stores a `func()` (calling `s.Exit(0)`) per session in the
  registry at connect time. `reg.Kick(username)` calls it; the session
  goroutine sees the connection close and tears down cleanly.
- **BAN** stores `banned_until` as a datetime. Permanent bans use a
  sentinel of year 2099 (`NeverExpires()`). Timed bans auto-lift on the
  user's next login attempt via `CheckAndLiftExpiredBan` in the auth
  handler — no admin action required for expiry.
- **DELETE USER** kicks the session first (if online), then hard-deletes
  the row. Distinct from BAN: the account is gone, the username is free.
  Self-delete is blocked.
- **LIST USERS** shows all accounts (pending, active, suspended/banned,
  locked) with role, effective status, and last login date. "Effective
  status" derives from the combination of `status`, `banned_until`, and
  `locked_until` fields so admins see the real picture at a glance.
- **PURGE PENDING** runs `PurgeExpiredPendingAccounts` on demand using
  the same expiry window as the background goroutine. Reports the count
  purged or notes if expiry is disabled.

## SET PLAN — complete (2026-06-28)

- **`SET PLAN`** launches an inline `bubbles/textarea` editor. The user
  edits their FINGER blurb; Ctrl+S saves, Esc or Ctrl+C cancels. The
  editor runs inline (no alt screen) via the same `activeApp` delegation
  mechanism as PHONE, using an `AppAdapter` wrapper in `internal/setplan/`
  to satisfy the `app.App` interface without coupling the setplan package
  to the app interface directly.

- **`SET PLAN CLEAR`** removes plan text immediately without opening the
  editor — useful for a quick wipe.

- **Character limit:** 512 runes, enforced by both the textarea widget
  (`CharLimit`) and `store.SetPlan` (returns `ErrPlanTooLong`). A live
  counter (`N/512`) is shown in the editor.

- **Security:** ANSI escape sequences are stripped at both storage time
  (`store.SetPlan` calls `store.StripANSI`) and display time (FINGER
  calls `store.StripANSI` before rendering). Belt and suspenders — even
  data that predates the sanitization is safe to display.

- **New dependency:** `github.com/charmbracelet/bubbles v0.21.0` —
  provides the `textarea` component. Added with `go get
  github.com/charmbracelet/bubbles@v0.21.0`.

- **New packages:** `internal/setplan/setplan.go` (Model + editor logic),
  `internal/setplan/app.go` (AppAdapter satisfying app.App),
  `internal/store/plan.go` (SetPlan, ClearPlan, GetPlan, StripANSI).

## Lobby HELP expansion — complete (2026-06-28)

- **`HELP`** now shows two role-aware sections: User commands and Admin
  commands. Admin commands are only shown to admin-role sessions — regular
  users see only user commands. Each command shows a usage line and a
  one-line description. Footer prompts `HELP <command>` for details.

- **`HELP <command>`** returns extended detail for a specific command.
  Currently detailed topics: `BAN` (full duration format table with
  examples), `INVITE` (full syntax with examples), `FINGER`, `SET PLAN`,
  `PHONE`, `WHO`, `HELP`. Admin-only topics (`BAN`, `INVITE`, etc.) return
  "no help available" to non-admin users — same as if the command doesn't
  exist, to avoid leaking command names.

- **Implementation:** `helpTopic` struct drives both sections; `topicDetails`
  map holds extended raw-string help text. `helpByTopic` is an
  `argCommandHandler` registered as the `HELP` prefix in `argCommands`,
  checked before the exact-match `HELP` entry so `HELP BAN` routes to
  `helpByTopic` and bare `HELP` routes to `helpCommand`. Admin topic gating
  uses an `adminDetailKeys` map cross-checked against `adminHelpTopics` so
  the gate stays consistent as topics are added.

## Password management — complete (2026-07-02)

Three commands (`SET PASSWORD`, `RESET PASSWORD <user>`,
`EXPIRE PASSWORD <user>`) sharing one flow in the new
`internal/setpassword` package. Replaces the raw-SQL hash-copy hack that
was the only way to reset a password before this (see the "Forgot an
admin password" emergency procedure in `docs/admin-guide.md` — that
section's "a cleaner `cmd/resetpw` tool is planned" line is now obsolete
and was removed; `RESET PASSWORD` is that tool).

Two gotchas worth remembering for future work in this area:

- **`SET PASSWORD` and the admin variant couldn't share a verb.** The
  request as originally phrased was `SET PASSWORD <username>` for the
  admin case too. But `dispatch()`'s admin-visibility gate
  (`adminCommandKeys`, built from `adminHelpTopics`) keys on the command
  verb itself — for a prefix command, `SET PASSWORD <username>` and bare
  `SET PASSWORD` derive the *same* key (`"SET PASSWORD"`). Registering
  the admin form in `adminHelpTopics` would have gated the self-service
  bare form too, hiding it from everyone. Renamed the admin command to
  `RESET PASSWORD <username>` instead of teaching the one
  admin-visibility chokepoint to special-case this command — worth
  checking for the same collision risk before reusing a verb across a
  self-service/admin pair again.
- **No mechanism exists to swap the root Bubble Tea model mid-session.**
  `EXPIRE PASSWORD`'s forced flow needed to intercept *before* the lobby
  loads. `cmd/server/main.go`'s `teaHandler` builds exactly one
  `tea.Program` per SSH session (same constraint `internal/registration`
  already lives under for a freshly activated account) — there's no API
  to hand off to a second model once the first one's `Update` loop
  starts. `setpassword.ForcedModel` follows registration's existing
  precedent: change the password, show a success message, then `tea.Quit`
  and require the user to reconnect. Wired into `teaHandler` via a new
  `mustChangePasswordKey` context value, set in `sessionMiddleware`
  alongside the existing `roleKey`/`regModeKey` — same pattern, not a new
  one.

Other implementation notes:

- The self-service current-password check reuses the existing lockout
  counter (`RecordFailedAttempt`/`ClearFailedAttempts`, same 5-attempt
  threshold as login) so it can't be brute-forced from an unattended
  session.
- `RESET PASSWORD` is two-phase logged, same pattern as `CREATE USER`:
  `requireAdminLogged` logs the invocation at dispatch time, the
  sub-app's `finalise()` logs the actual outcome, since the admin can
  still cancel the masked-password prompt. `EXPIRE PASSWORD` is a single
  atomic mutation with no sub-app, so `requireAdminLogged` alone covers
  it.
- New schema column: `must_change_password BOOLEAN DEFAULT 0`, additive
  migration, cleared automatically by `SetPassword` regardless of who
  changed the password (self-service, admin reset, or the forced flow
  itself).
- New packages/files: `internal/setpassword/setpassword.go` (shared
  state machine), `internal/setpassword/app.go` (`AppAdapter` for the two
  lobby-launched cases), `internal/setpassword/forced.go` (top-level
  model for the forced case), `internal/store/password.go` (`SetPassword`,
  `ExpirePassword`), `internal/store/password_test.go`.

## Docker / Unraid packaging — complete (2026-07-02)

`Dockerfile` (multi-stage, `golang:1.25-bookworm` builder →
`gcr.io/distroless/static-debian12` final, both `cmd/server` and
`cmd/adduser` baked in, static binary via `CGO_ENABLED=0` — no source
changes needed, the pure-Go `modernc.org/sqlite` choice already made this
possible), `docker-compose.yml`, and `unraid-template.xml` at the repo
root. Verified end-to-end on real hardware (build, boot, `docker exec
adduser` bootstrap, admin login on 2223, regular user + PHONE app), not
just build-clean.

Two real gotchas found during testing, worth remembering before touching
this area again:

- **`data/` has no `os.MkdirAll` anywhere in the app**, and is gitignored
  — a truly fresh clone or fresh container crashes on first run with
  `unable to open database file (14)` in `store.Open()`, *before* the SSH
  host-key auto-generation code (which genuinely does `os.MkdirAll`) is
  ever reached. This affects bare metal too, not just Docker — the
  quick-start checklists in both README.md and admin-guide.md now include
  a `mkdir -p data` step. Deliberately fixed at the packaging/docs layer,
  not by adding `os.MkdirAll` to Go source — see the WORKDIR-equals-mount-
  point design in the Dockerfile.
- **`ADMIN_HOST`'s bare-metal-safe default (`localhost`) actively breaks
  connectivity in containerized bridge-mode Docker**, and this was *not*
  caught by build/vet/local review — it only surfaced testing a real SSH
  connection. `localhost` inside a container binds to the container's own
  loopback interface, which Docker's bridge-mode port forwarding can
  never reach (forwarded traffic always arrives via the container's
  `eth0`/bridge IP). Symptom was a TCP-level connection reset that SSH
  clients reported as a failed key exchange — easy to misdiagnose as an
  auth or host-key problem instead of a network-layer one. Fix:
  `docker-compose.yml` and `unraid-template.xml` both explicitly set
  `ADMIN_HOST=0.0.0.0` for bridge mode — safe there since `ADMIN_HOST`
  provides zero access restriction in bridge mode regardless of its
  value (the host-IP-scoped port mapping is the real boundary), but
  **only** in bridge mode. Anyone switching a container to host network
  mode must change `ADMIN_HOST` back to a real VPN interface IP, or
  `0.0.0.0` there really would expose the admin listener on every host
  interface. The lesson: a config value that's a genuine security
  guard on bare metal can be a pure no-op with a connectivity trap
  hiding behind it once containerized — verify both properties
  independently, don't assume the security reasoning also covers
  correctness.

Also deliberately **not** done as part of this work, flagged as
follow-ups: publishing an image to GHCR (`ghcr.io/klingon00/retro-vax-bbs`
— the Unraid template's `Repository`/`Registry` fields already point
there, but nothing builds/pushes it yet; needs a GitHub Actions workflow),
an Unraid CA icon asset, and eventual submission to the Community
Applications repo for public listing (only needed if this should be
discoverable by other Unraid users, not for self-hosting via "Template
URL").

## Next concrete steps (as of 2026-07-02)

1. **VAX/VMS command abbreviation** — shortest unambiguous prefix (DCL
   style). Nice-to-have, non-blocking.
2. **GHCR publish workflow** — GitHub Actions to build and push the image
   on tag/release, so the Unraid template's `Repository` field resolves
   to something real. Blocks public/remote use of the Unraid template
   (self-hosting by building the image locally already works today).
