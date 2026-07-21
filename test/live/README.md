# Live SSH verification harnesses

Standing infrastructure for driving **real SSH sessions** against a running
retro-vax-bbs server, for behaviour that unit tests structurally cannot reach —
session interleaving, disconnect timing, and anything where "the guard permits X"
and "X works" are different claims.

**This is not part of `go test`.** It contains no `.go` files, needs a live
server and a seeded database, and takes tens of seconds per run (one scenario
deliberately waits out a 10-second ring interval). Run it by hand.

## Why this exists

Two PHONE bugs — audit findings 11 and 9 — were both missed by a green unit
suite. Finding 11 was additionally missed by a green *scripted* live pass,
because the tests only ever asserted that `Dial` returned no error. Driving the
real thing end-to-end is the only check that distinguishes "admitted" from
"works".

## Setup

Requires `python3` with `pexpect`, plus `sshpass` and `ssh`.

```bash
# 1. Build server + adduser somewhere disposable (NOT your working data/ dir)
mkdir -p /tmp/vaxrig/data && cd /tmp/vaxrig
go build -o vaxbbs   <repo>/cmd/server
go build -o adduser  <repo>/cmd/adduser

# 2. Seed the four accounts the harnesses expect
for u in alice bob carol dave; do
  ./adduser -username $u -password "pw-$u" -role user
done

# 3. Run on isolated loopback ports, with the rate limiter opened up
SSH_HOST=127.0.0.1 SSH_PORT=4222 ADMIN_HOST=127.0.0.1 ADMIN_PORT=4223 \
  RATELIMIT_PER_MINUTE=1000 RATELIMIT_BURST=100 \
  ./vaxbbs > server.log 2>&1 &
```

`RATELIMIT_BURST` must be raised: the default burst of 5 will throttle any
scenario opening several sessions in quick succession, and the failure looks like
a hung login rather than a rate limit.

Then:

```bash
python3 test/live/finding9_mid_ring_disconnect.py
```

Config is overridable via `VAXBBS_HOST`, `VAXBBS_PORT`, `VAXBBS_ADMIN_PORT`,
`VAXBBS_PASS_FMT` (see `livelib.py`).

## Run it against a pre-fix binary too

**A green harness on fixed code is consistent with a harness that asserts
nothing.** The only way to know a scenario detects anything is to run it against
a build where the behaviour is broken, and watch the right checks go red.

This is not hypothetical: an earlier version of `finding9_mid_ring_disconnect.py`
scored **14/16 against knowingly-broken code**, and two of its scenarios turned
out to prove nothing at all. Both defects are recorded below, because neither is
visible from reading the harness — they are properties of the system under test.

The current harness scores **16/16 fixed** and **11/16 pre-fix** (with
`ReapUnreachableRings` stubbed to return immediately), the red checks being
`S1.c`, `S2.b`, `S2.c`, `S2.d`, `S4.c`.

## Hard-won behaviours encoded here

Each of these cost real debugging time. Do not relearn them.

1. **BubbleTea runs the PTY in raw mode, so Enter is CR (`\r`).** pexpect's
   `sendline()` sends LF and is silently ignored. Symptom: typed commands pile
   onto one line and never submit. Always use `livelib.send()`.

2. **In `CallPending`, ANY keypress cancels the outbound ring.** A harness that
   types at the caller therefore tears down the stale state a later check is
   about to probe — via a pre-existing code path, not the one under test. This is
   what made the first version of the stale-ring scenario pass against broken
   code. In any stale-ring scenario the caller must stay completely silent.

3. **`RingInterval` is 10 seconds.** The re-ring goroutine resolves its target by
   *username*, so a stale ring reaches a reconnected session with a brand-new
   session ID — but only on its next tick. Waiting 1.5s for a "phantom ring"
   cannot fail and silently proves nothing. Wait longer than the interval.

4. **Every session must be logged in before any dialing starts.** `Calls.Dial`
   refuses a callee who is not yet connected, so a half-open session yields a
   misleading "not connected" instead of the behaviour under test.

5. **`PHONE_DEBUG_LOG=1` perturbs what it measures.** It is useful for tracing
   which events actually went where, but it widens `c.mu` at ~15 in-lock
   `SendToSession` sites, and a widened critical section can serialize a race out
   of existence as easily as expose it. **A clean run with logging on is not
   evidence a race is gone** — confirm headline scenarios with it off.

## Files

| File | Purpose |
|---|---|
| `livelib.py` | session helpers: `ssh`, `pump`, `send`, `clean`, `saw`, `drop`, `check`, `report` |
| `finding9_mid_ring_disconnect.py` | S1–S4 for PHONE finding 9 (callee disconnects mid-ring) |
