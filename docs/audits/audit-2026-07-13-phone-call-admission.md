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
fixed by one centralization; three are independent design questions and are
**deferred by decision**, marked as such below.

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

**Status:** ⬜ Open — fix in progress (reported from live testing).

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

**Status:** ⬜ Open — fix in progress.

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

**Status:** ⬜ Open — fix in progress.

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

**Status:** ⬜ Open — fix in progress.

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

**Status:** ⬜ Open — fix in progress.

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

**Status:** ⬜ Open — fix in progress (reported from live testing).

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

**Status:** ⬜ Open — fix in progress.

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

## Deferred by decision — independent design questions, not instances of the admission-chokepoint bug

*These are **not** fixed in this pass. Each needs its own discussion before a fix
is designed; none is resolved as a side effect of centralizing admission.*

### 8. Cross-cancellation on same-call double-ADD

**Status:** ⏸️ Deferred — needs a policy decision on second-ADD semantics.

**Severity: definite bug (low impact) · Confidence: high**

- **Where:** `internal/phone/call.go:355-360` (`Add` closes and overwrites the
  existing key) vs. `internal/phone/app.go:769-774` (`cancelPendingAdd`).
- **What:** Two participants in one call both `%ADD carol` produce the **same**
  `callID:carol` key. The second `Add` closes the first's `stopRing` and overwrites
  the entry — but the first caller's `pendingAddTarget` is still set, so their next
  keystroke calls `CancelAdd` and kills the **second** caller's ring.
- **Why deferred:** the right behavior is a policy question (reject the second ADD?
  make it a no-op? track per-inviter?), not a missing guard. Note the admission
  predicate does not resolve this — both ADDs target the same call, and by then
  carol is already being rung, so the second is refused as busy *only if* the
  being-rung check treats a same-call re-ADD as busy. That interaction is exactly
  what needs deciding.

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
