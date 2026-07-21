#!/usr/bin/env python3
"""Live-SSH verification for PHONE finding 9: callee disconnects mid-ring.

Covers the invariant that a ring is reaped as soon as no session of the callee
can receive it, and that the caller is told WHICH of the two causes applied:

    EventCalleeGone         callee's last session went away
    EventCalleeUnavailable  callee still connected, but no ringable session

Run against a fixed binary AND a pre-fix one — see README.md. Against a binary
where ReapUnreachableRings is a no-op this scores 11/16, with S1.c, S2.b, S2.c,
S2.d and S4.c red.
"""

import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from livelib import ssh, pump, send, clean, saw, drop, check, report  # noqa: E402

# ---------- S1: single-session callee drops mid-ring ----------
print("\n--- S1: callee disconnects mid-ring ---")
alice = ssh("alice")
bob = ssh("bob")
pump(alice, 1)
pump(bob, 1)
send(alice, "DIAL bob")
check("S1.a alice sees the outbound ring", saw(alice, "Ringing bob"))
check("S1.b bob is rung", saw(bob, "alice is phoning you"))
drop(bob)
check("S1.c alice told bob DISCONNECTED", saw(alice, "bob has disconnected", 6))
check("S1.d not mislabelled 'unavailable'", "bob is unavailable" not in clean(alice._acc))
# Back at the PHONE idle prompt, the next keystroke must be a real keystroke and
# not be swallowed by the CallPending "any key cancels" branch — the visible
# signature of a session left in the wrong state (cf. audit finding 13).
before = len(clean(alice._acc))
send(alice, "HELP")
check("S1.e next keystroke is not swallowed",
      saw(alice, "HELP", 4) and len(clean(alice._acc)) > before)
drop(alice)
time.sleep(1)

# ---------- S2: stale-ring consequences, caller stays SILENT ----------
# The caller must type nothing in this scenario. In CallPending any keypress
# cancels the outbound ring, which tears the stale call down through a
# pre-existing path and destroys the exact state being probed. An earlier version
# of this harness typed at the caller here and consequently PASSED against
# knowingly-broken code.
print("\n--- S2: stale ring: resurrection + dialability (caller stays silent) ---")
a2 = ssh("alice")
b1 = ssh("bob")
dave = ssh("dave")
pump(a2, 1)
pump(b1, 1)
pump(dave, 1)
send(a2, "DIAL bob")
check("S2.a bob is rung", saw(b1, "alice is phoning you", 5))
drop(b1)
b2 = ssh("bob")  # bob reconnects with a NEW session id
pump(b2, 1)
# RingInterval is 10s. The re-ring goroutine resolves its target by USERNAME, so
# a stale ring finds the reconnected session — but only on its next tick. A short
# wait here cannot fail and would prove nothing.
print("    (waiting out one full RingInterval to catch a resurrected ring...)")
time.sleep(13)
pump(b2, 2)
check("S2.b reconnected bob gets NO phantom ring for the old call",
      "alice is phoning you" not in clean(b2._acc))
send(dave, "DIAL bob")
check("S2.c dave can dial bob (stale ring not blocking)", saw(dave, "Ringing bob", 5))
check("S2.d no 'already being called'", "already being called" not in clean(dave._acc))
for c in (a2, b2, dave):
    try:
        drop(c)
    except Exception:
        pass
time.sleep(1.5)

# ---------- S3: multi-session, one drops, the other keeps ringing ----------
print("\n--- S3: surviving session keeps ringing ---")
a3 = ssh("alice")
c1 = ssh("bob")
c2 = ssh("bob")
pump(a3, 1)
pump(c1, 1)
pump(c2, 1)
send(a3, "DIAL bob")
check("S3.a both bob sessions rung",
      saw(c1, "alice is phoning you", 5) and saw(c2, "alice is phoning you", 5))
drop(c1)
time.sleep(2.5)
pump(a3, 2)
check("S3.b caller NOT told bob gone while a session survives",
      "bob has disconnected" not in clean(a3._acc) and "bob is unavailable" not in clean(a3._acc))
send(c2, "ANSWER")
check("S3.c surviving session can still answer", saw(a3, "bob", 5) and saw(c2, "alice", 5))
drop(a3)
drop(c2)
time.sleep(1.5)

# ---------- S4: connected but unringable -> UNAVAILABLE, not disconnected ----------
print("\n--- S4: unringable-but-connected wording ---")
a4 = ssh("alice")
carol = ssh("carol")
bx1 = ssh("bob")
bx2 = ssh("bob")
pump(a4, 1)
pump(carol, 1)
pump(bx1, 1)
pump(bx2, 1)
# bob's second session takes a call with carol, so it stops being ringable.
send(bx2, "DIAL carol")
check("S4.a carol rung by bob-session2", saw(carol, "bob is phoning you", 5))
send(carol, "ANSWER")
time.sleep(1.5)
pump(bx2, 1.5)
send(a4, "DIAL bob")
check("S4.b alice's ring reaches bob's idle session", saw(bx1, "alice is phoning you", 5))
drop(bx1)  # bob still CONNECTED via bx2, but now has ZERO ringable sessions
check("S4.c alice told UNAVAILABLE (not disconnected)", saw(a4, "bob is unavailable", 6))
check("S4.d must not claim a still-connected bob disconnected",
      "bob has disconnected" not in clean(a4._acc))
for c in (a4, carol, bx2):
    try:
        drop(c)
    except Exception:
        pass

sys.exit(report())
