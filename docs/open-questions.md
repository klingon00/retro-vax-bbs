# Retro VAX-BBS — Open Questions & Notes

Companion to the main design doc. This is the "still soft" stuff — things acknowledged but not yet designed in detail, plus a place to track what's actually been built.

## Not yet designed

- **Mail app** — interface contract exists (modular app framework), but no UX/content design yet.
- **Text game** — acknowledged as a future modular app, nothing scoped beyond that.
- **Color/emphasis** — opt-in negotiation agreed at a high level (both sender and receiver must opt in). Exact command syntax (e.g. `SET COLOR ON`) and which UI elements support emphasis: not yet detailed.
- **External notifications** — hook point reserved in the login/presence path, but the actual mechanism (webhook vs. ntfy-style push vs. something else), subscription command syntax, and notification rate-limiting are all undesigned.
- **Unraid Community Apps template** — not started. XML template, icon, port-mapping documentation, README for the listing: all pending.
- **CIDR-based admin allowlist** — documented as an alternative/complement to the dual-listener split, not implemented, not required (the listener split is the primary mechanism).
- **Multi-session `WHO` display** — agreed that concurrent sessions from one account are allowed (true to the original VAX/VMS-cluster PHONE experience) and should show as a count, e.g. `alice (2 sessions)`. Exact display format not finalized.
- **VAX/VMS-style command abbreviation** — agreed as a nice-to-have (shortest unambiguous prefix), not yet scoped into v1 build order.
- **Argon2id tuning** — rough starting params given (~64MB memory, 3 iterations) but not benchmarked against actual deployment hardware.
- **Third-party notices file** — license is MIT (see below), all current and planned dependencies are MIT/BSD-3-Clause, but a proper notices file listing each dependency's license hasn't been created yet. Good practice before public/Unraid release.

## Decisions explicitly deferred on purpose

These came up but were intentionally pushed past v1 — don't reopen them without a reason:

- Color/emphasis terminal options
- External notification hooks
- Mail and text-game apps
- Unraid packaging

## Build status

- [x] Project scaffolding
- [x] Lobby shell / command dispatcher
- [ ] Account & auth
  - [x] SQLite schema + argon2id password hashing
  - [x] Closed-mode (admin-created) accounts + real `wish` login auth
  - [ ] Registration modes (invite-only / open-with-approval)
  - [x] Account lockout
  - [x] Per-IP rate limiting
- [ ] Dual-listener split (public / admin)
- [ ] `WHO` / `FINGER`
- [ ] PHONE app (`DIAL` / `ANSWER` / `HANGUP` / `ADD`)
- [ ] Docker packaging

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
- `WHO` is a stub — real behavior (browsable list, admin invisibility,
  multi-session counts) is blocked on the account/session registry that
  comes with the auth milestone, not yet designed in code.
- No authentication exists yet. Current build binds to `localhost` only
  and accepts any username — explicitly not safe to expose, by design,
  until the auth milestone lands.
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
  successful login. Admin `UNLOCK` command (future) calls
  `ClearFailedAttempts` to release early. Tested with in-memory SQLite
  in `internal/store/store_test.go`. Live end-to-end test confirmed:
  lock triggered at 5th attempt, correct password rejected during lock
  window, access restored after 15 minutes with counter reset confirmed.
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

## Next concrete step (as of 2026-06-22)

Per-IP rate limiting done. The auth sub-checklist is now: registration
modes (invite-only / open-with-approval) remaining. Before that, the
dual-listener split (public port refuses admin accounts, admin-only port
refuses non-admin accounts) is worth considering — it's the structural
security boundary the design doc calls out as the primary admin-access
control mechanism, and registration modes will need to know about account
roles to work correctly anyway.
