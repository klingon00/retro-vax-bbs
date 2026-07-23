# Retro VAX-BBS — Open Questions & Notes

Companion to the main design doc. This is the "still soft" stuff — things acknowledged but not yet designed in detail, plus a place to track what's actually been built.

## Not yet designed

- **Mail app** — interface contract exists (modular app framework), but no UX/content design yet.
- **Text game** — acknowledged as a future modular app, nothing scoped beyond that.
- **Color/emphasis** — opt-in negotiation agreed at a high level (both sender and receiver must opt in). `color_opt_in` column already in schema. Exact command syntax (e.g. `SET COLOR ON`) and which UI elements support emphasis: not yet detailed. Implementation path: sender wraps text in ANSI codes, receiver strips them if `color_opt_in = false`.
- **External notifications** — hook point reserved in the login/presence path, but the actual mechanism (webhook vs. ntfy-style push vs. something else), subscription command syntax, and notification rate-limiting are all undesigned.
- **Unraid Community Apps template** — not started. XML template, icon, port-mapping documentation, README for the listing: all pending.
- **CIDR-based admin allowlist** — documented as an alternative/complement to the dual-listener split, not implemented, not required (the listener split is the primary mechanism).
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
  registry at connect time, keyed by sessionID. `reg.Kick(username)` calls
  **every** session's func and returns how many it terminated; each session
  goroutine sees its connection close and tears down cleanly. (Until
  2026-07-20 the func was stored per *account*, so only the newest session
  was closed — see the retired entry further down.)
- **BAN** stores `banned_until` as a datetime. Permanent bans use a
  sentinel of year 2099 (`NeverExpires()`). Timed bans auto-lift on the
  user's next login attempt via `CheckAndLiftExpiredBan` in the auth
  handler — no admin action required for expiry.
- **DELETE USER** kicks every session of the account first (if online),
  then hard-deletes the row. Distinct from BAN: the account is gone, the username is free.
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

## Public release prep + GHCR publish workflow — complete (2026-07-03)

Repo is going fully public (source + GHCR package both), to enable eventual
Unraid Community Applications submission — a private-repo-plus-public-image
split was considered and rejected as unusual and trust-undermining for an
SSH-facing self-hosted service people are vetting before running at home.

Before flipping any visibility switch, audited current tree + full git
history for anything that shouldn't go public: no secrets/tokens/keys, no
real full name, no real email beyond the GitHub-linked one, no laptop
username/hostname leaks. Clean, except for one old placeholder example
username (a common first name, not a real one — deliberately not spelled
out here, see below) left in a few files/tests from before an earlier pass
only caught it in README.md.

- **Fixed the remaining occurrences of the old placeholder** — `"alice"` in
  `cmd/adduser/main.go` and `internal/phone/app.go`, but **`"carol"`** in
  `internal/registry/registry_test.go` and **`"BOB"`** in
  `internal/phone/layout.go`'s ASCII diagram. Both needed a different
  replacement than the obvious `"alice"` because each file already had its
  own `"alice"`/`ALICE` example elsewhere — a blind global find-and-replace
  would have created a duplicate test username (breaking an
  alphabetical-sort assertion) and a redundant diagram label, respectively.
  Only caught by reading the files directly, not by grepping for the string
  and assuming one replacement fits everywhere.
- **Added `.github/workflows/docker-publish.yml`**: builds and pushes to
  `ghcr.io/klingon00/retro-vax-bbs` on `v*.*.*` tag push, tags both the
  version and `latest`, amd64-only (Dockerfile has no `GOARCH` pin and
  `ubuntu-latest` runners are amd64, so no buildx/multi-arch matrix needed),
  authenticated via the built-in `GITHUB_TOKEN` — no PAT.
- **Git history rewrite via `git-filter-repo`** to scrub the old placeholder
  from all 29 historical commits, since the repo was still private with exactly one
  clone in existence — the one moment a rewrite is genuinely safe, closing
  permanently the instant the repo goes public. Two things worth remembering
  if this comes up again: (1) `git filter-repo`'s `--replace-text` only
  rewrites blob/file content, **not commit messages** — that needs the
  separate `--replace-message` flag (same file syntax), run together in one
  pass; `--replace-text` alone left the string sitting in two commit
  messages. (2) a same-machine `git clone` without `--no-local` hardlinks
  objects with the source repo, and `git filter-repo` refuses to run against
  that — needs a genuinely independent clone. Verified clean afterward via
  `git log --all -p` (zero hits for the word-boundary pattern, `wrong` still
  intact as a sanity check that the boundary matching worked), plus a full
  `go build && go vet && gofmt -l` pass on the rewritten history before the
  force-push, which happened only after explicit confirmation.

**Closed 2026-07-04.** All four manual steps done and verified:
1. Re-checked GitHub's own UI/code search for the scrubbed string — clean.
   (Per CLAUDE.md's "History-scrub playbook": this recheck is a confidence
   signal, not proof — GitHub's search can't see dangling objects, only
   fail to surface them. Not escalated to a Support-requested purge, since
   the scrubbed string was a common first name, not a credential — a
   proportionality call, not an oversight.)
2. Repo flipped to public (Settings → General → Danger Zone).
3. `v0.1.0` tag pushed, triggering the publish workflow successfully.
4. GHCR package visibility confirmed public.

**Anonymous-pull proof, not just dashboard settings**: ran
`docker pull ghcr.io/klingon00/retro-vax-bbs:0.1.0` from an unauthenticated
shell — pulled clean, no login required. Booted the pulled image standalone,
created a test account via `adduser`, logged in over SSH successfully. This
is the real end-to-end verification the plan called for: repo public,
package public, image pullable anonymously, and the pulled image actually
boots and serves SSH correctly — not just correct-looking visibility
toggles. Docker packaging + public release arc is fully closed.

Note for anyone pulling by tag later: the image tag has **no `v` prefix**
even though the triggering git tag does — pushing `v0.1.0` publishes as
`0.1.0` (the workflow strips it via `${GITHUB_REF_NAME#v}`). This tripped
up the anonymous-pull test until caught; now documented in README's Docker
section too.

## Bootstrap admin account via env vars (Docker/Unraid first-run fix)

Real-hardware testing of a custom Unraid template against the public image
flagged that first-run account creation isn't Unraid-friendly. Root cause:
the only documented bootstrap path is `docker exec -it retro-vax-bbs
/adduser ...`,
but the final image is `FROM gcr.io/distroless/static-debian12` — no shell.
Unraid's WebUI "Console" button execs a shell into the container to give an
operator a terminal; with no shell in the image, that button is simply dead
here, and Unraid's whole workflow is WebUI-driven, not terminal-driven.

Added two optional env vars, `BOOTSTRAP_ADMIN_USERNAME` and
`BOOTSTRAP_ADMIN_PASSWORD`. At startup, if both are set and
`Store.CountUsers()` (new method, mirrors `CountPendingAccounts`) is zero,
the server creates that admin account itself — same `auth.HashPassword` +
`db.CreateUser` calls `cmd/adduser` already makes — and logs success (never
the password). Neither set → no behavior change. Exactly one set, or the
username case-insensitively equals `"new"` (reserved for self-registration,
would create an unreachable account) → `log.Fatalln`, fail fast rather than
start half-configured. See `cmd/server/main.go`'s `bootstrapAdminAccount`
for the implementation and full rationale in comments.

**Deliberately not a one-time-only mechanism.** Gating on `CountUsers()==0`
rather than a first-boot marker file means if every account is later
deleted (`DeleteUser` is an unguarded hard delete, reachable via lobby
`DELETE USER`), leaving the bootstrap vars set lets the next restart
re-create the account. This was a genuine design fork — the decision was
to keep it as a recovery lever rather than close it off, specifically because
`docs/admin-guide.md`'s existing "Emergency procedures" assume bare-metal
shell/`sqlite3` access and don't reach this image at all; without this
mechanism there'd be no Docker/Unraid recovery path if every account were
deleted. (At the time this was written, it did *not* help the "admin
accounts are banned but not deleted" case — a banned row still counted
toward `CountUsers()`. That gap is now closed — see the "Banned-admin
recovery + last-usable-admin guard" entry below, which replaced this gate
with `CountUsableAdmins()` and added a dedicated recovery path for exactly
this case.) Also read the password directly via `os.Getenv` rather than
threading it through `loadConfig()`'s `config` struct — that struct gets
dumped wholesale in one `log.Printf` at startup, which would have logged
the password in plaintext.

Docs updated in `README.md` and `docs/admin-guide.md` (new "Bootstrapping
the first admin account without a shell" subsection, plus a correction note
in "Emergency procedures"); `unraid-template.xml` gained two `Config`
entries (password field `Mask="true"`, noted as cosmetic-only — still
plaintext in the template file and `docker inspect`); `docker-compose.yml`
got the same two vars, commented out by default.

**Real-hardware Unraid verification, 2026-07-04.** A custom template was
tested on real Unraid hardware and found one genuine bug — not in this
repo's template, but a lesson for anyone building their own: the custom
template's `BOOTSTRAP_ADMIN_USERNAME` field had a non-empty `Default` in
the XML.
Unraid re-populates a field from its `Default` on Apply whenever the WebUI
field is left blank, so clearing the username field didn't actually unset
it — it silently reappeared, tripping the "one set, one not" fatal-error
guard for the wrong reason (a stale default value, not an actual
misconfiguration). Checked this repo's own `unraid-template.xml`: both
`BOOTSTRAP_ADMIN_USERNAME` and `BOOTSTRAP_ADMIN_PASSWORD` already had
`Default=""`, so not affected — added an XML comment directly above both
entries so a future edit doesn't accidentally introduce an example/
placeholder default, plus a caution in admin-guide.md for anyone building a
custom template instead of using this one as-is. Worth remembering: a
non-empty default on the *password* field specifically would be worse than
this username case — it'd silently apply a known fixed password rather
than fail loud.

Also confirmed on the same hardware: manually-added (non-Community-Apps)
containers don't get Unraid's automatic update-checking — pulling a new
`:latest` requires an explicit `docker pull` + Force Update, or a
stop/re-Apply. Not relevant to this repo's own template today (single
version tag, no updates yet to check for), but will matter the moment a
second version ships, and is now documented in admin-guide.md's Docker/
Unraid section for whenever the CA submission (the "Unraid Community
Applications submission" item under "Next concrete steps" below) makes this
automatic for most users.

With this, the bootstrap-admin flow has been verified end-to-end on real
Unraid hardware, not just this sandbox's Docker: fresh-volume creation,
restart-is-a-no-op, and correct behavior after actually clearing the vars
in Unraid's own UI all check out.

**Icon didn't show after adding it — root cause found, 2026-07-04.** After
wiring `icon.png` into `unraid-template.xml`'s `<Icon>` and confirming the
raw GitHub URL loaded fine, it still didn't show up even after fully
removing and recreating the container from the updated template. Root
cause, found by inspecting the Unraid box directly: Unraid generates a
separate `my-<ContainerName>.xml` snapshot the first time a container is
created from a template, under
`/boot/config/plugins/dockerMan/templates-user/`. That snapshot, not the
original template file, is what Unraid reads on every subsequent
start/restart. The subtlety that actually caused this:
**deleting the container through Unraid's UI does not delete this
snapshot file** — it lingers on disk, and recreating a container with the
same name picks the stale snapshot back up instead of regenerating fresh
from the (now-updated) template. So even his full remove-and-recreate
cycle didn't pick up the new `<Icon>` value, because the snapshot from the
container's original creation — before the icon existed — was still there
and got reused. Editing `unraid-template.xml` (or pulling a fresh copy from
git) has **zero effect** on a container unless both the container *and*
its `my-*.xml` snapshot are deleted before recreating. This applies to
*any* template field change, not just the icon — worth remembering for
every future template edit, including whatever the CA submission process
ends up requiring.
Documented in `docs/admin-guide.md`'s new "Template changes don't affect
containers already created from them" subsection.

## Blocklist-matching git hooks (2026-07-04)

Earlier the same day, some text that shouldn't be in this now-public repo's
history ended up in several commit messages and doc content, despite the
project's own history-scrub playbook existing precisely to prevent that.
Remediated via a `git-filter-repo` rewrite + force-push (also had to catch
and re-point a `v0.2.0` tag that independently kept the old, un-rewritten
commit reachable — a branch-only rewrite doesn't touch tags). To prevent
recurrence regardless of which session/tool is driving, added a
`pre-commit` + `commit-msg` hook pair (`scripts/pre-commit`,
`scripts/commit-msg`, shared logic in `scripts/lib/check-blocklist.sh`)
that blocks any commit whose staged content, added/renamed file paths, or
message match a word on a local blocklist.

A hash-based approach was considered and rejected: hashing a short,
guessable string doesn't actually protect it — a dictionary attack against
a small enough candidate set recovers it almost instantly, so a hash in a
tracked file would be obscurity, not protection. Instead, the tracked hook
scripts are fully generic (zero information about what they block) and
read the actual blocklist from `.git/hooks/pre-commit-blocklist` — a file
that lives entirely outside git's trackable surface (anything under
`.git/` is structurally outside what `git add`, even `git add -f`, can
ever stage), created locally, never committed. Two hooks, not one, because
`pre-commit` runs before the commit message is even drafted and
structurally cannot see it — the incident that prompted this specifically
included message-only instances a diff-only hook would have missed.

Design review (empirically tested against real git behavior, not just
reasoned about) caught real bugs before implementation: `git rev-parse
--git-dir` resolves to the wrong place in a linked worktree (fixed via
`--git-path` instead); a user's own global `color.ui`/`GIT_EXTERNAL_DIFF`
config can silently corrupt the diff parse (fixed via `--no-color
--no-ext-diff`); a pure rename with the matched text only in the new
filename produced zero content-line matches (fixed by also scanning
`--name-only` output); and git's own auto-generated comment scaffolding in
the commit-msg file could false-positive before `commit.cleanup` strips it
(fixed by stripping `^#` lines first). Verified end-to-end in a disposable
scratch repo (never the real one) with a placeholder test word, covering:
blocked content, blocked message, blocked rename-only filename, correct
fail-open-with-warning when the blocklist is missing, and a clean commit
succeeding normally. Known, documented limitation: these are client-side
hooks — they don't run for `git rebase` or `git cherry-pick`, and
`pre-commit` doesn't run for merge commits at all.

Installed locally in this working copy (symlinked into `.git/hooks/`,
local blocklist populated) and documented in a new README "Contributing"
section for future clones/worktrees.

## Banned-admin recovery + last-usable-admin guard (2026-07-04)

Closes the "Docker/Unraid recovery for 'all admins banned'" item that used
to sit here as open step #3. Two changes, addressing both the recovery
side and the root cause:

- **`bootstrapAdminAccount` (`cmd/server/main.go`) now gates on a new
  `Store.CountUsableAdmins()`** (`internal/store/store.go`) instead of
  `CountUsers()`. `CountUsableAdmins` counts admin accounts that are
  active, or suspended with a timed (non-permanent) ban that's already
  lapsed — the same lapsed-ban test `CheckAndLiftExpiredBan` already uses,
  so an admin whose ban is about to self-heal on next login doesn't
  spuriously read as "zero usable admins." When triggered, the function
  now does a three-way check on `BOOTSTRAP_ADMIN_USERNAME` via
  `GetUserByUsername`: not found → unchanged fresh-`CreateUser` behavior;
  found with `role != "admin"` → `log.Fatalf` (refuses to touch or
  silently reassign an unrelated account); found as a `suspended` admin →
  the new recovery path, `SetPassword` + `UnbanUser` in that order (both
  individually idempotent, so a crash between the two on a container
  restart-policy retry just re-enters the same branch cleanly).
  `User.IsUsableAdmin()` is a single-row Go-side twin of the same
  predicate, used by the lobby guard below so it doesn't need a redundant
  query on a row already in hand — kept in sync with the SQL by hand,
  there's no shared query builder in this codebase.
- **A side effect of the gate swap, worth knowing:** it also fixes a
  latent, unrelated quirk where a single self-registered non-admin user
  used to permanently block bootstrap-admin creation forever under
  `CountUsers()`; now fixed since the gate only cares about *admin*
  usability.
- **Case-sensitivity fail-safe: `GetUserByUsernameCI`
  (`internal/store/store.go`).** `GetUserByUsername` is exact-match (no
  `COLLATE NOCASE` in the schema), so an initial version of this change
  had a real silent-failure mode: if `BOOTSTRAP_ADMIN_USERNAME` differed
  only in case from an existing suspended admin's stored username (e.g.
  `klingon00` vs. a stored `Klingon00`), the exact-match lookup missed,
  fell through to `errors.Is(err, store.ErrNotFound)`, and silently
  fresh-created a second, look-alike admin under the mismatched name —
  leaving the real banned admin untouched with no error or warning that
  anything had gone differently than intended. Fixed by adding
  `GetUserByUsernameCI`, a `COLLATE NOCASE` lookup scoped to this one call
  site (deliberately not a schema-wide collation change, which would touch
  username-uniqueness assumptions everywhere else). In
  `bootstrapAdminAccount`'s `ErrNotFound` branch, before falling through to
  fresh-create, a case-insensitive lookup now runs first: a match →
  `log.Fatalf` naming the existing account's exact stored username rather
  than guessing, matching the same fail-loud-on-ambiguity pattern already
  used by the role-mismatch branch; no match (under any case) → proceeds
  to fresh-create exactly as before.
- **New preventive guard: `lastUsableAdminGuard`
  (`internal/lobby/commands.go`)**, wired into both `banCommand` and
  `deleteUserCommand` right before each command's `Kick` call. Refuses the
  action if the target is currently a usable admin and `CountUsableAdmins`
  is `<= 1` — i.e., this action would zero out admin access. This closes
  the actual root cause: `BanUser`/`banCommand` previously had zero
  guardrails at all (no role check, no self-ban check), so an admin could
  ban every other admin and then themselves with no warning.
  **Self-ban is deliberately still allowed** as long as another usable
  admin remains — a conscious choice, not an oversight: self-banning only
  affects the actor's own access, so it isn't a real attack path for a
  rogue/compromised admin the way zeroing out *all* admin access is; only
  the last-usable-admin case is actually dangerous, so that's the only
  case refused.
- **New tests:** `internal/store/store_test.go` gained
  `TestCountUsableAdmins` (0/1 active/permanently-banned/lapsed-ban-admin
  combinations), `TestIsUsableAdmin` (table-driven over the same
  status/role/BannedUntil combinations), and `TestGetUserByUsernameCI`
  (finds a match regardless of case, preserves the originally-stored
  casing in the result, returns `ErrNotFound` for a genuinely absent
  username). `internal/lobby` gained its
  *first* test file, `commands_test.go` — it drives `banCommand`,
  `deleteUserCommand`, and `lastUsableAdminGuard` directly (constructing a
  real `lobby.Model` over a real in-memory SQLite store, not through
  actual SSH) covering: a solo admin refused on self-BAN (last usable
  admin), a two-admin BAN succeeding then the remaining admin correctly
  refused on a follow-up self-BAN, a non-admin target never refused, and
  `DELETE USER`'s guard invoked directly (its own unconditional self-guard
  would otherwise short-circuit before reaching this one in the only
  self-target scenario, so the guard itself is exercised standalone).
- **A pre-existing, unrelated escape hatch worth documenting alongside
  this:** `docker exec <container> /adduser -username <new> -password ...
  -role admin` already works today regardless of any admin's ban state —
  `adduser` is a separate one-shot binary with no ban check in
  `CreateUser`, and no interaction with the running server's bootstrap
  logic at all. It can't recover an *existing* banned identity (refuses
  duplicate usernames), but it's the fastest option when the operator just
  wants back in under a new name. Added to `admin-guide.md`'s "All admin
  accounts are banned" section as a third option alongside the bare-metal
  `sqlite3` path and the new env-var recovery path. Confirmed directly: ran
  the compiled `adduser` binary against a scratch DB with one admin already
  suspended, and it created a new active admin account with no error and no
  interaction with the suspended row at all.
- **Manually verified against the real compiled `server` binary** (not
  just `go build`/`go vet`/`gofmt -l`/`go test ./...`, all clean) — every
  scenario run against scratch, file-backed SQLite databases with real
  startup log capture: (1) fresh empty DB + bootstrap vars → unchanged
  fresh-create log line and an active admin row; (2) restart with that
  admin still active + bootstrap vars set → skipped, with the new "usable
  admin account(s)" wording; (3) that admin suspended via a direct SQL
  edit (simulating a ban predating this change, or a manual DB edit).
  Restart with `BOOTSTRAP_ADMIN_USERNAME`/`PASSWORD` matching produced the
  new `bootstrap admin: recovered admin account "..." (password reset, ban
  lifted)` log line — not the fresh-create line — with exactly one row
  left in the table (status `active`, `banned_until` cleared), confirmed
  by direct query; the actual password reset was verified as real (not
  just a log claim) via `auth.VerifyPassword` against the stored hash
  through a temporary in-module helper: the new password verified true,
  the old one verified false. (4) Pointing the bootstrap username at an
  existing `role='user'` account: process exited nonzero with the new
  role-mismatch fatal message, and that account's row was confirmed
  unchanged afterward. (5) The case-sensitivity fail-safe re-run against
  the fixed binary, using a test admin named `klingon00test` for this run:
  suspending it, then restarting with `BOOTSTRAP_ADMIN_USERNAME` set to a
  different-case variant of that name now exits nonzero with a fatal error
  naming the existing account's exact stored username, and no second row
  gets created — confirmed by querying the table afterward and seeing only
  the original, still-suspended row. Regression-checked immediately after:
  restarting with the *exact*-case username against that same suspended
  admin still recovers it correctly (password reset, ban lifted), and a
  genuinely fresh, empty database with no matching username under any case
  still takes the unchanged fresh-create path.

**Real-terminal SSH pass — completed by klingon00, 2026-07-04.** The gap
noted above (this agent's verification stopped at the compiled-binary/log/
DB-query level, with the BAN-guard covered instead via `internal/lobby`'s
tests, not a live session) is now closed: banned a test admin over a real
SSH session, restarted the server with `BOOTSTRAP_ADMIN_USERNAME`/
`PASSWORD` set — including a deliberate case-mismatch attempt — and
confirmed both the fatal refusal (case-mismatch case) and a successful
recovery (matching exact case) followed by an actual SSH login with the
new password, live. This closes the recovery path out fully end-to-end,
the same standard the Docker/Unraid packaging work was held to.

> **Superseded in part (2026-07-06):** the `lastUsableAdminGuard` described
> above was a non-atomic check-then-act — a TOCTOU under two concurrent admins
> — flagged as audit finding #3 and since replaced by folding the guard into a
> single conditional `UPDATE`/`DELETE` in `store.BanUser`/`DeleteUser`. See
> "Audit finding #3 fix" below. The `CountUsableAdmins`/`IsUsableAdmin`
> predicate and the deliberate self-ban-allowed policy described here are
> unchanged; only *where* and *how atomically* the check runs changed.

## Timed-ban and invite-expiry self-heal bug: naive local time stored, compared as UTC (2026-07-04)

Found independently of the admin-recovery work above, reported directly
from a live repro: `BAN alice 10m` followed immediately by a login attempt
lifted the ban within seconds, nowhere near the 10-minute window.

**Root cause was upstream of `CheckAndLiftExpiredBan`, which was already
correct.** `BanUser` (`internal/store/store.go`) formatted its `until`
parameter with `until.Format("2006-01-02 15:04:05")` and stored the result
directly. `until` is built by `internal/lobby/commands.go`'s
`parseBanDuration`/`parseBanDurationFull` as `time.Now().Add(d)` — a
`time.Time` carrying the server's *local* Location. `.Format()` prints the
wall-clock digits with no zone indicator, so the stored string is a naive
local timestamp. `CheckAndLiftExpiredBan`'s SQL compares that string
against SQLite's `datetime('now')`, which always returns UTC. On a server
running behind UTC (this one: `EDT`, UTC-4), a ban set for "10 minutes from
now" in local time gets stored as a string that reads roughly 4 hours
*earlier* than the UTC "now" it's compared against — so it looks
already-expired the instant it's written, regardless of the requested
duration. On a server running ahead of UTC, the fault would run the other
way: bans would outlast their stated duration until local wall-clock time
caught up to the mis-tagged expiry.

**The identical bug existed in `CreateInvite`**, found by inspection once
the pattern in `BanUser` was identified — same
`expiresAt.Format(...)` with no `.UTC()`, feeding
`ValidateAndConsumeInvite`'s `time.Parse("2006-01-02 15:04:05", expiresAt)`
(which defaults to UTC when the layout has no zone, per Go's documented
`time.Parse` behavior) compared against `time.Now()`. Same root cause,
different downstream mechanism — one hits a SQL string comparison, the
other a Go-side re-parse — both broken by the same naive-local-string
write.

**Fix, both call sites:** convert to UTC immediately before formatting —
`until.UTC().Format(...)` in `BanUser`, `expiresAt.UTC().Format(...)` in
`CreateInvite`. `NeverExpires()` was already unaffected — it constructs its
2099 sentinel directly with `time.UTC`, never through this local-`Format`
path.

**Verified by reproducing first, then fixing, then re-verifying** — not
just added tests that happened to pass: `internal/store/store_test.go`
gained `TestCheckAndLiftExpiredBan_FutureBanStaysBanned` and
`_PastBanIsLifted`, run against the *unfixed* code first (the future-ban
test failed exactly as reported — status flipped to `active` immediately),
then confirmed passing after the `BanUser` fix. Same procedure for
`TestValidateAndConsumeInvite_FutureExpiryStillValid` and
`_PastExpiryIsInvalid` against `CreateInvite` (future-expiry test failed
identically before the fix, passed after). Full `go build`/`go vet`/`gofmt
-l`/`go test ./...` clean afterward.

**Noted but not touched:** `Invite.IsExpired()` (`store.go`) duplicates
`ValidateAndConsumeInvite`'s expiry check as a separate method, but is
never actually called anywhere in the codebase (confirmed via grep) —
`ValidateAndConsumeInvite` has its own inline parse-and-check instead of
calling it. Dead code, and arguably a reuse opportunity, but out of scope
for a bug-fix pass; flagging for whoever next touches invite logic.

## Audit finding #1 fix: never-closed event-channel goroutine leak (2026-07-06)

Closes finding #1 of `docs/audits/audit-2026-07-05.md` — the one item that
audit flagged as actually worth prioritizing (a real availability issue for a
24/7 service). Two `tea.Cmd` goroutines did a *blocking* channel receive that
never returned: `waitForPhoneEvent` (`internal/lobby/model.go`) on the
registry's per-account `notify` channel, and `waitForChar`
(`internal/phone/app.go`) on a PHONE participant's `IncomingChar`. Neither
channel was ever closed, and Bubble Tea can't cancel an in-flight `Cmd`, so
every lobby session leaked one goroutine and every call participation leaked
another, for the whole process uptime.

The naive fix (close the channel in `Unregister`/`Hangup`) would panic: the
senders (`sendEvent`/`NotifyAdmins`/`BroadcastChar`) do non-blocking sends, and
a `close()` racing an in-flight send is a send-on-closed panic that `default:`
does not catch. The fix uses two *different* coordinated-shutdown mechanisms,
chosen by where each channel's sends are locked:

- **registry `notify` → a signal-only `done` channel the receiver selects on.**
  `sendEvent` sends *after* releasing the registry RLock (it's handed the
  channel by `Notify()`), so close-under-lock can't exclude it. Instead each
  `entry` gains a `done chan struct{}`, closed once in `Unregister` when the
  account's last session departs (`count <= 0`). `waitForPhoneEvent` selects on
  `notify` and `done`; `notify` itself is never closed, so the lock-free
  non-blocking sends can never panic. `Events()` now returns `(notify, done)` as
  a matched pair under a single RLock — fetching them in two calls could pair a
  live channel with a stale one across a reconnect. `waitForPhoneEvent` guards
  *both* for nil: a non-nil `notify` with a nil `done` would make `select` block
  forever on the nil arm, silently re-disabling shutdown, so it fails toward
  "stop listening" (visible) not "leak" (silent).

- **phone `IncomingChar` → close directly under the sender's lock.**
  `BroadcastChar` (the only sender) holds `c.mu`, the same lock `Hangup`/`Reject`
  hold, so closing there can't race a send, and once the participant is spliced
  out of the slice no later `BroadcastChar` targets it. A shared `hangupLocked`
  helper does the remove-close-notify-teardown, and is idempotent (an
  `idx == -1` guard makes a second removal of an already-gone participant a
  no-op, never a double close). The receiver's pre-existing
  `if !ok { return nil }` path — the audit's "smoking gun" for an
  intended-but-missing close — reaps the goroutine with no receiver change.

- **`Calls.HangupUser(username)` for the disconnect case.** A mid-call SSH *drop*
  runs neither HANGUP nor EXIT, so nothing closed the dropped session's
  `IncomingChar` and it left a phantom participant in everyone else's call. The
  fix hangs the user up from `sessionMiddleware`'s teardown defer — the one hook
  that fires on every exit and runs *outside* the doomed `tea.Program`.
  Registered *after* `reg.Unregister` so LIFO runs `HangupUser` first (tear down
  the call, then remove presence). This also closes the latent
  phantom-participant bug, not just the leak.

**Verification.** Deterministic channel-close / idempotency / nil-guard tests in
`internal/registry/registry_test.go`, `internal/phone/call_test.go` (new file),
and `internal/lobby/model_test.go` (new file). Separately, a throwaway churn
harness armed 100 blocked event-receivers and 200 blocked char-receivers (the
goroutine count rose by exactly +100 then +200, proving they really spawned),
then confirmed teardown returned to baseline, and a 500-cycle
connect/call/disconnect churn held the goroutine count flat (+0) at every
100-cycle checkpoint. The harness was **not** committed — goroutine-count
assertions are timing-sensitive, so the deterministic channel-state tests are
the committed regression guard. `go build`/`go vet`/`go test ./...`/`gofmt -l`
all clean.

**Real-terminal SSH pass — completed by klingon00, 2026-07-06.** The gap noted
above (the agent's own verification stopped at the churn harness plus the
deterministic unit tests — the mid-call *disconnect* path can't be fully
exercised without two live terminals) is now closed: two real SSH sessions in an
active PHONE call, then one side hard-killed by closing the terminal — not a
clean HANGUP or EXIT — and the surviving session correctly showed "X has left the
call," with no hang and no phantom participant left in the call. That exercises
the `HangupUser`-from-`sessionMiddleware`-teardown-defer path on a real socket
death, the same real-terminal standard the Docker/Unraid packaging and
admin-recovery work were held to.

**Not addressed here (deliberate, bounded):** with a per-account `done`, an
earlier session of a multi-session account has its event-receiver reaped only
when the account's *last* session leaves (that's when `done` closes) — bounded,
self-clearing, and a proportionate call given the notify channel is
deliberately per-account (the documented "ring reaches one session" design).
Findings #4–#6 and the minor items in the audit remain open follow-ups.

## Audit finding #3 fix: last-usable-admin guard made atomic (2026-07-06)

Closes finding #3 of `docs/audits/audit-2026-07-05.md` — the concurrency gap
in the last-usable-admin guard. The original guard (added 2026-07-04, see
"Banned-admin recovery + last-usable-admin guard" above) was
`lastUsableAdminGuard` in `internal/lobby/commands.go`: it read
`CountUsableAdmins()` and *then* `banCommand`/`deleteUserCommand` called
`BanUser`/`DeleteUser` as a separate statement. That's a check-then-act
TOCTOU — two admins in two sessions each banning the other could both read
count = 2 (guard passes), then both mutate, landing at zero usable admins:
exactly the state the guard exists to prevent, and the one
`bootstrapAdminAccount`'s recovery lever exists to undo.

The fix folds the count check into the write, so check-and-mutate is one
atomic SQL statement (SQLite serializes writes, so the second statement sees
the first's effect):

- **Shared predicate extracted.** `usableAdminPredicate`
  (`internal/store/store.go`) is now the single SQL definition of "reachable
  admin," referenced by `CountUsableAdmins` and both guarded writes instead of
  each carrying an inline copy — also trimming the hand-synced duplication
  audit finding #4 warns about (the Go twin `User.IsUsableAdmin` still has to
  be kept in sync by hand; a string const can't cross the SQL/Go boundary).
- **Guarded writes.** `BanUser`/`DeleteUser` gained
  `WHERE username = ? AND (NOT (<pred>) OR (SELECT COUNT(*) … <pred>) > 1)`:
  the mutation applies only if the target isn't itself a usable admin (banning
  it can't drop the count) or more than one usable admin exists. On zero rows
  affected they do a follow-up existence read purely to *label* the result —
  `ErrNotFound` vs the new `ErrLastUsableAdmin` — never to decide safety (the
  atomic write already did that; a state change between write and read can at
  worst mislabel the message, never zero out the admins).
- **Go pre-check removed, not layered.** `lastUsableAdminGuard` is deleted;
  the lobby handlers map `store.ErrLastUsableAdmin` to the same
  `%VAX-BBS-E-LASTADMIN` message and now `Kick` the target only *after* a
  confirmed mutation (previously kick-first), so a raced last-admin refusal no
  longer disconnects someone it then declines to ban/delete.

**A consequence worth remembering:** with the guard now in the store, zero
usable admins is unreachable through *any* guarded ban/delete path (only the
empty-DB bootstrap lever mints an admin from nothing). The pre-existing
`TestCountUsableAdmins` had to switch to a white-box `forceBan` helper (raw
SQL, test-only, same package) to construct the zero-usable-admins state it
asserts on — the guarded `BanUser` now correctly refuses to build it.

**Verification.** New store-level tests in `internal/store/store_test.go`:
`TestBanUser_RefusesLastUsableAdmin`, `TestDeleteUser_RefusesLastUsableAdmin`,
`TestBanUser_NonAdminAllowedWithSingleAdmin`,
`TestBanUser_NotFoundNotMisreportedAsLastAdmin`, and
`TestBanUser_ConcurrentMutualBan` — the direct race regression: two goroutines
ban each other (pool pinned to one connection because modernc's `:memory:` is
per-connection), asserting exactly one succeeds, one returns
`ErrLastUsableAdmin`, and `CountUsableAdmins() >= 1` always holds. The
`internal/lobby` tests that used to call `lastUsableAdminGuard` directly were
migrated to drive the handlers through `store.ErrLastUsableAdmin`.
`go build`/`go vet`/`go test ./...`/`gofmt -l` all clean; `-race` clean; the
concurrent test stable across 20 runs. Unlike finding #1's disconnect path,
this race can't be meaningfully hand-triggered on two live terminals (it needs
same-instant execution), so the deterministic + `-race` tests are the
regression guard rather than a live SSH pass; a manual sanity check (ban down
to the last admin and confirm the `%VAX-BBS-E-LASTADMIN` refusal over SSH) is
straightforward if wanted. Reviewed via klingon00's parallel-instance pass on
the full diff. Branch `fix/last-admin-toctou`: code+tests in `153adb7`, audit
status line in `cec8375`.

## Audit finding #4 guard: SQLite DSN timezone param (2026-07-07)

Closes finding #4 of `docs/audits/audit-2026-07-05.md` — the *latent* one, not a
live bug. Every timestamp in `internal/store` is written naive-UTC
(`datetime('now')`, `CURRENT_TIMESTAMP`, or `.UTC().Format(...)`) and today reads
back correctly as UTC. But that correctness rides entirely on the `sql.Open` DSN
being a bare path with no `?_timezone=` query param: the `modernc.org/sqlite`
driver only parses stored `DATETIME` strings with `time.Parse` (→ UTC) while the
connection's `loc` is nil, and `loc` is nil precisely because nothing appended a
timezone param. Verified against the pinned driver source (`v1.53.0`,
`sqlite.go:258` sets `c.loc = time.LoadLocation(v)` from `?_timezone=`;
`sqlite.go:154` `parseTime` switches to `time.ParseInLocation(f, ts, c.loc)` when
`c.loc != nil`). If anyone ever appended `?_timezone=Local`, the driver would
reinterpret stored UTC strings in the server's zone, silently skewing every
ban/lockout/invite-expiry comparison by the UTC offset — the exact bug class the
`BanUser`/`CreateInvite` write-side `.UTC()` fix already closed.

The correctness was invisible and load-bearing, so this adds a guard rather than
changing behavior:

- **Comment at the DSN** (`internal/store/store.go`, above `sql.Open`) spelling out
  why no `?_timezone=`/`_loc` param may be added, and pointing at the test.
- **`TestTimestampRoundTripsAsUTC`** (`internal/store/store_test.go`) stores a
  pinned future instant via `BanUser` (whose explicit `*time.Time` gives a value
  we control, stored via `until.UTC().Format(...)` — the same write path all
  timestamps take) and asserts the read-back `.Equal`s it. The subtlety: a naive
  round-trip test would *pass even with the bug* on a UTC CI host, because
  `?_timezone=Local` resolves through `time.LoadLocation("Local")` → `time.Local`,
  and `Local == UTC` there. So the test pins `time.Local` to a fixed `UTC+5` zone
  (`time.FixedZone`, no tzdata dependency, restored on `t.Cleanup`); a future
  `?_timezone=Local` then resolves to that non-UTC zone and shifts the instant,
  failing the assertion. None of this package's tests use `t.Parallel()`, so the
  global `time.Local` swap is safe, and it has no effect on the current nil-loc
  path (which passes today).

**Verification.** `go build`/`go vet`/`go test ./internal/store/`/`gofmt -l` all
clean; the new test green at baseline. Load-bearing check that the guard actually
bites: temporarily injecting `?_timezone=America/New_York` failed the test with a
+4h skew (EDT in June) and `?_timezone=Local` with a −5h skew (the pinned zone),
both with a legible "did store.Open's DSN gain a _timezone param?" message; the
DSN was reverted before committing. A regression test that can't fail is
worthless, so proving it fails on the regression is the point. Branch
`fix/dsn-timezone-guard`: comment+test in `145e421`, this docs entry + audit
status line in the following commit.

## Audit finding #6 fix: invite expiry now fails closed (2026-07-07)

Closes finding #6 of `docs/audits/audit-2026-07-05.md` — a fail-open on the
invite expiry check. `inviteExpired` (`internal/store/store.go`) parses the
stored `expires_at` string and returned "not expired" whenever *either* the
value was genuinely in the future *or* it failed to parse under both known
layouts, because the expression was `err == nil && year<2090 && now.After(t)`:
a parse failure makes `err == nil` false and short-circuits the whole thing to
`false`. So a corrupted or hand-edited `expires_at` read as a *never-expiring*
invite — the wrong direction for an expiry check to fail.

The fix returns `true` (expired → callers reject with `ErrInviteInvalid`) on a
parse failure, so the ambiguous case fails **closed**. It's a one-helper change
that covers both entry points (`ValidateInvite` and `ValidateAndConsumeInvite`
both route through `inviteExpired`; `ListInvites` has a separate display-only
parse, left untouched). Failing closed can't reject a legitimately-stored
invite: every normal write goes through `CreateInvite`'s
`expiresAt.UTC().Format("2006-01-02 15:04:05")`, which always parses — only
genuinely-corrupted data reaches the new `return true`. The stale doc comment
that had documented the fail-open as "deliberately not changed" (a hold-over
from finding #2's refactor) was rewritten to match.

**Verification.** Two new tests in `internal/store/store_test.go`:
`TestInviteExpired` table-tests the helper (garbage and empty-string → expired
are the regression cases; past/future/never-expires-sentinel/alternate-ISO-layout
pin the surrounding behavior so the fix can't silently break valid-invite
handling), and `TestValidateAndConsumeInvite_CorruptedExpiryFailsClosed` proves
the rejection end-to-end through the public API by corrupting a row's
`expires_at` with a raw white-box `UPDATE` (same technique as `forceBan`) and
asserting both validate paths return `ErrInviteInvalid`. `go build`/`go vet`/
`gofmt -l`/`go test ./internal/store/` all clean. Load-bearing check: both new
tests were run against the old fail-open body and confirmed to go red (garbage/
empty flip to `false`, and the end-to-end paths return `<nil>` instead of
`ErrInviteInvalid`) before the fix was restored — a regression test that can't
fail is worthless. Branch `fix/invite-expiry-fail-closed` off `c661d19`:
code+tests `52a3ed1`, this docs entry + audit status line in the following
commit.

## Audit finding #5 fix: block "new" in admin account-creation paths (2026-07-07)

Closes finding #5 of `docs/audits/audit-2026-07-05.md` — and the last open
finding from that audit. The two admin account-creation paths let you create an
account named `new`, which collides with the self-registration routing sentinel:
the public listener does `strings.EqualFold(username, "new")` (`cmd/server/main.go`)
and routes such a connection to registration instead of login. So a `user`-role
`new` account can never log in (a zombie), and an `admin`-role `new` (reachable
only on the admin listener, which doesn't special-case `new`) is a confusing
footgun. Only self-registration's `reservedUsernames` blocked it;
`validateNewUsername` (CREATE USER) and `cmd/adduser` did not.

The fix rejects `new` case-insensitively in both paths, mirroring the guard
`bootstrapAdminAccount` already applies to `BOOTSTRAP_ADMIN_USERNAME`
(`cmd/server/main.go`, "reserved for self-registration and could never log in").

**Deliberately only `new`, not the whole reserved set.** `validateNewUsername`'s
doc comment already records that skipping the reserved-word block is intentional
— an admin creating accounts directly may legitimately want `sysop`, `admin`, or
`root`, which self-registration blocks to stop impersonation. `new` is different:
it's the *routing sentinel*, not just a reserved word, so it's the one name that
must be blocked even on the admin paths. Blocking the full set would contradict
the existing design; the test pins `sysop`/`admin`/`root` as still-allowed to
guard against that regression.

**Verification.** `TestValidateNewUsername_BlocksNewSentinel`
(`internal/lobby/commands_test.go`) table-tests the helper: `new`/`New`/`NEW`
rejected (case-insensitive), `sysop`/`admin`/`root`/`alice` allowed, and the
format rules (too-short, bad-char) still enforced. Verified it goes red without
the guard (only the new/New/NEW cases fail — the reserved-but-allowed and format
cases stay green, confirming the test targets exactly the fix). `cmd/adduser`'s
guard is inline in `main()` (a `package main` CLI, like the untested
`BOOTSTRAP_ADMIN_USERNAME` twin) and was verified manually: `adduser -username new`
and `-username New` both exit non-zero with the reserved message, before any DB
open. `go build`/`go vet`/`gofmt -l`/`go test ./...` all clean. Branch
`fix/reserve-new-username` off `c6f62bb`: code+tests `50ebfec`, this docs entry +
audit status line in the following commit.

With #5 closed, every audit-2026-07-05 finding (#1–#6) is resolved; only the
minor/stylistic cluster remained — since addressed (see next entry).

## Audit minor/stylistic cluster: cleanups + leave-alones (2026-07-07)

Worked the seven minor/stylistic items from `docs/audits/audit-2026-07-05.md`.
Each was re-located in current code (the audit's line numbers predate the #3–#6
refactors) and marked in place in the audit doc. Three were fixed as trivial
no-visible-behavior cleanups (this branch, `chore/audit-minor-cleanups`, commit
`318cab9`); three were deliberately left as-is with the reasoning recorded here so
a future pass doesn't re-flag them; one (the display-zone inconsistency) is a
visible-behavior change handled separately on its own branch (next entry).

**Fixed (`318cab9`):**

- **`Invite.IsExpired()` deleted** (`internal/store/store.go`) — dead code, zero
  callers repo-wide. The finding #6 fix made the string-based `inviteExpired` the
  single hardened (fail-closed) expiry check used by both `ValidateInvite` and
  `ValidateAndConsumeInvite`; this parallel method on the already-parsed
  `time.Time` had no fail-closed handling and was pure two-sources-of-truth drift
  risk. Deleting it completes #6. The live `DisplayExpiry()` method stays.
- **`preAuthTimeout` timer stopped** (`cmd/server/main.go`) — `time.After(d)` →
  `time.NewTimer(d)` + `defer timer.Stop()` + `case <-timer.C:`. Behavior-identical
  (the timeout still fires the same way; `Stop()` after it fires is a harmless
  no-op), but on the auth-success / ctx-done paths the timer is released
  immediately instead of lingering up to `AUTH_TIMEOUT_SECONDS` (default 120s) per
  connection. A minor holding, not a leak — but free to fix.
- **Nil-guard parity** (`internal/lobby/commands.go`) — `kickCommand` now guards
  `m.reg == nil` and `listUsersCommand` guards `m.db == nil`, matching the sibling
  handlers (`whoCommand`, `listPendingCommand`, `banCommand`) that already do. A
  nil was previously caught by `dispatch`'s `recover`, so this is consistency +
  a clean error rather than a bug fix.

**Left as-is (recorded so they aren't re-flagged):**

- **`banCommand` logs before arg-validation** — `requireAdminLogged` emits the
  audit line before the `len(parts) < 2` usage check, so a malformed `BAN alice`
  logs an attempt then returns usage. This *matches* the documented "log the
  attempt" audit philosophy (the log records admin intent, malformed or not).
  Reordering to log-after-validate would change the audit semantics from attempts
  to actions — a policy choice, not a cleanup. Trivial to flip later if wanted.
- **`generateInviteCode` modulo bias** — uses `crypto/rand` but `int(b[i]) %
  len(list)`. Invite codes are deliberately human-memorable `adjective-noun-NN`
  strings, gated by rate-limit + use-count + expiry, not cryptographic secrets;
  modulo bias on a memorable-code generator is irrelevant to its purpose, and
  rejection sampling would be cargo-cult hygiene.
- **`adduser -password` plaintext on the CLI** — visible in `ps`/shell history,
  inherent to a password flag. Accepted, documented tradeoff for the operator-run,
  closed-mode bootstrap CLI. A real fix (prompt on stdin when `-password` is
  omitted, like the masked in-lobby CREATE USER flow) is a small *feature* that
  changes the invocation contract — out of scope for cleanup; noted as a future
  hardening option.

**Verification.** `go build`/`go vet`/`gofmt -l`/`go test ./...` all clean; no new
tests (the deleted method has no callers, the timer change is a well-known idiom
equivalence in untested SSH wiring, and the nil-guards mirror existing untested
sibling guards).

## Audit minor #1 fix: local wall-clock display + tzdata for containers (2026-07-07)

The one visible-behavior item from the minor cluster (previous entry), on its own
branch `fix/display-zone-local-time`. `TIME`/`WHO` printed server-local time
(`time.Now()`) while `FINGER`/`LIST USERS`/`LIST PENDING` printed the stored UTC
`time.Time` values, so on a non-UTC server they visibly disagreed.

**Direction: normalize to local, not UTC** — authentic early-90s VAX/VMS showed
users local wall-clock time, which is the whole aesthetic goal. Storage stays UTC
internally (unaffected, correct); this is purely display formatting: `.Local()`
added to the three stored-value sites in `internal/lobby/commands.go`, no zone
label (bare local time is what authentic DCL printed).

**The deployment-context catch (worth remembering):** "local" is only meaningful if
`time.Local` resolves to the operator's zone. It does on bare metal
(`/etc/localtime`), but the shipped image (`gcr.io/distroless/static-debian12`) has
**no `/etc/localtime`, so `time.Local` = UTC unless `TZ` is set** — and even a set
`TZ` can fall back to UTC because a minimal image may ship no zone database. So
without more, "local" would silently just mean UTC in the most common deployment
path. Fix: blank-import `time/tzdata` in `cmd/server/main.go`, embedding the IANA
DB (~450 KB) as a fallback used only when system zoneinfo is absent — bare-metal
unchanged, but `TZ=<zone>` now resolves inside distroless. The `TZ` knob is
surfaced in `docker-compose.yml` + `unraid-template.xml` (default `UTC`) and
documented in `docs/admin-guide.md`'s Docker/Unraid section.

**Verification (the deployment-context proof).** (1) The built server binary
contains the embedded zone data (grep found `America/New_York`). (2) `.Local()`
shifts a stored `12:00 UTC` instant correctly per `TZ`: Tokyo `21:00`, New_York
`08:00` (EDT), UTC `12:00`. (3) **Container smoke test on the actual distroless
image** — the server's own Go-`log` timestamps (default `Ldate|Ltime`, i.e. local)
tracked `TZ` end-to-end: against a real `02:30 UTC`, the container logged `02:30`
(TZ=UTC), `11:30` (TZ=Asia/Tokyo, +9), and `22:30` prev-day (TZ=America/New_York,
−4). Without the `time/tzdata` import the last two would have stayed at UTC — the
exact silent-UTC failure the embed prevents. `go build`/`go vet`/`gofmt -l`/`go
test ./...` all clean. Branch `fix/display-zone-local-time` off `2b5a3e6`: feature
`57fb763`, this docs entry + audit status line in the following commit.

**With this, the audit-2026-07-05 minor/stylistic cluster is fully dispositioned
(#1/#2/#4/#7 fixed, #3/#5/#6 deliberately left as-is), closing the entire audit —
all six findings plus all seven minor items resolved.**

## v0.3.1 release: published to GHCR + verified end-to-end (2026-07-12)

Version bump to `v0.3.1`, tagging the post-audit tree (`85e9d81` — every
audit-2026-07-05 finding plus the whole minor/stylistic cluster resolved, per
the entries above). Tag pushed by klingon00; the `docker-publish.yml` workflow
fired on the `v*.*.*` tag and built/pushed `ghcr.io/klingon00/retro-vax-bbs`
(amd64, image built 2026-07-12 22:53 UTC).

Verified to the same standard as the `v0.1.0` closeout — an anonymous-pull proof
*plus* an actual boot-and-serve check, not just green dashboard toggles:

- **Anonymous pull.** `docker pull ghcr.io/klingon00/retro-vax-bbs:0.3.1` from a
  shell with no `~/.docker/config.json` (no ghcr.io credentials) pulled every
  layer clean. `:0.3.1` and `:latest` share one config digest
  (`sha256:9c722a…`), confirming the workflow tagged both from a single build.
  The **`v`-prefix strip still holds:** git tag `v0.3.1` publishes as image tag
  `0.3.1` (`${GITHUB_REF_NAME#v}`), same as v0.1.0 — pull `:0.3.1`, never
  `:v0.3.1`.
- **Clean boot.** Ran the pulled image detached in bridge-mode Docker with a
  named volume at `/data` (dodges both the missing-`/data` crash *and*
  root-owned host files on cleanup), high host ports `12222/12223`
  (collision-proof for a throwaway container), and a bootstrap admin via
  `BOOTSTRAP_ADMIN_USERNAME`/`_PASSWORD`. Startup logged the config line,
  `bootstrap admin: created initial admin account "verifyadmin"` (password not
  logged, as designed), and both listeners up — no DB crash.
  **`ADMIN_HOST=0.0.0.0` was required** for the admin port to be reachable
  through the published-port mapping — the documented bridge-mode gotcha (the
  app default `localhost` binds container loopback, which Docker port-forwarding
  never reaches).
- **SSH on 2223 confirmed by the server's own auth log** (source of truth, not
  an inferred `ssh` exit code): a real admin login produced
  `admin auth success: "verifyadmin" from 172.17.0.1:…`, and the dual-listener
  partition held — the same admin account on the public listener (2222) produced
  `public auth failure: admin account "verifyadmin" rejected on public
  listener`. The `172.17.0.1` source (the Docker bridge gateway, i.e. the host
  as seen from inside the container) is itself evidence the connection arrived
  via the bridge/published-port path — the exact reason `ADMIN_HOST=0.0.0.0` is
  needed there.

Two reusable notes for the next release verification:

- **Log-based login proof beats exit-code proof for a TUI over SSH.** A scripted
  `ssh` into the Bubble Tea lobby has no clean exit — the session gets
  `timeout`-killed (exit 124) because the full-screen app never returns. But
  auth completes *before* the TUI renders, so the server's `admin auth success`
  line is the unambiguous confirmation. Read the app's audit log; don't try to
  script the interactive session.
- **`docker manifest inspect` works client-side; `docker pull` needs the daemon
  socket.** In a restricted/sandboxed shell, `manifest inspect` (a direct
  registry query over HTTPS) can succeed while `pull` fails with
  `permission denied … unix:///var/run/docker.sock`. Handy for a fast
  "is it published yet?" check that needs no daemon access — but only a full
  `pull` proves every layer is retrievable, which is the real user-facing
  guarantee.

Throwaway container + named volume removed afterward; the release is good.

## VAX/VMS command abbreviation — design settled (2026-07-12)

> **Update (2026-07-13): implemented and live-SSH-verified.** See the
> "implemented + live-verified" build-log entry below. The
> "implementation not yet started" wording in this entry is left in place as
> the point-in-time record of the design as of 2026-07-12.

Moved out of "Not yet designed": the shortest-unambiguous-prefix feature
(DCL-style command abbreviation) is now **fully designed; implementation not yet
started.** The forks previously logged there as open questions are now settled
decisions, recorded below. No code yet — the next step is implementation scoping
(resolver location in `internal/lobby/`, function signature, and the integration
point in `dispatch()`).

**Decisions:**

1. **Token model: per-token prefix matching.** Each word of a command is matched
   independently against the set of valid words *at that position*, not the whole
   command line as one prefix. Resolution proceeds left to right: match word 1
   against the valid first-words; once resolved, match word 2 (if any) against
   only the valid continuations of that resolved first word. Example: `LI` →
   unambiguous first-word match on `LIST`; `LIST P` → then unambiguous
   second-word match on `PENDING` (vs. `USERS`/`INVITES`). (`L` alone is *not*
   unambiguous — it collides with `LOGOUT`; and which first-word prefixes are
   ambiguous is role-dependent, since admin-only first-words like `LIST` aren't
   candidates for a non-admin — see decision 3. Corrected against the real
   `commands`/`argCommands` tables in `internal/lobby/commands.go`, which is
   exactly the kind of collision a memory-written example missed.)

2. **Exact match wins over prefix ambiguity.** If a typed word exactly equals a
   valid word at that position, it resolves immediately even if it is also a
   prefix of a longer valid word. Example: `SHOW USER` resolves to the
   `USER <username>` command exactly, despite `USERS` also starting with the same
   letters.

3. **Role-scoped candidate list, computed before matching.** The candidate command
   set is filtered to the caller's role — the same scoping `dispatch()`'s
   `adminCommandKeys` already applies — *before* any prefix resolution runs. Admin
   commands never enter the matching process for a non-admin session: they can't
   be matched, can't contribute to an ambiguity, and can't appear in an ambiguity
   message, for a non-admin caller. This preserves the existing anti-enumeration
   property (abbreviation can't distinguish an admin command from gibberish any
   more than exact typing can).

4. **Aliases are independent candidates, not linked.** `TIME`/`SHOW TIME` and
   `WHO`/`SHOW USERS` are two separate entry points to the same handler. Each is
   matched independently; one alias being an unambiguous prefix match isn't
   blocked or affected by the other alias existing.

5. **Ambiguity error lists the role-scoped candidates.** On an ambiguous prefix,
   return an error listing the ambiguous candidate command names, generated from
   the *same* role-scoped candidate list used for matching — never a separate or
   unfiltered lookup. This keeps the message-construction path from accidentally
   bypassing the role scoping (decision 3) and leaking an admin command name to a
   non-admin.

6. **Both dispatch tables run through one resolver.** The exact-match `commands`
   map and the `argCommands` prefix slice (`FINGER <user>`, admin commands with
   arguments) both run through the same role-scoped, per-token resolver *before*
   falling through to the existing exact/prefix dispatch logic. Abbreviation is a
   resolution step that sits *ahead* of the existing tables, not a parallel path:
   the resolver expands an abbreviated input to its canonical command, then the
   existing dispatch tables handle it unchanged. (Resolves the original
   "two dispatch tables" fork.)

## VAX/VMS command abbreviation — implemented + live-verified (2026-07-13)

Shipped. The six settled decisions above are now code, and the feature has been
exercised end-to-end over real SSH — not just unit tests.

**Implementation** landed across four commits on 2026-07-12: `f4c0f1f` (scope the
design forks), `eb6e87d` (settle them into the six decisions), `ed54e64` (fix a
decision-1 example — `L` collides with `LOGOUT`), and `07ea9c1` (the feature:
code + tests). It's a per-token, role-scoped resolver in
`internal/lobby/abbrev.go`: a keyword trie (`abbrevNode`) built once via
`sync.Once` from the *same* `commands` map + `argCommands` prefixes that drive
dispatch (single source of truth — same pattern as `adminCommandKeys`, so a new
command auto-enrolls in abbreviation with no second list to hand-sync), plus
`resolveAbbrev(line, role) (canonical, ambiguityMsg)`. Integration is a ~13-line
block at the top of `dispatch()` in `commands.go` that runs *ahead* of the
exact/prefix tables and rewrites the line; the existing `adminCommandKeys` gates
still run after it (defence in depth — abbreviation is never a back door into an
admin handler). Tests: `internal/lobby/abbrev_test.go` (resolver unit tests +
`dispatch()` integration, incl. role-scoping/anti-enumeration).

**Live SSH verification (2026-07-13).** Built the server, ran it on isolated
loopback ports (`127.0.0.1:4222`/`:4223`) against a throwaway DB seeded with a
regular user and an admin, and drove real SSH sessions with a Python `pexpect`
script plus a small hand-rolled VT100 emulator to read the BubbleTea alt-screen
(pyte wasn't installed). The server's own auth log showed three clean
auth-success + connect/disconnect cycles with no panics / `recovered` / errors,
so `resolveAbbrev` genuinely ran through `dispatch()` under the wish/BubbleTea
stack. All observed behavior matched the unit tests, which also re-ran green.
Confirmed on a real terminal: `WH`→WHO, `TIM`→TIME, `FI alice`→FINGER (argument
case preserved), `LI P` & `LIST P`→LIST PENDING, `LI U`→LIST USERS; exact-match-
wins verified as two *different* resolutions — `SHOW USER sysop` (finger) vs
`SHOW USERS` (WHO-style list); ambiguity messages for admin `L` (→ LIST, LOGOUT)
and `SH US` (→ SHOW USER, SHOW USERS); and the anti-enumeration invariant held —
a non-admin's `BA`, `DE`, `LI`, and pure gibberish `ZZ` all returned the
byte-identical `"X" is not a recognized command.`

**Two behavior nuances the live pass pinned down — the code is correct; two
plausible-sounding predictions about it were wrong:**

1. **Regular-user `L` → LOGOUT, not "unknown command."** Because `LIST` is hidden
   from non-admins (decision 3), `L` is *unambiguous* for them and resolves to the
   only user-visible L-command, `LOGOUT` (matching the unit test `{"L","LOGOUT"}`
   for role `user`). The anti-enumeration property is still intact — `LIST` is
   never revealed (that's what `LI`→"not recognized" demonstrates) — but `L` does
   not behave like a typo for a regular user; it ends the session with `Goodbye.`

2. **Admin `LI` → `"LIST" is not a recognized command`, not an ambiguity naming
   LIST PENDING/USERS/INVITES.** The resolver is strictly per-token with no
   look-ahead: `LI` uniquely resolves the *first* token to `LIST` (the only
   top-level word starting "LI"), but there is no bare `LIST` command and the
   resolver does not peek at the `PENDING`/`USERS`/`INVITES` children, so dispatch
   reports the resolved-but-incomplete `LIST` as unrecognized. Ambiguity only ever
   surfaces at the *first ambiguous token actually typed* — which is why `L`
   (LIST vs LOGOUT) and `SH US` (SHOW USER vs USERS) are the real ambiguous cases,
   not `LI`. This is consistent with decision 1 (`LI` is described there only as
   the *first-word* step of `LI P`, never as a standalone command).

## Known minor: long lobby output lines truncate instead of wrapping (2026-07-13)

Found during the command-abbreviation live-verification pass, but **pre-existing
and general — not specific to command abbreviation.** `internal/lobby/model.go`'s
`View()` applies no width handling: `flattenHistory` only splits on existing
`\n`, and every history entry is emitted verbatim through the same
`b.WriteString(line)` path. BubbleTea's standard (line-diff) renderer then
truncates any line wider than the terminal to the terminal width — it does not
wrap, and it doesn't toggle terminal autowrap (`ESC[?7l` never appears in the
stream); it clips the content at the source.

Confirmed empirically at an 80-column PTY (real SSH session reconstructed on a
wider grid so truncation is visible): the abbreviation *ambiguity* message and a
long admin `HELP` description line both truncate identically — their tails
(`…command name.` and `…HELP BAN for details.`) drop off the right edge and are
absent from the byte stream — while a sub-80-column `LIST USERS` row is
untouched. So it's a width-threshold rendering gap shared by *all* long
single-line lobby output; abbreviation only surfaced it because an ambiguity
error is a single unbroken line that routinely exceeds 80 columns.

**Severity: minor/stylistic; not blocking**, and explicitly *not* something the
command-abbreviation feature is gated on. The highest-value case to fix is the
ambiguity message — truncating a `did you mean: …` candidate list off-screen
defeats the message's purpose, whereas a clipped `HELP` description is cosmetic.
**Likely fix (its own task, not done here):** word-wrap history lines to
`m.width` before flatten/render (e.g. via lipgloss), and adjust the scroll math
in `View()`/`viewportHeight()` for the changed flattened line count — a wrapped
line occupies more than one display row, so the existing "1 entry = 1 line"
assumption in the scroll window would need updating.

## Known minor: WHO's per-account app label is last-writer-wins across sessions (2026-07-18)

Pre-existing, made more visible by per-session PHONE routing: `Registry.currentApp`
(shown by WHO/FINGER) is one label per *account*, set via `SetApp(username, …)`. With
two sessions of one account in different apps, WHO shows whichever wrote last (e.g.
"PHONE" while the other session sits at the LOBBY prompt). Cosmetic — presence display
only; PHONE routing/admission are per-session and unaffected. Not fixed here.

## Known minor: KICK only terminates one session of a multi-session account (2026-07-19)

> **✅ RETIRED 2026-07-20 — fixed.** `kick` moved from the per-account `entry` to
> `sessionState`, `SetKick` is now keyed by sessionID rather than username, and
> `Kick` fans out across every session and returns a count instead of a bool. The
> original entry is preserved unchanged below, per this file's convention. Full
> write-up: "KICK now terminates every session of an account (2026-07-20)".

Pre-existing and **not** cosmetic, unlike its sibling above. `entry.kick` is a single
func stored per *account*, and `SetKick` is called by `sessionMiddleware` on every
session — so the most recently registered session overwrites the previous one's kill
func. `KICK <user>` then closes **only that last session**; any earlier session of the
same account survives and stays connected.

That matters because KICK is a moderation control. An admin kicking a user who has two
sessions open gets a success message and a still-connected user, with nothing in the
output indicating a partial effect. The admin-action audit log records the KICK as
issued, so the log agrees with the admin's belief rather than with what happened.

Deliberately left as-is through the per-session PHONE routing work (`74a2ef5`), which
made session identity available but did not rework KICK. First noted in
`docs/audits/audit-2026-07-05.md`'s "Other observations", where it was filed alongside
the shared-`notify` quirk that later became PHONE audit finding 11 — see that finding
for why "low-severity multi-session edge case" was the wrong call on its neighbour.

The fix is now cheap and worth doing before KICK is relied on: the registry already
tracks per-session state, so `kick` should move from `entry` to `sessionState` and
`Kick(username)` should iterate every session of the account. Not attempted here
because it is a moderation-behaviour change and wants its own testing, not a rider on
a PHONE release.

## KICK now terminates every session of an account (2026-07-20)

Closes the entry retired above. `KICK <user>` closed exactly one session of a
multi-session account while reporting unqualified success — the admin got
`'bob' has been disconnected.` and a still-connected user, with nothing in the
output or the audit log indicating a partial effect.

**The root cause was a cardinality assumption that outlived its truth**, not a
missing check. `entry.kick` was a single `func()` stored per *account*, and
`sessionMiddleware` called `SetKick` on every session, so each new session
overwrote its predecessor's hook. That was *correct* while one account meant one
session; it silently became last-writer-wins the moment multi-session was
supported, with no code change and no error. `74a2ef5` built the per-session
substrate (`sessionState`, `sessionIndex`, threaded session IDs) but deliberately
left `kick` alone, since a moderation-behaviour change wanted its own testing
rather than riding a PHONE release.

**The fix is a move, not a redesign** — and the choice of key is the point.
`kick` moved from `entry` to `sessionState`, and `SetKick` is now keyed by
**sessionID rather than username**. Re-keying is what actually closes this:
an account-keyed setter has exactly one slot per account, so clobbering stays
permanently reachable and the code merely has to be careful; addressing the
session directly makes the bug **unrepresentable**. Same move `74a2ef5` made for
`notify`, for the same reason.

**`Kick` returns a count, not a bool.** The count is not cosmetic — the entire
harm here was an unqualified success message, so a partial or empty effect has to
be *visible*. `kickCommand` reports it (suppressed at 1 to keep the common case
clean); sessions still inside the brief `Register`→`SetKick` window are skipped
rather than counted, since a session that cannot be terminated must not be
reported as terminated.

**Behaviour change worth naming: a BAN or DELETE USER can now close N
connections where it previously closed one.** Both call `Kick` and inherit the
fan-out automatically, and both now append a session clause when non-zero. This
is the intended behaviour — a ban that leaves the banned user connected on
another session was never sensible — but it is a real change in what those two
commands do to a live system, not just a KICK fix.

**The audit log now records the outcome, not just the intent.**
`requireAdminLogged` logs at dispatch time, before the result is known, which is
precisely how the log came to agree with the admin's belief rather than with what
happened. `kickCommand` emits a second line after `Kick` returns —
`admin action: sysop KICK bob (2 session(s) terminated)` — including for zero,
since "kicked nobody" is worth having. Same two-phase shape as `CREATE USER` /
`RESET PASSWORD`, except those log their outcome from a sub-app and KICK has
none. Scoped to KICK; BAN and DELETE keep single-line logging.

**The lock discipline is load-bearing and now says so in code.** `Kick` collects
the hooks under `RLock`, releases, and only then calls them. It must not be
collapsed into one loop: `kick` is `s.Exit(0)`, whose teardown defers
`reg.Unregister(sid)`, which takes the **write** lock. `sync.RWMutex` is neither
reentrant nor upgradable, so invoking a hook while still holding `RLock` risks a
writer blocking on a reader that is itself waiting on the writer. The single-hook
version already copied-then-called for this reason; iterating generalises that
discipline rather than introducing it. Left uncommented, a two-pass loop reads
like something to tidy away.

**Verification: unit-level, and deliberately seen red first.** This is a
state/cardinality bug, not the session-interleaving-timing shape finding 11 was,
so unit coverage was the agreed bar and no live SSH pass was required. There was
previously **zero** test coverage of `Kick`/`SetKick` anywhere in the tree; six
tests now cover fan-out, sibling non-clobbering, account isolation, the
not-connected zero case, the nil-hook skip, and stale-ID tolerance.

The two regression tests were confirmed **red against pre-fix semantics** before
being accepted —
`TestKick_TerminatesEverySessionOfAccount` reporting `got 1` of 2 sessions and
`TestSetKick_DoesNotClobberSiblingSession` reporting the hook clobbered. Note how
that had to be done: a literal `git stash` of `registry.go` does **not**
demonstrate anything, because the new tests expect `Kick` to return `int` and the
old signature returns `bool` — you get a compile error, not a failing test. The
old *semantics* were reconstructed behind the new *signatures* (one shared slot
on `entry`, last-writer-wins) so the real test could run and produce a real
number. Worth remembering the next time a "confirm it fails first" step meets a
signature change: the two halves of a fix can be separated, but only on purpose.
`go build` / `go vet` / `go test ./... -race` / `gofmt -l` all clean.

**Still not fixed, and still cosmetic:** WHO's per-account app label remains
last-writer-wins across sessions (its own entry above). That one is display-only
and was explicitly out of scope here.

## v0.4.0 release: command abbreviation shipped, published to GHCR + verified end-to-end (2026-07-13)

Minor version bump to `v0.4.0` — *not* a patch `v0.3.2`: DCL command abbreviation
is a new user-facing feature, so it earns a minor bump even pre-1.0 (initially
tagged `v0.3.2`, retagged `v0.4.0` before any push). Tagged `762da5e` — the
command-abbreviation feature (`07ea9c1`) plus the two doc commits above (`d6d969b`
impl+live-verify record, `762da5e` known-minor truncation note); the code tree is
identical to the live-verified `07ea9c1`. Lightweight tag, matching v0.3.1 (tag
style across releases is mixed — v0.1.0/v0.3.0 annotated, v0.2.0/v0.3.1/v0.4.0
lightweight; either works, nothing depends on it). Tag pushed; `docker-publish.yml`
fired on the `v*.*.*` tag and built/pushed `ghcr.io/klingon00/retro-vax-bbs`
(amd64, image built 2026-07-14 00:54 UTC, config digest `sha256:3201aad7…`).

Verified to the same standard as v0.3.1 and v0.1.0 — anonymous-pull proof *plus* a
boot-and-serve check — and, new this release, an actual feature check against the
*published binary*:

- **Anonymous pull.** `docker logout ghcr.io` first, then
  `docker pull ghcr.io/klingon00/retro-vax-bbs:0.4.0` pulled every layer clean.
  The `v`-prefix strip still holds: git tag `v0.4.0` → image tag `0.4.0`.
- **Clean boot.** Detached bridge-mode run with a bootstrap admin and
  `ADMIN_HOST=0.0.0.0` (the documented bridge-mode requirement). Startup logged the
  config line, `bootstrap admin: created initial admin account "smokeadmin"`, and
  both listeners up — no `/data` crash (the image ships a pre-created `/data`
  VOLUME, so the missing-dir gotcha doesn't bite the container).
- **SSH on 2223 + dual-listener partition, confirmed by the server's own auth log:**
  `admin auth success: "smokeadmin" from 172.17.0.1:…` on the admin listener, and
  `public auth failure: admin account "smokeadmin" rejected on public listener` for
  the same account attempted on 2222.
- **Feature-in-the-artifact check (new for this release).** Drove the lobby over
  SSH and typed `WH` — it resolved to `WHO` and rendered the Interactive Users
  table with no "not a recognized command". This proves the shipped `0.4.0` image
  actually *contains* the abbreviation code, not merely that it boots — the gap a
  build-and-boot check alone leaves open.

Reusable note for the next release: **poll the GHCR registry manifest anonymously
to detect "published yet?"** when `gh` isn't installed — `GET
https://ghcr.io/v2/klingon00/retro-vax-bbs/manifests/0.4.0` with an anonymous
bearer token from `https://ghcr.io/token?scope=repository:…:pull` returns 404 while
building and 200 once pushed (here ~1 min after the tag push). And note the
`docker logout ghcr.io` used for the anonymous-pull test clears local ghcr creds —
CI is unaffected (it uses `GITHUB_TOKEN`), but a later *local* `docker push` would
need `docker login ghcr.io` again.

Throwaway container + volume + pulled image removed afterward; the release is good.

## PHONE call-admission fix: two live-testing bugs + a discovery pass (2026-07-14)

> **Update (2026-07-19) — three claims in this entry are superseded by `74a2ef5`.**
> The entry is otherwise accurate as a record of 2026-07-14 and is kept unchanged
> below; read these first, because each reads as present-tense.
> 1. *"one user, one call (finding 10) stays unenforced"* — finding 10 is
>    **retired**. It is now enforced, per **session** rather than per account: one
>    call per session, and an account may hold as many as it has sessions.
> 2. *"`TestDial_AdmitsCallerWhoseAccountIsAlreadyInACall` exists as the
>    tripwire"* — that test was **replaced**. It asserted only that `Dial` returned
>    no error, which is precisely how finding 11 hid behind a green suite; the
>    replacement drives the admitted call through to a completed answer on an
>    independent session.
> 3. The self-check rationale — *"it could never work anyway, because the ring goes
>    to the account-shared notify channel"* — no longer holds, because there is no
>    account-shared channel. **The policy survives its justification**: the
>    self-check is still deliberately account-level, so one session still cannot
>    phone another session of the same account. That is now a genuine product
>    choice rather than a concession to a routing limitation, and would need a
>    fresh decision to change.

Live testing found two PHONE bugs — a user could `DIAL` themselves, and a
participant in one active call could dial a participant in another, leaving that
callee unable to answer or reject while being re-rung every 10 seconds. A
requested discovery pass over the surrounding DIAL/ADD/ANSWER logic turned up
eight more gaps. All ten are recorded in
`docs/audits/audit-2026-07-13-phone-call-admission.md` (findings 1-8 fixed in
`3ecd86b`; 9 and 10 deferred by decision).

**The root cause was not a missing check.** `Calls.Dial`'s busy check was already
correct and complete — it scanned every active call for the callee regardless of
the dialer's state. The defect was that in-call DIAL never reached it:
`doDial` intercepts on `m.state == CallActive` and reroutes to `doAddToCall` ->
`Calls.Add`, a **separate admission path with no busy check at all**. Two doors
into one room, with the rule written on only one of them. So the fix was not to
patch each symptom but to move admission to the `Calls` chokepoint as one
`admitLocked` predicate that both `Dial` and `Add` call — the same shape as audit
finding #3's `usableAdminPredicate`, and for the same reason: a future third
caller inherits the rule instead of re-forgetting it. That one change resolves
findings 1, 2, 4, 5, 6, 7 and 8 with no symptom-specific patches.

Worth not relearning:

- **Identity granularity was the load-bearing design question, and the answer was
  an omission.** The predicate deliberately does **not** consult the *caller's*
  call membership. Checking it would refuse a dial from an account that merely has
  *another* session in a call — breaking multi-session, which is explicitly
  supported (it is the documented rationale for `RATELIMIT_BURST=5`). That same
  omission is why "one user, one call" (finding 10) stays unenforced: the fix
  neither closes nor worsens it. `TestDial_AdmitsCallerWhoseAccountIsAlreadyInACall`
  exists as the tripwire and is the one new test that passes *before* the fix too —
  it is a guard, not a regression test.
- **The self-check is account-level, so one session cannot phone another session
  of the same account.** A deliberate policy call: it could never work anyway,
  because the ring goes to the account-shared notify channel (a nondeterministic
  session wins the receive) and the dialing session sits in `CallPending`, where
  any keypress cancels before it could type ANSWER.
- **Gap E (ADD cross-cancellation) was logged as deferred, then reclassified as
  resolved-as-a-consequence** — not quietly, but as an explicit decision. The
  being-rung check keys on the callee with no per-call exception, so a second
  `%ADD` of the same target never starts a ring, making the cross-cancellation
  unreachable. Preserving it as "deferred" would have meant threading `callID`
  into the shared predicate purely to carve out an exception that keeps a known
  bug reachable. The reasoning is recorded in the audit entry rather than just a
  status flip.
- **A ring goroutine outliving its call was the one item that was not admission**
  (finding 3), and it is the same never-closed-goroutine class as audit
  finding #1. `hangupLocked` never touched `pendingAdds`, and the `Add`
  goroutine's `sendEvent` sat *outside* the call-existence guard — which only ever
  protected the participant re-notify. So a conference ring outstanding when the
  call was torn down rang its target every 10s **forever**, for a call that no
  longer existed. Both were fixed: teardown closes the rings, and the goroutine
  returns when its call is gone.

**Verification.** 10 new tests in `internal/phone/call_test.go`, each run against
the unfixed code first and confirmed red for the right reason — every one
reported `got <nil>`, i.e. the ring was *admitted*, which is precisely the bug.
`go build`/`go vet`/`go test ./...`/`gofmt -l` and `-race` all clean.

**Live SSH pass** (the abbreviation-verify recipe: isolated loopback
`127.0.0.1:4222`/`:4223`, throwaway seeded DB, pexpect): four real sessions
building two real calls, then driving the reported scenario. 8/8 checks pass,
server log free of panics/`recovered`. Crucially the **same harness was run
against a pre-fix binary built from `e3ba975` and scored 2/8**, capturing the
reported bug verbatim on the trapped callee's screen — `\x07 *** dave is calling
(20:34:36) - %ADD to conference ***`, BEL byte included. A green harness on a
fixed binary is consistent with a harness that asserts nothing; running it
against the broken build is what rules that out. Two reusable notes: **BubbleTea
runs the PTY in raw mode, so Enter is CR (`\r`) — pexpect's `sendline()` sends LF
and is silently ignored** (symptom: typed commands pile onto one line, never
submitting), and **every session must be logged in before any dialing starts**,
since `Dial` refuses a callee who is not yet connected.

## Live testing after the call-admission fix: one regression, one pre-existing gap (2026-07-14)

Manual live testing (four real terminals) caught two things the green unit suite
and the scripted live pass both missed. Logged as findings 11 and 12 of
`docs/audits/audit-2026-07-13-phone-call-admission.md`. **Both were traced by
running a repro harness against the fixed *and* the pre-fix (`e3ba975`) binaries**
— which is what settled which was a regression and which was not, rather than
reasoning about it. Worth doing that first next time a "this used to work" report
arrives; the answer here was one of each, and not the expected one.

**Finding 12 — a real regression, mine.** Finding 3's teardown cleanup sent the
pending-ring cancellation with `Caller: username`, i.e. `hangupLocked`'s
*departing participant*, not whoever started the ring. `addKey{callID, callee}`
records who was rung and from which call but never who rang: `Add` receives
`callerUsername` and threw it away. Repro: alice and bob in a call, alice rings
carol, alice drops, then bob drops — alice's exit leaves bob behind so no teardown
runs; bob's exit empties the call and fires the cleanup with `username = "bob"`.
Carol, told "alice is phoning you", is then told **"bob cancelled the call"**. It
always named whoever left **last**; reverse the order and it was accidentally
right, which is exactly the trap the regression test now pins (the test has the
inviter leave *first* — with the inviter leaving last, old and new code agree and
the test proves nothing). Fixed by making `pendingAdds` values a
`pendingRing{stop, inviter}`. Scope was attribution only: pre-fix, carol got no
cancellation at all and was still being rung (finding 3's phantom ring), so the
notification is new and correct to send — only the name was wrong.

**Finding 11 — pre-existing, NOT a regression, but the fix invited it.** Two
sessions of one account cannot both hold live calls: session 2 dials carol, carol
answers, and the first keystroke in session 2 cancels the call. Verified identical
at `e3ba975`. The chain: `notify` is one channel per *account*, both sessions arm
a receiver on it, `Answer` can only address `call.Caller` by username, and
`handlePhoneEvent` **ignores `event.CallID`** — so session 1 misreads session 2's
`EventAnswer` as a conference join, session 2 never leaves `CallPending`, and
`app.go`'s "any key cancels the outbound ring" hangs up a real call. Now
prioritized (the "PHONE per-session event routing" item under "Next concrete
steps"), not deferred open-endedly.

**The lesson worth keeping, because it is about the verification and not the
code.** The admission fix shipped `TestDial_AdmitsCallerWhoseAccountIsAlreadyInACall`
and a design-doc line saying "an account with one session in a call can still dial
from another". The predicate genuinely preserves *admission* — but that was only
ever verified as **`Dial` returning no error**, never as the resulting call
working. A guarantee got advertised that the layer beneath does not deliver, and
that is what sent someone to test it. **"The guard permits X" and "X works" are
different claims**, and a passing test for the first reads to everyone like proof
of the second. Both the test comment and the design-doc wording now say
admission-only and point at finding 11. The general form: when a fix's headline is
"we preserved capability X", the verification has to *drive X end-to-end*, not
assert that the gate didn't close.

Also note both misses were **scenario-shaped, not logic-shaped** — 12 needed a
specific leave order, 11 needed two sessions of one account. Neither is subtle in
hindsight; both were simply outside the shapes the harness exercised.

> **Update (2026-07-19):** finding 11 is fixed and live-verified — see the
> "PHONE per-session event routing" entry below. The references above to the
> design-doc and test comment "now saying admission-only" are superseded: the
> design-doc records one-call-per-**session** as a working invariant, and the
> admission-only tripwire test was replaced with one that drives the admitted call
> through to a completed answer. The lesson in this entry stands unchanged, and
> was in fact the standard the fix was held to — it is why closure required a live
> two-session pass rather than a green suite.

## PHONE per-session event routing — finding 11 fixed and live-verified (2026-07-19)

Fixes the gap recorded in the 2026-07-14 entry above: two sessions of one account
could not both hold live calls. `registry.entry.notify` was one channel per
*account*, and a Go channel is a **single-consumer queue** — so both sessions armed
a receiver on it and every control event went to whichever won the race. A sibling
session would consume the second session's `EventAnswer`, misread it as a
conference join, and leave the dialing session stuck in `CallPending`, where the
next keystroke tore down a real, answered call.

**The cheap fix was a trap and was deliberately not taken.** Filtering on
`event.CallID` in `handlePhoneEvent` looks like two lines, but it does not help:
the wrong session still *consumes* the event and then discards it, converting a
misdelivery into a silent drop. The state is still wrong; only the symptom moves.
`CallID` guards were added anyway, at the receivers, as defence-in-depth — safe
precisely *because* delivery is now per-session, not as the mechanism.

**What changed (`74a2ef5`).** The registry splits per-account presence (`entry`:
role, admin visibility, WHO/FINGER app label, KICK hook) from per-session delivery
(`sessionState`: its own notify and done channels). *(The KICK hook moved to
`sessionState` on 2026-07-20 — see "KICK now terminates every session of an
account" below. This sentence is accurate as a record of `74a2ef5`, which
deliberately left `kick` where it was.)* `Register` mints an opaque
monotonic session ID and returns it, threaded middleware → context → lobby → PHONE
→ `Participant.SessionID`, so a call membership belongs to a session rather than an
account. `Events`/`SendToSession`/`SessionsOf`/`Connected` replace the per-account
`Notify`; `HangupUser` becomes `HangupSession`.

**Admission is asymmetric on purpose.** Being-rung stays **account-level** — one
ring per callee, no per-call exception — and that is what keeps findings 4 and 8
closed: a second concurrent dial is refused at admission, so at most one pending
`Call{Callee: x}` exists and a per-session fan-out can never overlap a second ring
and clobber each session's single `pendingIncomingCallID`. Busy became
**per-session**: admitted if the callee has any idle session. The caller's own
membership is still never consulted, which is not an oversight — a `Dial`
structurally only originates from an idle session, because an in-call DIAL routes
to `Add`.

**Ring fan-out is first-answer-wins.** An incoming call rings every idle session;
the winner goes active and the losers' rings are retracted with a **distinct**
`EventAnswerElsewhere` rather than `EventHangup`. The payload is otherwise
identical and both clear the ring the same way — the separate type exists purely so
the losing session says "answered on another session" instead of reporting that the
caller cancelled, at the exact moment the caller is talking to the account's other
session. Same class of defect as finding 12: state machine right, attribution a
lie. A session racing an `ANSWER` in after the call is active is refused with
`ErrAlreadyAnswered` at the `Calls` chokepoint, so first-answer-wins is enforced by
the call layer, not by UI timing.

**Verified by a full two-session live SSH pass (S0–S6), which is the point.** This
finding exists because a green unit suite *and* a green scripted live pass both
missed it — the tests only ever asserted that `Dial` returned no error. S1 drove
the headline end-to-end (session 2 dials, answers, and *types* while session 1's
call stays untouched — the typing being what the old bug destroyed); S2 fan-out to
both idle sessions plus first-answer-wins wording in both lobby and in-app
contexts; S3 one-ring-per-callee still refused a concurrent dial with two idle
sessions rung; S4 session-aware busy from both directions; S5 finding 12's inviter
attribution still correct under the discriminating leave order; S6 a mid-call
session drop tearing down only its own call, with `1 session(s) remain, account
entry retained` confirming the account survived its session.

**One scenario could not be driven by hand, accepted.** S2's late-`ANSWER` →
`ErrAlreadyAnswered` race is unreachable manually: the retract clears the ring
faster than a person can type, so the lobby short-circuits with "no incoming call
to answer" before `Calls.Answer` runs. The call-layer guard is covered by
`TestAnswer_DoubleAnswerFirstWins`; the two *rendering* paths
(`commands.go:819`, `app.go:743`) remain unexercised and are a genuine, if minor,
coverage gap — they are the safety net for a lost or delayed retract, which is
exactly when the wording would matter.

**Finding 10 is retired by this**, not deferred: it asked whether one account
*should* hold concurrent calls, and the answer is now recorded as the standing
invariant — one call per **session**, an account may hold as many as it has
sessions. Two new findings (13, 14) were opened during the pass; see the audit
report and the next entry.

## PHONE diagnostic logging — opt-in, and it perturbs what it measures (2026-07-19)

Added `internal/debuglog` plus emit points across `internal/phone/call.go`
(`Dial`, `Answer`, `Add`, `HangupSession`) and `internal/registry/registry.go`
(`Register`, `Unregister`, `SendToSession`). Gated on **`PHONE_DEBUG_LOG=1`,
default off**. Written during the finding-11 live pass, when a one-shot anomaly
appeared and there was nothing to diagnose it with: the PHONE/registry/lobby
packages had **no logging at all** beyond the two admin-action audit lines, so
every dial, ring, answer, retract and teardown was invisible.

**Default-off is deliberate, for three separate reasons.** With the flag unset an
emit point costs one boolean test, so the binary verified in a manual pass behaves
identically to the one that ships. The lines name accounts and record who called
whom — call metadata, which has no business on stdout by default. And permanent-
but-dormant instrumentation still exists the next time an intermittent fault
appears; scaffolding stripped before commit does not.

**The caveat that matters operationally: turning the log on changes the timing of
the thing you are trying to observe.** Emit points in `call.go` use a deliberate
LIFO-deferred pattern — the log defer is registered *before* the unlock defer, so
it runs *after* it and the write lands outside `c.mu`. That ordering reads
backwards and looks like something to tidy; it is load-bearing, and each site says
so in a comment. **But it only covers each function's own line.** The per-delivery
lines come from `registry.SendToSession`, which is called from ~15 sites in
`call.go` that all hold `c.mu` — `ringLocked`, `notifyAccountLocked`,
`hangupLocked`, the `Answer` retract fan-out. Those writes are inside the phone
lock and no defer ordering in `call.go` can move them, because the lock belongs to
the caller. A two-session ring fan-out goes from two channel sends to two channel
sends plus two `log.Printf` syscalls, all under `c.mu`.

So: **a clean re-run with `PHONE_DEBUG_LOG=1` is not evidence a race is fixed.** A
widened critical section can serialize a race out of existence as easily as expose
it. The log is trustworthy for what it *records*; it is not trustworthy as proof of
absence. Making those sites lock-free would mean buffering log lines in the `Calls`
layer and flushing after unlock at every one of them — a real refactor for a
default-off diagnostic, not done, and recorded here rather than left implicit.

The highest-value line is the one in `SendToSession`'s `default:` branch. `notify`
is buffered at 8; past that the non-blocking send falls through and the event is
**discarded with no error and no trace**, which downstream is indistinguishable
from an event that was never sent — the session simply never changes state. That
discard was completely unobservable before this. The discard itself is a latent
defect in its own right, not merely a visibility gap: it is tracked as **finding
14** in `docs/audits/audit-2026-07-13-phone-call-admission.md`, where the options
(bigger buffer, always-on drop counter, treat as a session-health signal) are laid
out. Logging makes it visible when the flag is on; with the flag off, which is the
default, it is still silent.

## PHONE mid-ring disconnect — finding 9 resolved and live-verified (2026-07-20)

Closes audit finding 9, which had been deferred since 2026-07-14 because it needed
a **behaviour decision**, not a guard. The decision: **immediate teardown on both
sides — no grace period, no reconnect window, no new timer state.**

**The rationale is period-authentic, not just pragmatic.** On real VAX/VMS the
terminal/modem layer had no concept of a grace period: a dropped carrier was an
immediate, unconditional hangup and everything above simply found the line gone.
Holding a ring open for a callee who *might* come back is the modern instinct, not
the historical one. Recorded in design-doc.md's new "Mid-ring disconnect"
subsection.

**The structural finding is the part worth not relearning.** The bug was not a
missing check inside session teardown. **A callee who is being rung is not a
participant** — `Dial` puts only the caller into `call.participants`, and the
callee becomes one only on `Answer`. `HangupSession` scans participants by session
ID, so it never saw a rung-but-unanswered callee at all. That is why the ring
survived every previous teardown-hardening pass, `74a2ef5` included: not an
oversight in the teardown, but a teardown that structurally could not observe the
case. The fix is a separate step, `ReapUnreachableRings`, keyed by **username**
and reading the pending call's `Callee` plus `pendingAdds`.

**Two causes, two messages.** "No session can be rung" happens two ways and they
are not the same thing to the person waiting: the callee's last session went away
(`EventCalleeGone`), or they are still logged in with every session busy elsewhere
(`EventCalleeUnavailable`). Teardown triggers on *zero ringable*; the message is
selected by re-checking *connected*. Defaulting both to "disconnected" would say
something false about a user who is still online — the same family of defect as
finding 12's misattribution, designed out here rather than corrected later. Two
distinct types rather than one plus a discriminator field, following
`EventAnswerElsewhere`; the cost of the alternative is visible in `EventHangup`,
whose overloaded `Callee` convention is what finding 13 is a consequence of.

**Ordering is load-bearing.** The reap must run *after* `registry.Unregister`, or
the departing session still counts as ringable and nothing is reaped.
`sessionMiddleware`'s teardown became **one defer with an explicit sequence**
rather than reversed-LIFO defers — a third defer would have made readers invert
three registrations to recover the order.

**Verification, and the part that actually mattered.** 7 unit tests, 6 confirmed
red against pre-fix behaviour (the 7th is the over-reach guard and correctly
passes both ways), plus a live SSH pass: **16/16 on the fix, 11/16 on a pre-fix
binary**, with `PHONE_DEBUG_LOG` off so the widened-`c.mu` caveat does not apply.

**The live harness caught two defects in itself, and that is the lesson.** Its
first version scored **14/16 against knowingly-broken code**. Two scenarios proved
nothing: (a) a keystroke sent at the caller to test something else had *cancelled
the pending ring* through the pre-existing "any key cancels in `CallPending`"
path, destroying the stale state the next check was about to probe; and (b) a
1.5-second wait for a "resurrected ring" could never fire, because `RingInterval`
is 10 seconds. Both are properties of the system under test, invisible from
reading the harness. **A green harness on fixed code is consistent with a harness
that asserts nothing** — running it against the broken build is the only thing
that rules that out, and here it did.

**Harness kept as standing infrastructure at `test/live/`** (`livelib.py`,
`finding9_mid_ring_disconnect.py`, `README.md`) — it needs a live server and a
seeded DB, so it is deliberately outside `go test`. Its README records the five
behaviours that cost real debugging time, including the two above.

**Finding 13 is deliberately still open.** The new event types route *around* it
(each calls `goIdle()` in its own handler, so the reap path cannot strand a
session in `CallPending`), and no new `EventHangup`→`CallPending` route was added,
so it is neither worsened nor fixed. Closing it blind would have meant an
unverified one-line change riding along with a verified fix.

## Known minor: zero-ringable reached with no disconnect (2026-07-20)

Out of scope for finding 9 and recorded here rather than left as a passing
mention. The reap is driven from **session teardown**, so it only ever runs when a
session departs. The same dead-ring state is reachable without anyone
disconnecting: X is being rung, ignores it, and dials someone else — X's session
becomes a participant of its own pending call and therefore stops being ringable,
so the original caller's ring can no longer land on anybody.

Nothing reaps that, because no teardown occurs. The caller sits on "Ringing X…"
until they press a key, and X stays un-dialable to third parties while the stale
ring stands. Strictly narrower than finding 9 was: X is present and will free up,
and the re-ring goroutine recomputes ringable sessions every tick, so the ring
lands the moment X's other call ends.

Not fixed because the trigger is different in kind — closing it means reaping on
*call-state transitions* (a session becoming non-ringable) rather than on session
departure, which is a broader change than finding 9 called for and wants its own
decision about whether a ring should survive the callee getting briefly busy.

## v0.4.2 release: PHONE KICK multi-session termination + mid-ring disconnect, published to GHCR + verified end-to-end (2026-07-22)

Patch bump `v0.4.1` → `v0.4.2` — both changes are bug fixes, not new user-facing
features, so a patch earns it (same reasoning as v0.4.1's finding-11/12 fixes). The
one behaviour change worth naming — **a BAN or DELETE USER can now close N
connections where it closed one** — is a *correction* of wrong behaviour, not a new
capability, so it stays a patch rather than a minor.

Bundles five commits on top of `v0.4.1` (`ebea990`); tree = `4a5c175`:

- `9e1f3ad` **fix: terminate every session of an account on KICK** — `kick` moved
  from the per-account `entry` to `sessionState`, `SetKick` re-keyed by sessionID
  (clobbering made unrepresentable, not merely guarded), `Kick` fans out and
  returns a count, and `kickCommand` emits a second, outcome line
  (`admin action: sysop KICK bob (2 session(s) terminated)`) so the audit trail
  records what happened rather than what was intended. BAN and DELETE USER inherit
  the fan-out. Full detail: "KICK now terminates every session of an account
  (2026-07-20)" above.
- `ca414f1` docs correcting the KICK claims the per-session fix falsified.
- `a637e71` **test: the live-SSH verification harness** now at `test/live/`
  (standing infrastructure, deliberately outside `go test`).
- `e0b7855` **fix: reap PHONE rings when no session of the callee can receive
  them** (audit finding 9) — `ReapUnreachableRings`, run from session teardown
  *after* `registry.Unregister`, with the caller told *which* of two causes
  applied: `EventCalleeGone` (the callee's last session went away) vs
  `EventCalleeUnavailable` (still online, every session busy elsewhere). Immediate
  teardown, no grace window — period-authentic to a dropped VMS carrier. The
  two-message split exists precisely so a still-online user is never reported as
  disconnected. Full detail: "PHONE mid-ring disconnect — finding 9 resolved and
  live-verified (2026-07-20)" above.
- `4a5c175` docs recording finding 9 resolved, with its structural lesson (a rung
  callee is *not* a participant, so participant-keyed teardown structurally could
  never see it).

**The release gate was a hands-on live pass, not the automated suites.** This
project's standing rule is that for anything session-interleaving-shaped, "a green
suite" and "a green scripted pass" are both insufficient — finding 11 slipped past
both. So before this tag, **both** bugs were reproduced by hand on the preserved
pre-fix rig binary (`vaxbbs-prefix`, built before either fix) and then confirmed
cleared on the current build, over real SSH. KICK's automated bar was unit-level (a
state/cardinality bug, seen red first); finding 9 additionally had a live harness
scoring 16/16 on the fix vs 11/16 pre-fix — but the by-hand pass is what actually
released it.

**Scope of the release, stated honestly:** closes the KICK multi-session gap and
audit finding 9. **Still open and deliberately not in this release:** findings 13
and 14 (both low-severity, reachability unestablished) and the "zero-ringable
reached with no disconnect" known-minor (its own entry directly above) — finding
9's fix routes *around* 13 rather than closing it.

Lightweight tag, matching `v0.4.1`/`v0.4.0` (tag style across releases is mixed and
nothing depends on it).

**Verified end-to-end (2026-07-23).** The tag push fired `docker-publish.yml`,
which built and pushed `ghcr.io/klingon00/retro-vax-bbs:0.4.2` (amd64, `v`-prefix
stripped — git tag `v0.4.2` → image `0.4.2`). Verified to the v0.4.0 standard —
anonymous-pull proof, a boot-and-serve check, and the dual-listener partition read
from the server's *own* auth log — and this release went one better than v0.4.0:
the feature-in-the-artifact check exercised *its own headline fix*, not just a
generic lobby command.

- **Anonymous pull.** `docker logout ghcr.io` first, then
  `docker pull ghcr.io/klingon00/retro-vax-bbs:0.4.2` pulled clean — manifest digest
  `sha256:7841343b…`, config digest `sha256:258b4e4a…` (the 15-layer amd64 v2
  manifest confirmed earlier by an anonymous `manifests/0.4.2` poll).
- **Clean boot.** Detached bridge-mode run with a bootstrap admin and
  `ADMIN_HOST=0.0.0.0` (the documented bridge-mode requirement). Startup logged the
  config line —
  `config: public=0.0.0.0:2222 admin=0.0.0.0:2223 ratelimit=1.0/min burst=5 maxIPs=1000 authTimeout=120s registration=closed pendingExpiry=7d`
  — then `bootstrap admin: created initial admin account "smokeadmin"` and both
  listeners up (`public listener: 0.0.0.0:2222 (refuses admin-role accounts)` /
  `admin listener:  0.0.0.0:2223 (refuses non-admin accounts; …)`), no `/data` crash.
- **Dual-listener partition, from the server's own auth log.** On 2223,
  `admin auth success: "smokeadmin" from 172.17.0.1:59892` — and only after two
  `admin auth failure: wrong password for "smokeadmin"`, so the auth path is
  actually verifying, not rubber-stamping. The same account on 2222 was refused
  three separate times: `public auth failure: admin account "smokeadmin" rejected
  on public listener from 172.17.0.1:39378`.
- **Feature-in-the-artifact (abbreviation).** In the 2223 lobby, `WH` resolved to
  `WHO` — the shipped binary carries the abbreviation resolver and boots into a
  working lobby, the same gap-closing check v0.4.0 introduced.
- **Feature-in-the-artifact (*this* release's fix — new, and stronger than
  v0.4.0's check).** KICK was exercised against a real multi-session account:
  `smokeadmin` held two sessions, and a single `KICK smokeadmin` terminated
  **both**, visible as two `INFO disconnect user=smokeadmin` lines (durations
  `14.045…s` and `24m41.063…s`). The audit log carries exactly the two-phase shape
  the fix was built to produce — the dispatch-time
  `admin action: smokeadmin KICK smokeadmin` immediately followed by the new
  outcome-time `admin action: smokeadmin KICK smokeadmin (2 session(s) terminated)`.
  That is proof *in the published artifact* that both the multi-session fan-out and
  the outcome-count logging shipped, not merely that the image boots. (This check
  covers the KICK fix specifically; finding 9's proof remains the hands-on rig pass
  and the 16/16-vs-11/16 harness recorded above — a disconnect-race timing is not
  something a single-operator smoke test reproduces.)

Throwaway container removed afterward (`docker rm -f`). **The release is verified
end-to-end; v0.4.2 is fully closed.**

## PHONE finding 13 resolved by analysis — unreachable dead code (2026-07-23)

Closes audit finding 13 (`EventHangup` in `CallPending` never calls `goIdle()`,
unlike `EventReject`) **without a code change** — the branch is proven
**unreachable**, so there is nothing to fix. The deliverable is the proof and a
comment marking the branch, not a line of behaviour.

**The claim is an absence, established over finite static paths.** The
fall-through fires only when `m.state == CallPending` *and* `event.CallID ==
m.callID`. Reachability reduces to one question: can any code emit an `EventHangup`
carrying call C's ID to the session that is the `CallPending` caller of C? All five
`EventHangup` emit sites were traced and none can:

- A `CallPending` session is the **sole caller-participant** of its pending call
  (`Dial` creates `[caller]/CallPending`; the only append flips the call to
  `CallActive` in the same locked step), and caller/callee are always different
  accounts (`admitLocked` self-call reject).
- Every emitter targets either the **callee account's sessions** (`hangupLocked`
  last-participant fan-out, both `CancelAdd` sites — a different account from the
  caller) or the **remaining participants after one leaves** (`hangupLocked`'s main
  notify — and a pending call has none once its lone caller is removed; the
  active-call `pendingAdds`/`CancelAdd` sites carry an active-call `CallID` that the
  receiver's guard filters).
- The one timing route (call answered, then a participant hangs up) can't strand a
  `CallPending` session either: **finding 11's per-session FIFO delivery** means the
  session consumes the earlier `EventAnswer` (→ `CallActive`) before any later
  `EventHangup`, so the `CallActive` branch handles it. A cancelled/rejected/reaped
  pending caller instead receives `EventReject` or
  `EventCalleeGone`/`EventCalleeUnavailable`, all of which `goIdle()`.

**Deliberately not "fixed".** Adding a `goIdle()` for symmetry would be
unexercisable today — an untestable change riding along with a verified one, which
is exactly the anti-pattern this project avoided all through the finding-9/11 work
and the reason finding 13 was left open rather than patched in passing. The branch
is **kept, not deleted**: it is a harmless guard, and it is the correct place to
handle the event if a future emit site ever addresses a pending caller. A comment
at the fall-through in `internal/phone/app.go` records the unreachability, names the
two conditions that would reopen it (a new `EventHangup` emitter addressing a
pending caller, or a break in per-session FIFO delivery), and warns against both
deleting the branch and blind-adding the `goIdle()`.

**No live SSH pass, by design.** Live testing cannot prove a negative better than
this trace does — the claim is the non-existence of a route through static code,
not a behaviour under load. Full trace and the emit-site table live in
`docs/audits/audit-2026-07-13-phone-call-admission.md` finding 13. Finding 14
(latent buffer-full discard) remains the only open item from that audit.

## Next concrete steps

1. ✅ **VAX/VMS command abbreviation** — shortest unambiguous prefix (DCL style).
   **Done: implemented 2026-07-12, live-SSH-verified 2026-07-13** (see the two
   entries above). No further work outstanding on this feature.
2. ✅ **KICK multi-session fix.** **Done 2026-07-20** — `kick` moved from the
   per-account `entry` to `sessionState`, `SetKick` re-keyed by sessionID, `Kick`
   fans out across every session and returns a count, and the audit log gained an
   outcome line. BAN and DELETE USER inherit the fan-out. See "KICK now
   terminates every session of an account (2026-07-20)" above. Unit-verified with
   the two regression tests confirmed red against pre-fix semantics first; no live
   SSH pass required for this one (state/cardinality bug, not an interleaving
   one). No further work outstanding.
3. Unraid Community Applications submission — icon asset done (`icon.png`
   at repo root, 256x256 transparent, wired into `unraid-template.xml`'s
   `<Icon>` as of 2026-07-04); CA repo listing itself still open. Gated on
   the manual GHCR steps above, which are confirmed working end-to-end
   (`docker pull` succeeding anonymously). Ops-only, no coding.
4. ✅ **PHONE per-session event routing** (finding 11). **Done: fixed in
   `74a2ef5`, live-verified 2026-07-19** by a full two-session SSH pass (S0–S6) —
   see the "PHONE per-session event routing" entry above. The registry now splits
   per-account presence from per-session delivery. The `CallID`-filter shortcut was
   correctly avoided (single-consumer queue: the wrong session consumes and then
   drops), and the finding 10 policy question it forced is **answered, not
   deferred** — the enforced unit is the session. No further work outstanding on
   this finding.
   - **The design question this item used to pose is answered, not dropped.** It
     asked: with per-session channels, which session rings, and can two of them
     answer the same call? **All idle sessions of the callee ring**
     (`ringableSessionsLocked` fans out to every session not already in a call),
     and **no, two cannot both answer** — first-answer-wins is enforced at the
     `Calls` chokepoint, so a second session racing an `ANSWER` gets
     `ErrAlreadyAnswered` and is never appended as a participant, while its ring is
     retracted with `EventAnswerElsewhere`. Pinned by
     `TestAnswer_DoubleAnswerFirstWins` (which asserts exactly two participants,
     not merely that the second call errored) and driven live in S2.
   - **Remaining from the release, not blocking:** push the four local commits and
     tag `v0.4.1`, framed as *"closes findings 11 and 12; records 13 and 14 as
     open"* rather than "audit fully closed".
5. ✅ **PHONE call-state: the deferred design question is answered** (finding 9 of
   `docs/audits/audit-2026-07-13-phone-call-admission.md`, deferred 2026-07-14).
   **Done 2026-07-20** in `e0b7855`: immediate teardown on both sides, with the
   caller told *which* of two causes applied (`EventCalleeGone` vs
   `EventCalleeUnavailable`). Live-verified 16/16 on the fix against 11/16
   pre-fix — see "PHONE mid-ring disconnect — finding 9 resolved and
   live-verified (2026-07-20)" above. **Finding 10 was also listed here and is
   now retired** — answered by the per-session routing work on 2026-07-19, see
   the "PHONE per-session event routing" item above; its entry below is kept for
   the record with that disposition noted. The original framing of finding 9 is
   preserved below, since it explains why the admission fix deliberately left it
   alone:
   - **Callee disconnects mid-ring** — the caller sits on "Ringing X…" forever
     with no "X has disconnected", and a reconnecting X gets rung for a call
     placed before they logged in. Needs a behavior call (tear down and notify
     the caller? grace window for a reconnect?). **Its priority rose as a side
     effect of the admission fix**: a stale ring now also makes the disconnected
     user un-dialable by anyone until it clears, so it costs a reconnecting user
     inbound calls, not just a confused caller.
   - ✅ **"One user, one call" is assumed but never enforced** — **RETIRED
     2026-07-19.** The original text is kept below for the record. It called for
     "deciding whether an account may hold concurrent calls at all, and if not,
     plumbing session identity through the registry — which is keyed by username
     today with no per-session handle." That is exactly what `74a2ef5` did: session
     identity now exists, the registry is no longer username-keyed for delivery,
     and the decision is that an account **may** hold concurrent calls — one per
     session. `HangupUser` became `HangupSession`, so teardown can no longer hang
     up the wrong call.
     *Original entry:* `HangupUser`'s comment asserts it; nothing checks it, so
     with two sessions one account can be in two calls, and teardown may hang up
     the wrong one. Unaffected by the admission fix by design (the predicate never
     consults the *caller's* membership, precisely so multi-session keeps working).
6. **Dependency refresh** — deferred as its own task (flagged 2026-07-13).
   `go list -u -m all` shows nearly the whole module tree has newer versions,
   including major bumps: `charmbracelet/bubbles v0.21.0 → v1.0.0` (a direct dep —
   the `textarea` behind SET PLAN) and `charmbracelet/log v0.4.1 → v1.0.0`, plus
   `golang.org/x/*` (crypto, sys, net, text, term), `go-git/v5`,
   `modernc.org/{cc,ccgo,libc}`, and the `charmbracelet/x/*` sublibs. The core
   TUI/SSH/DB direct deps are already current (`bubbletea v1.3.10`,
   `lipgloss v1.1.0`, `wish v1.4.7`, `modernc.org/sqlite v1.53.0` — no updates), so
   nothing here is urgent. Deliberately NOT done during a housekeeping pass:
   `bubbles v1.0.0` is a major bump layered on an un-updated `bubbletea v1.3.10`,
   exactly the kind of TUI-core version skew that can break rendering subtly, and
   this whole app is TUI-over-SSH. Do it in a dedicated session and **re-verify
   after** — `go build ./...` / `go vet ./...` / `go test ./...` *plus* a live SSH
   pass (the pexpect + isolated-server approach used for the command-abbreviation
   verify), not just a clean build.
