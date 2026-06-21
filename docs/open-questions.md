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
- [ ] Account & auth (registration modes, argon2id, lockout, rate limiting)
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

## Next concrete step (as of 2026-06-20)

Scaffolding is done and tested. Next milestone per the build order is
account & auth: SQLite schema (users/invites — see design doc), argon2id
password hashing, registration modes (invite-only / open-with-approval /
closed), and lockout.
