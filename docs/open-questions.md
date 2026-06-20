# VMS/PHONE Revival — Open Questions & Notes

Companion to the main design doc. This is the "still soft" stuff — things acknowledged but not yet designed in detail, plus a place to track what's actually been built.

## Not yet designed

- **Mail app** — interface contract exists (modular app framework), but no UX/content design yet.
- **Text game** — acknowledged as a future modular app, nothing scoped beyond that.
- **Color/emphasis** — opt-in negotiation agreed at a high level (both sender and receiver must opt in). Exact command syntax (e.g. `SET COLOR ON`) and which UI elements support emphasis: not yet detailed.
- **External notifications** — hook point reserved in the login/presence path, but the actual mechanism (webhook vs. ntfy-style push vs. something else), subscription command syntax, and notification rate-limiting are all undesigned.
- **Unraid Community Apps template** — not started. XML template, icon, port-mapping documentation, README for the listing: all pending.
- **CIDR-based admin allowlist** — documented as an alternative/complement to the dual-listener split, not implemented, not required (the listener split is the primary mechanism).
- **Multi-session `WHO` display** — agreed that concurrent sessions from one account are allowed (true to the original VMS-cluster PHONE experience) and should show as a count, e.g. `alice (2 sessions)`. Exact display format not finalized.
- **VMS-style command abbreviation** — agreed as a nice-to-have (shortest unambiguous prefix), not yet scoped into v1 build order.
- **Argon2id tuning** — rough starting params given (~64MB memory, 3 iterations) but not benchmarked against actual deployment hardware.

## Decisions explicitly deferred on purpose

These came up but were intentionally pushed past v1 — don't reopen them without a reason:

- Color/emphasis terminal options
- External notification hooks
- Mail and text-game apps
- Unraid packaging

## Build status

*(Update this section as work actually happens — nothing built yet as of this doc's creation.)*

- [ ] Project scaffolding
- [ ] Lobby shell / command dispatcher
- [ ] Account & auth (registration modes, argon2id, lockout, rate limiting)
- [ ] Dual-listener split (public / admin)
- [ ] `WHO` / `FINGER`
- [ ] PHONE app (`DIAL` / `ANSWER` / `HANGUP` / `ADD`)
- [ ] Docker packaging

## Next concrete step (as of this doc's creation)

The conversation had narrowed to three options — write the design doc (done, this is it), mock up the actual prompt/command UX text, or start scaffolding the Go project. Pick one to resume.
