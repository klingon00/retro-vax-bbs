# PHONE Call-Admission Audit — Findings Report

*Findings-only audit, 2026-07-13, triggered by two bugs found in live testing
(self-dial; in-call-to-in-call dial with no busy handling) plus a requested
discovery pass over the surrounding DIAL/ADD/ANSWER call-state logic. Same format
and conventions as `audit-2026-07-05.md`: this is a **living record**, not a
frozen snapshot — as findings are resolved each is marked in place with a
`**Status:** ✅ Fixed in <commit>` line rather than deleted, so the file preserves
both the original findings and their disposition. No code was changed by the
audit itself.*

Ten findings, ranked most-severe first. Seven share a single root cause and are
fixed by one centralization; three were logged as independent design questions.

**Disposition (2026-07-14):** findings 1–7 fixed in `3ecd86b`. Finding 8 turned
out to be **resolved as a consequence** of the same centralization rather than
needing its own design pass — see its entry for the reasoning. Findings 9 and 10
remain **deferred by decision**: they are genuine design questions, not instances
of the admission-chokepoint bug, and finding 10 in particular is neither closed
nor worsened by the fix.

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

**Status:** ⏸️ Deferred — **a design question distinct from, and unaffected by, the
admission fix.**

**Severity: plausible risk (low likelihood) · Confidence: high on the gap, low on
real-world reachability**

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
