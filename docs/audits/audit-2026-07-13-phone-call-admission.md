# PHONE Call-Admission Audit — Findings Report

*Findings-only audit, 2026-07-13, triggered by two bugs found in live testing
(self-dial; in-call-to-in-call dial with no busy handling) plus a requested
discovery pass over the surrounding DIAL/ADD/ANSWER call-state logic. Same format
and conventions as `audit-2026-07-05.md`: this is a **living record**, not a
frozen snapshot — as findings are resolved each is marked in place with a
`**Status:** ✅ Fixed in <commit>` line rather than deleted, so the file preserves
both the original findings and their disposition. No code was changed by the
audit itself.*

Originally ten findings from the read-only pass, ranked most-severe first. Seven
shared a single root cause and are fixed by one centralization; three were logged
as independent design questions. **Findings 11 and 12 were added later**, from
manual live testing that the unit tests did not catch — see "Found later" below.
**Finding 13 was added later still**, by inspection while verifying finding 11's
fix — see "Found during finding 11 verification" below.

**Disposition (2026-07-14; finding 13 added 2026-07-19):**

| Findings | State |
|---|---|
| 1–7 | ✅ Fixed in `3ecd86b` |
| 8 | ✅ Resolved as a consequence of the shared predicate (reclassified from deferred — reasoning in its entry) |
| 9 | ⏸️ Deferred — disconnect-mid-ring behavior; priority *raised* by the fix (see its entry) |
| 10 | ⏸️ Deferred as a policy question, but **upgraded** from "low likelihood": its mechanism is finding 11, reproduced live |
| 11 | 🔴 **Open, prioritized follow-up** — per-account event routing breaks a second session's live call. Pre-existing (verified at `e3ba975`), *not* a regression, but the fix advertised a guarantee this layer doesn't deliver |
| 12 | ✅ Fixed — pending-ring cancellation named the wrong person (a real regression from finding 3's fix) |
| 13 | 🔵 **Open, unconfirmed** — `EventHangup` in `CallPending` never returns the session to idle, unlike `EventReject`. Found by inspection, reachability not established, no fix attempted |
| 14 | 🔵 **Open, latent** — `SendToSession` silently discards an event when a session's notify buffer is full. Pre-existing and by design; recorded because the failure is invisible and indistinguishable from finding 11's symptom |

**A note on what the unit tests missed, worth keeping.** Findings 11 and 12 were
both caught by a human driving four real terminals, after a green suite and a live
SSH pass. Neither was subtle in hindsight: 12 needed a *specific leave order* to
show up (the inviter leaving first), and 11 needed *two sessions of one account*
— a shape the harness only exercised as far as "does Dial return an error?" So
the coverage gap was scenario-shaped, not logic-shaped. Tests that assert a guard
permits something prove strictly less than driving the thing it permits.

**Root cause shared by findings 1–2 and 4–7:** admission rules ("may this ring be
placed?") live per-path in the UI layer rather than at the `Calls` chokepoint.
`Calls.Dial` carries a connectivity + busy check; `Calls.Add` carries only a
connectivity check; neither carries a self-check. `doDial` reroutes
DIAL-while-in-a-call to `Add`, so the busy rule silently does not apply there.
This is the same shape as audit-2026-07-05 finding #3 (`lastUsableAdminGuard`), and
takes the same fix: one shared predicate every path references.

**Why these shipped:** `internal/phone/call_test.go` contains exactly four tests,
all inherited from the finding #1 channel-close work
(`TestHangup_ClosesDepartingParticipantOnly`,
`TestHangupUser_RemovesFromActiveCallWithoutCallID`,
`TestHangup_IdempotentNoDoubleClose`, `TestReject_ClosesCallerChannel`). **Nothing
covers `Dial`'s busy check, self-dial, or `Add` at all** — the entire
call-admission surface is untested.

---

## Definite bugs — fixed in this pass

### 1. In-call → in-call DIAL has no busy handling; the callee is trapped

**Status:** ✅ Fixed in `3ecd86b` (`fix: centralize PHONE call admission; stop ADD rings outliving their call`). In-call DIAL still reroutes to Add (kept: it matches real VAX/VMS and the existing PHONE HELP text), but Add now runs the same `admitLocked` predicate as Dial, so the reroute inherits the busy rule instead of bypassing it. Confirmed over live SSH: with two real calls established, the in-call dial returns `%PHONE-E-BUSY, bob is already in a call.` and the callee is never rung. The same harness reproduces the original trapped-callee ring against a pre-fix binary.

**Severity: definite bug (medium-high impact) · Confidence: high**

- **Where:** `internal/phone/app.go:644-648` (`doDial`'s reroute),
  `internal/phone/call.go:339-352` (`Add`, no busy check),
  `internal/phone/app.go:337-352` (`EventRing` handler's non-idle branch).
- **What:** `Calls.Dial`'s busy check (`call.go:77-85`) is **correct and complete**
  — it scans every active call for the callee regardless of the dialer's state.
  The defect is that in-call DIAL never reaches it: `doDial` intercepts on
  `m.state == CallActive` and reroutes to `doAddToCall` → `Calls.Add`, which
  checks only `reg.Notify(callee) == nil` for connectivity and has **no busy check
  at all**. The two admission paths have asymmetric rules.
- **Failure scenario:** Calls A and B are both active. A participant in call B
  dials a participant in call A. The ring is admitted. The callee is in
  `CallActive`, so their `EventRing` handler takes the non-idle branch, which
  shows a notification and rings the bell but **never sets
  `pendingIncomingCallID`** — the sole gate on `doAnswer:694` and `doReject:727`.
  ANSWER returns "no incoming call to answer"; REJECT returns "no incoming call to
  reject." The `Add` ring goroutine re-rings every 10s and stops only via
  `Answer`/`Reject` on its key or `CancelAdd` — none reachable by the callee. Only
  the *caller* can stop it (any keypress → `cancelPendingAdd:769`). Confirmed live.
- **Smoking gun:** the notification the trapped callee is shown reads
  `*** X is calling (HH:MM:SS) — %ADD to conference ***` (`app.go:350`) — inviting
  them to ring the caller into *their* call, i.e. the call-merging behavior that is
  explicitly not wanted.

### 2. `Calls.Add` has no busy check (independently reachable via `%ADD`)

**Status:** ✅ Fixed in `3ecd86b`. `Add` now calls `admitLocked` — the same predicate `Dial` uses — instead of its connectivity-only check, so `%ADD` of someone already in another call is refused on its own merits, not just as a side effect of fixing the DIAL reroute.

**Severity: definite bug (medium impact) · Confidence: high**

- **Where:** `internal/phone/call.go:339-352`.
- **What:** The root cause of finding 1, but also a bug in its own right: the
  legitimate conference command `%ADD <user>` has the identical hole. `Add` checks
  connectivity only.
- **Failure scenario:** `%ADD <someone already in another call>` rings them with the
  same trapped-callee result. Fixing finding 1 at `doDial`'s routing alone would
  leave the real conference command broken — the reason the fix belongs at the
  chokepoint, not the route.

### 3. ADD ring goroutine outlives its call — leak plus an eternal phantom ring

**Status:** ✅ Fixed in `3ecd86b`. `hangupLocked` now closes and deletes every `pendingAdds` entry for a call being torn down, notifying the rung callee via the existing `EventHangup` Callee-non-empty convention so their prompt clears. The ring goroutine additionally returns if the call is gone: its `sendEvent` was moved *inside* the `c.mu` + call-existence guard that previously protected only the participant re-notify. Covered by `TestAdd_RingStopsWhenCallIsTornDown`.

**Severity: definite bug (medium impact) · Confidence: high**

- **Where:** `internal/phone/call.go:386-407` (the goroutine), root cause at
  `internal/phone/call.go:295-333` (`hangupLocked`).
- **What:** `hangupLocked` **never touches `pendingAdds`**. The goroutine stops only
  on its `stopRing`, closed exclusively by `Answer:150`, `Reject:210`, or
  `CancelAdd:419`. So when a call is deleted with a conference ring outstanding,
  nothing stops it — and its `sendEvent` is **unconditional**, sitting outside the
  call-existence guard, which protects only the participant re-notify:

  ```go
  case <-ticker.C:
      c.sendEvent(calleeUsername, ringEvent)   // unconditional
      c.mu.Lock()
      if call2, ok := c.calls[callID]; ok {    // guards only the re-notify
  ```
- **Failure scenario:** A participant `%ADD`s carol; before she answers, everyone
  in the call hangs up. The call is deleted from `c.calls`. Carol is rung **every
  10 seconds forever**, for a call that no longer exists; typing ANSWER gives
  `call ... not found` (`call.go:137`). The goroutine leaks for process lifetime.
- **Note:** same never-closed-goroutine class as audit-2026-07-05 finding #1, which
  is why it is bundled with the admission fix rather than deferred.
- **This fix shipped with a bug of its own** — the cancellation it added named the
  wrong person. See finding 12 (fixed in `7187581`). The teardown and the ring
  goroutine's guard were correct; only the `Caller` on the notification was not.

### 4. Two calls can ring the same target; the loser's goroutine rings forever

**Status:** ✅ Fixed in `3ecd86b` — at the source, not by cleanup. `admitLocked` refuses a callee who already has any ring outstanding, so two concurrent rings to one target can no longer be created and there is no loser goroutine to leak. Covered by `TestDial_RejectsCalleeBeingAddedToConference`.

**Severity: definite bug (low-medium impact) · Confidence: high**

- **Where:** `internal/phone/call.go:355-360` (`pendingAdds` keyed
  `callID + ":" + callee`), `call.go:149-153` (`Answer`'s selective close).
- **What:** Two different calls ADDing the same target produce different `callID`s
  → different keys → two concurrent ring goroutines. `Answer` closes only its own
  key's `stopRing`.
- **Failure scenario:** Calls A and B both `%ADD` carol. She answers A. B's ring
  goroutine is never stopped and rings her **forever**, mid-call, with no way to
  clear it. Closed at the source by the admission predicate's being-rung check.

### 5. Busy check ignores `CallPending`, so mid-ring targets are dialable

**Status:** ✅ Fixed in `3ecd86b`. The participant scan now covers `CallPending` as well as `CallActive`, and a dedicated check catches a callee who is the `Callee` of a pending call, returning `ErrBeingRung`. Covered by `TestDial_RejectsCalleeAlreadyBeingRung`.

**Severity: definite bug (low-medium impact) · Confidence: high**

- **Where:** `internal/phone/call.go:78` (`if call.State == CallActive`).
- **What:** The busy scan considers only active calls, so a callee who is already
  being rung (pending, unanswered) is admitted as free.
- **Failure scenario:** Bob dials carol. Before she answers, alice dials carol too.
  Both admitted. Both rings write `pendingCallID` — last writer wins
  (`internal/lobby/model.go:235`, `internal/phone/app.go:342`) — so **the earlier
  caller's call becomes permanently unanswerable**: carol has no reference to it.
  If she answers alice's call, bob's ring goroutine keeps ringing her mid-call
  indefinitely, and bob sits at "Ringing carol…" until he presses a key.

### 6. Self-dial is admitted by every gate

**Status:** ✅ Fixed in `3ecd86b`. `admitLocked` rejects a self-target before any ring logic, on both Dial and Add, returning `%PHONE-E-SELF, You cannot phone yourself.` Uses `strings.EqualFold`, so `DIAL ALICE` typed by alice names the real cause instead of falling through to the misleading "ALICE is not connected". Confirmed live in both cases. Note the self-check is account-level, so one session of an account cannot dial another session of the same account — a deliberate policy call, recorded because it could not work anyway (the ring goes to the account-shared notify channel, so a nondeterministic session receives it, and the dialing session sits in `CallPending` where any keypress cancels before it could type ANSWER).

**Severity: definite bug (low impact) · Confidence: high**

- **Where:** `internal/phone/call.go:70-126` (`Dial`) — no caller/callee comparison
  exists anywhere in the package; also `internal/lobby/commands.go:789`,
  `internal/phone/app.go:653`.
- **What:** `Dial` checks only that the callee is connected (you are) and not in an
  active call (you aren't). `DIAL <self>` creates a real `CallPending` call with
  `Caller == Callee == you`.
- **Failure scenario:** It rings **your own bell** immediately and every 10s.
  `phone.New` (`commands.go:794`) builds viewports from self + callee, so you get
  **two viewports both labeled with your own username** — and `charArrivedMsg`'s
  routing loop `break`s at the first username match (`app.go:205-218`), leaving the
  duplicate dead. The `EventRing` lands on your own session in `CallPending`, which
  falls past the `CallIdle` branch (`app.go:340`) to the in-call branch, telling
  you `*** <you> is calling (HH:MM:SS) — %ADD to conference ***`. Self-limiting:
  in `CallPending` any keypress cancels (`app.go:384`). So: not a crash, not a
  no-op, not an infinite hang — an absurd state plus an unsolicited bell loop until
  a keystroke. `%DIAL <self>` from PHONE-idle is the same path; `%ADD <self>`
  while in a call rings you into your own call.

### 7. `ADD` can target someone already in the same call

**Status:** ✅ Fixed in `3ecd86b` — by the shared predicate, with no bespoke check. The participant scan already sees the target in the current call and returns `ErrBusy`, which prevents both the spurious ring and the duplicate-participant append. Covered by `TestAdd_RejectsExistingParticipant`.

**Severity: definite bug (low impact) · Confidence: high**

- **Where:** `internal/phone/call.go:339-352` (no participant dedup),
  `call.go:140-144` (`Answer`'s conference branch appends without dedup).
- **What:** No check that the ADD target is already a participant.
- **Failure scenario:** `%ADD <existing participant>` rings them; they are
  `CallActive` so they cannot answer (finding 1's mechanism) — spurious 10s rings
  until the caller presses a key. Were they able to answer, `Answer` would append
  them a **second** time: duplicate participant, duplicate viewport, doubled
  characters.

---

## Resolved as a consequence of the shared predicate

### 8. Cross-cancellation on same-call double-ADD

**Status:** ✅ Resolved in `3ecd86b` as a consequence of the admission predicate —
**reclassified from "deferred"**; see the reasoning below.

**Severity: definite bug (low impact) · Confidence: high**

- **Where:** `internal/phone/call.go:355-360` (`Add` closes and overwrites the
  existing key) vs. `internal/phone/app.go:769-774` (`cancelPendingAdd`).
- **What:** Two participants in one call both `%ADD carol` produce the **same**
  `callID:carol` key. The second `Add` closes the first's `stopRing` and overwrites
  the entry — but the first caller's `pendingAddTarget` is still set, so their next
  keystroke calls `CancelAdd` and kills the **second** caller's ring.
- **Originally logged as deferred** on the reasoning that second-ADD semantics were
  a policy question (reject it? no-op? track per-inviter?) deserving their own
  discussion, and that the predicate would only resolve it *if* the being-rung check
  treated a same-call re-ADD as busy — flagged at the time as the open interaction.
- **Why it was reclassified rather than fixed separately:** that interaction was
  decided deliberately, not by accident. The being-rung check keys on the **callee**
  with no per-call exception — one ring per callee at a time — so the second `%ADD
  carol` is refused with `ErrBeingRung` and never starts a ring. With only one ring
  able to exist, the cross-cancellation is **unreachable**: the second inviter's
  `doAddToCall` returns the error and never sets `pendingAddTarget`, so there is no
  second ring for the first inviter's keystroke to kill. The alternative — threading
  `callID` into the shared predicate to carve out an exception — would have added a
  parameter and a special case *solely to preserve a known-bad behavior*, and would
  still leave `%ADD` of an already-being-rung target starting a redundant second
  ring. The policy question turned out to be answered by the same principle the rest
  of the fix rests on: **a callee already being rung is not available.**
- **Nothing is foreclosed:** the `pendingAddTarget` / `CancelAdd` mechanism is
  untouched and still keyed per (call, callee). If per-inviter ADD tracking is ever
  wanted, the only change needed is relaxing the being-rung check — the machinery is
  still there. Covered by `TestAdd_RejectsSecondAddOfSameTarget`.

---

## Deferred by decision — independent design questions, not instances of the admission-chokepoint bug

*These are **not** fixed. Each needs its own discussion before a fix is designed;
neither is resolved as a side effect of centralizing admission.*

### 9. Callee disconnects mid-ring → caller hangs on "Ringing X…" forever

**Status:** ⏸️ Deferred — needs a behavior decision, not just a guard.

**Severity: definite bug (low-medium impact) · Confidence: high**

- **Where:** `internal/phone/call.go:487-496` (`sendEvent` no-ops on
  `reg.Notify == nil`), `call.go:112-123` (`Dial`'s ring goroutine).
- **What:** When the callee disconnects mid-ring, `sendEvent` silently no-ops
  forever; nothing closes `stopRing` and the `CallPending` call is never reaped.
- **Failure scenario:** The caller's status line reads "Ringing X…" **indefinitely**
  with no "X has disconnected", self-clearing only on a keypress. Worse: if X
  reconnects, `Register` creates a fresh entry and the still-running goroutine finds
  the new channel — X is rung for a call placed before they logged in.
- **Why deferred:** the fix requires deciding what *should* happen (tear the call
  down and notify the caller? keep ringing for a grace window in case they
  reconnect?) — a UX decision, not a missing check.
- **New interaction with the finding 1–8 fix (`3ecd86b`), worth knowing before
  designing this:** the admission predicate's being-rung check now consults
  `pendingAdds`, so a *stale* ring left behind by a callee who disconnected
  mid-ADD makes that callee un-dialable — anyone dialing them gets
  `%PHONE-E-BUSY, X is already being called.` until the ring is cancelled or its
  call is torn down (teardown now clears it — finding 3). This is **not a
  regression**: the message is truthful, because the stale ring goroutine really
  does resume ringing X the moment they reconnect (exactly the resurrection
  behavior described above). But it makes this finding's blast radius wider and
  more visible than it was, which raises its priority relative to when it was
  first logged: it now costs a reconnecting user *inbound* calls, not just a
  confused caller.

### 10. "One user, one call" is assumed but never enforced (multi-session)

**Status:** ⏸️ Deferred as a *policy* question — but **no longer low-likelihood**,
and now coupled to finding 11, which should be worked with it.

**Severity: upgraded 2026-07-14 from "plausible risk (low likelihood)" to a real,
reproduced gap · Confidence: high on the gap; reachability is no longer
theoretical**

> **Update (2026-07-14):** the "rings race for whichever session's receiver wins"
> mechanism noted below is no longer hypothetical — it was reproduced live and is
> written up as finding 11, where it silently kills an answered call. This entry
> stays open as the *policy* half (**should** one account hold concurrent calls?);
> finding 11 is the *mechanism* half (event delivery cannot address a session
> even if we decide it should). Answering 11 forces answering this, because
> per-session delivery has to decide which session rings.

- **Where:** `internal/phone/call.go:263-282` (`HangupUser`), whose comment states
  the invariant: *"A user is only ever in one call at a time, so the first match is
  the only one."* Nothing enforces it — `Dial` checks only the **callee's**
  membership.
- **What:** With two concurrent sessions — explicitly supported, and the documented
  rationale for `RATELIMIT_BURST=5` — one account can be in two calls at once
  (session 1 in a call, session 2 dials from the lobby).
- **Failure scenario:** `HangupUser(alice)` on one session's teardown hangs up
  whichever call it finds first in map order — possibly the **other** session's,
  leaving a phantom participant in the real one. Separately, `notify` is shared
  per-account (`registry.go:47-51`), so rings for either call race for whichever
  session's receiver wins.
- **Explicitly NOT addressed by the admission fix, by design:** the centralized
  predicate deliberately does **not** consult the caller's call membership —
  checking it is exactly what would break documented multi-session behavior
  (alice-session-1 at the lobby could no longer dial anyone while alice-session-2
  is in a call). So this fix neither closes nor worsens this gap. Closing it means
  first deciding the policy question — *should* one account hold concurrent calls?
  — and, if not, plumbing session identity through the registry, which today is
  keyed by username with no per-session handle.

---

---

## Found later, in live testing (2026-07-14)

*Two issues manual live testing caught that the unit tests did not. Both were
traced with a repro harness run against **both** the fixed and the pre-fix
(`e3ba975`) binaries, which is what settled which was a regression and which was
not — worth doing before assuming either way.*

### 11. Per-account event routing breaks a second session's live call

**Status:** 🔴 Open — **prioritized follow-up, own session.** Not deferred
open-endedly: it is reachable today and silently kills a live, answered call.

**Severity: definite bug (high impact) · Confidence: high — reproduced live**

- **Not a regression, verified.** The identical repro (alice session 1 in a call
  with bob; session 2 dials carol; carol answers; alice types in session 2 → the
  call cancels and session 2 drops to the idle `%` prompt) behaves **exactly the
  same on `e3ba975`**, before any of the admission work. This is pre-existing.
- **But the admission fix is what made it reachable-by-invitation**, which is why
  it is logged here rather than as unrelated: `3ecd86b` documented and unit-tested
  "an account with one session in a call can still dial from another" as a
  preserved guarantee. The predicate does preserve *admission* — but that was only
  ever verified as `Dial` returning no error, never as the resulting call working.
  A guarantee was advertised that the layer beneath does not deliver, which is
  what prompted someone to test it. The design-doc wording and the test comment
  have since been corrected to say admission-only.
- **Where:** `internal/registry/registry.go:47-51` and `:85-100` (`entry.notify`
  is one channel per **account**; `Register` reuses it on `count++`),
  `internal/lobby/model.go`'s `subscribePhoneEvents` → `m.reg.Events(m.username)`,
  `internal/phone/call.go`'s `Answer` (sends `EventAnswer` to `call.Caller` — a
  username), and `internal/phone/app.go:247-269` (`EventAnswer` branch).
- **Root cause chain:**
  1. `notify` is account-addressed and shared by every session of that account —
     there is no session identity anywhere in the registry to address instead.
  2. Each session's lobby arms its own `waitForPhoneEvent` on that *same* channel,
     so two sessions race and each event is consumed by exactly **one**,
     nondeterministically.
  3. `Answer` can only address `call.Caller` by username; it cannot target the
     session that placed the call.
  4. **`handlePhoneEvent` ignores `event.CallID`.** The `EventAnswer` branch never
     checks it, so session 1 — sitting in an unrelated active call — misreads the
     answer for session 2's call as a *conference join* and appends the answerer
     to its own call's viewports.
  5. Session 2 therefore never leaves `CallPending`.
  6. `app.go:384`'s "any key cancels the outbound ring"
     (`if m.state == CallPending { return m.doHangup() }`) fires on the first
     keystroke and hangs up the real, answered call.
- **Live evidence (both binaries):** the harness reports `session1 shows 'carol
  joined': True` — session 1 visibly steals the event — then typing in session 2
  drops it to the idle prompt and carol sees the call end.
- **Not limited to `EventAnswer`.** `EventHangup`'s CallActive branch,
  `EventReject`, `EventRing` and `EventRinging` all ignore `CallID` too. With two
  sessions, *any* event type can land on the wrong session; this is the loudest
  symptom, not the only one.
- **The obvious cheap fix is a trap.** Filtering on `CallID` in
  `handlePhoneEvent` ("ignore events for calls I'm not in") looks like two lines,
  but `notify` is a **single-consumer queue, not a broadcast**: the wrong session
  still *consumes* the event and would then discard it, so the right session still
  never sees it. That turns a misdelivery into a silent drop — strictly harder to
  debug. A real fix needs per-session delivery (session identity in the registry,
  `sendEvent` targeting a session), with `CallID` filtering only as defence in
  depth.
- **Forces the finding 10 design question**, which is why they should be worked
  together: with per-session channels, if alice has two sessions and bob dials
  her, **which session rings?** Both — and then two sessions could each ANSWER the
  same call, appending a duplicate participant? Or one, and by what rule? The
  current "the ring reaches one session" behavior is a consequence of the shared
  channel, not a decision anyone made.

### 12. Pending-ring cancellation named the wrong person

**Status:** ✅ Fixed in `7187581` — regression introduced by finding 3's fix in
`3ecd86b`, caught in live testing.

**Severity: definite bug (low impact, high visibility) · Confidence: high**

- **Where:** `internal/phone/call.go:423` (as shipped in `3ecd86b`).
- **What:** Finding 3's teardown cleanup sent the cancellation with
  `Caller: username` — `hangupLocked`'s **departing participant** argument, not
  whoever started the ring. `addKey{callID, callee}` records who was rung and from
  which call but never who did the ringing: `Add` receives `callerUsername` and
  discarded it, so teardown had no inviter in scope and used the only username it
  had.
- **Failure scenario:** alice and bob are in a call; alice rings carol into it;
  alice drops, then bob drops. Alice's departure leaves bob behind, so the call is
  not empty and no teardown runs. Bob's departure empties it and triggers the
  cleanup with `username = "bob"` — so carol, who was told "alice is phoning you",
  is then told **"bob cancelled the call"** about a ring bob had nothing to do
  with. It always named whoever left **last**; reversing the leave order made it
  accidentally correct.
- **Scope of the regression is attribution only.** Pre-fix (`e3ba975`) carol got
  *no* cancellation at all and was still being rung after both dropped — finding
  3's eternal phantom ring. The notification is new and correct to send; only the
  name was wrong.
- **Fix:** `pendingAdds` values became a `pendingRing{stop, inviter}` struct, so
  teardown attributes the cancellation to `ring.inviter`. `CancelAdd` already took
  `callerUsername` and got this right, so the data existed at `Add` time and was
  simply not retained. Covered by
  `TestHangup_PendingRingCancellationNamesInviterNotLastToLeave`, which pins the
  discriminating leave order (inviter leaves first, bystander last) — with the
  inviter leaving last the old and new code agree, so the test would prove
  nothing. Live-confirmed: carol now sees "alice cancelled the call".

---

## Found during finding 11 verification (2026-07-19)

### 13. `EventHangup` in `CallPending` never returns the session to idle, unlike `EventReject`

**Status:** 🔵 Open — **recorded, not fixed, and reachability not established.**
Found by reading, not by reproducing. Deliberately written down rather than
carried in a conversation: it was noticed while chasing a different symptom, and
that is exactly the kind of observation that evaporates when attention moves on.

**Severity: suspected bug (unconfirmed) · Confidence: medium on the asymmetry
being real, low on it being reachable**

- **Where:** `internal/phone/app.go:328-331` (the `EventHangup` fall-through)
  against `app.go:346-349` (`EventReject`).
- **What:** both branches handle the same situation — our pending *outbound* call
  ended before anyone answered. `EventReject` sets a notification **and calls
  `m.goIdle()`**. `EventHangup` sets its notification and clears
  `pendingIncomingCallID` / `pendingIncomingCaller` — which are the *incoming*-ring
  fields and are not what an outbound `CallPending` session is holding — but never
  calls `goIdle()`. The session keeps `m.state == CallPending`.
- **Consequence if reachable:** the session displays
  `*** X cancelled the call ***` while still believing it is dialling. The next
  keystroke reaches `app.go:421` (`if m.state == CallPending { return m.doHangup() }`),
  which **consumes the character** and hangs up a call that no longer exists. The
  visible signature is a flashed cancellation followed by a silently swallowed
  first keypress.
- **How it was found, and what it is *not*.** During S6 setup on 2026-07-19 a
  one-shot anomaly was seen on a second session of one account: first character
  dropped, plus a flash of a message naming the dial target. This asymmetry was
  found while looking for a cause. It is **probably not** that cause — the likelier
  explanation is `app.go:427-429`, where an active call with a pending ADD ring
  deliberately consumes the next keypress and emits `Cancelled ringing <target>.`
  That path is documented, intended, and accounts for all four observed symptoms
  (character consumed, target named, call unaffected, hard to reproduce because it
  needs `pendingAddTarget` set at the instant of the keystroke). The anomaly was
  not reproducible on a clean retry and no diagnosis is claimed here.
- **Not investigated:** whether an `EventHangup` carrying a matching `CallID` can
  actually reach a session sitting in `CallPending`. Finding 11's fix removed one
  route — sibling-ring retraction now sends `EventAnswerElsewhere`, not
  `EventHangup` — leaving `Reject` and `hangupLocked` as the candidates to trace.
  Establishing reachability is the bulk of the work; the fix itself, if warranted,
  is one line.
- **Next step:** decide whether `EventHangup`'s `CallPending` fall-through should
  call `goIdle()` for symmetry with `EventReject`. A test would need to pin the
  discriminating case (a hangup, not a reject, arriving at a session in
  `CallPending`) — written as a reject it passes against both old and new code and
  proves nothing, the same trap as finding 12's leave order.
- **Instrument now available:** the `PHONE_DEBUG_LOG=1` diagnostic logging added
  alongside this entry records every event delivery per session, including the
  silent buffer-full discard in `registry.SendToSession`. If this recurs, the log
  will show which event actually arrived and at which session — the detail that
  was missing when the anomaly was first seen.

### 14. A full notify buffer silently discards the event

**Status:** 🔵 Open — **latent, pre-existing, by design, and recorded rather than
fixed.** Not introduced by the finding-11 work; the per-session split changed who
owns the buffer, not the discard behaviour.

**Severity: latent defect (low likelihood, silent failure) · Confidence: high on
the mechanism, unquantified on the likelihood**

- **Where:** `internal/registry/registry.go`, `SendToSession`'s
  `select { case ss.notify <- event: default: }`.
- **What:** `notify` is buffered at 8. The send is non-blocking *deliberately* — a
  slow or wedged receiver must never block the sender, which holds `c.mu` at every
  call site. When the buffer is full the `default:` arm fires and **the event is
  dropped**: no error, no return value, no record. The design trade is correct
  (blocking here would deadlock the call table); the problem is that the losing
  side of the trade is invisible.
- **Why it matters beyond bookkeeping:** downstream, a discarded event is
  **indistinguishable from one that was never sent**. The session simply never
  changes state. That is precisely finding 11's symptom — a session stuck in
  `CallPending` while everyone else believes the call is live — arrived at by a
  different route. Anyone diagnosing a recurrence would reasonably suspect routing
  and find the routing correct.
- **Likelihood:** unquantified, believed low. Each session's Bubble Tea loop
  consumes promptly via `waitForPhoneEvent`, and 8 slots is generous for a facility
  whose events are ring/answer/hangup/reject. It needs a wedged or descheduled
  receiver plus sustained event pressure — a re-ring every `RingInterval`, or heavy
  conference churn — to reach.
- **Now observable, still not handled.** `PHONE_DEBUG_LOG=1` logs the discard
  explicitly (`DROPPED (notify buffer full)`), which converts a silent failure into
  a visible one *when logging is on*. That is a diagnostic, **not a fix**: with the
  flag off, which is the default and therefore the norm, the drop remains silent.
- **Next step, if it ever fires:** the options are a larger buffer (defers the
  problem), a drop counter surfaced somewhere always-on (cheap, makes it detectable
  without debug logging), or treating a full buffer as a session-health signal and
  tearing the session down rather than letting it drift out of sync. No decision
  taken. Related: finding 13 is the other "a session sits in the wrong state and
  the next keystroke does damage" entry.

---

## Verified clean

- **`Dial`'s busy check is correct for the case it covers** (`call.go:77-85`): it
  scans every active call for the callee with no reference to the dialer's state.
  The lobby-idle-dialer → busy-callee path reported as working is genuinely
  working; finding 1 is not a defect in this check but in a path that bypasses it.
- **No double-close of `pendingAdds` channels.** `Answer`/`Reject`/`CancelAdd` each
  `delete` after `close`; `Add` closes only on a present key and immediately
  overwrites. No path leaves a closed channel in the map.
- **`sendEvent` under `c.mu` is sound.** It takes the registry's separate lock and
  the registry never calls back into `Calls`, so there is no inversion — already
  relied on at `call.go:110` and `call.go:376`.
- **`Answer` never verifies the answerer is the intended callee** (`call.go:131-188`
  ignores `call.Callee`) — but it is not reachable through the UI: both `doAnswer`
  and `answerCommand` gate on a `pendingCallID` set from an `EventRing` addressed to
  that account. Noted as robustness surface, not a live bug.
