"""Shared helpers for the live-SSH verification harnesses in this directory.

Drives real SSH sessions against a running retro-vax-bbs server with pexpect.
See README.md for setup, and for the hard-won behaviours these helpers encode.

Config comes from the environment so the harness is not pinned to one rig:

    VAXBBS_HOST       default 127.0.0.1
    VAXBBS_PORT       default 4222   (public listener)
    VAXBBS_ADMIN_PORT default 4223   (admin listener)
    VAXBBS_PASS_FMT   default "pw-{user}"
"""

import os
import re
import sys
import time

import pexpect

HOST = os.environ.get("VAXBBS_HOST", "127.0.0.1")
PORT = int(os.environ.get("VAXBBS_PORT", "4222"))
ADMIN_PORT = int(os.environ.get("VAXBBS_ADMIN_PORT", "4223"))
PASS_FMT = os.environ.get("VAXBBS_PASS_FMT", "pw-{user}")


def ssh(user, port=None, timeout=20):
    """Open an SSH session and wait until the lobby has greeted the user.

    Every session must be fully logged in BEFORE any dialing starts: Calls.Dial
    refuses a callee who is not yet connected, so a half-open session produces a
    misleading "not connected" rather than the behaviour under test.
    """
    port = PORT if port is None else port
    password = PASS_FMT.format(user=user)
    cmd = (
        "sshpass -p '%s' ssh -p %d -o StrictHostKeyChecking=no "
        "-o UserKnownHostsFile=/dev/null -o LogLevel=ERROR %s@%s"
        % (password, port, user, HOST)
    )
    c = pexpect.spawn(cmd, encoding="utf-8", timeout=timeout, dimensions=(40, 120))
    c._acc = ""
    c._user = user
    c.expect("Welcome, %s" % user, timeout=timeout)
    c._acc += c.before + c.after
    return c


def pump(c, secs=1.5):
    """Drain whatever has arrived into this session's accumulator."""
    end = time.time() + secs
    while time.time() < end:
        try:
            c._acc += c.read_nonblocking(size=65536, timeout=0.3)
        except pexpect.TIMEOUT:
            pass
        except Exception:
            break
    return c._acc


def send(c, s):
    """Send a line to the BBS.

    BubbleTea puts the PTY in raw mode, so Enter is CR ("\\r"). pexpect's
    sendline() sends LF, which is silently ignored — the symptom is typed
    commands piling onto one line and never submitting. Always use this.
    """
    c.send(s + "\r")


def clean(s):
    """Strip ANSI so substring assertions survive BubbleTea's redraws."""
    return re.sub(r"\x1b\[[0-9;?]*[a-zA-Z]", "", s).replace("\x00", "")


def saw(c, needle, secs=4):
    """Poll until needle appears in this session's output, or time out."""
    end = time.time() + secs
    while time.time() < end:
        pump(c, 0.4)
        if needle in clean(c._acc):
            return True
    return False


def drop(c):
    """Abrupt disconnect — kill the SSH client, i.e. a dropped carrier.

    This is deliberately NOT a clean LOGOUT: a dropped connection runs neither
    HANGUP nor EXIT, so it exercises the session-teardown path rather than the
    command path.
    """
    c.close(force=True)


RESULTS = []


def check(name, ok, detail=""):
    RESULTS.append((name, ok, detail))
    print(("PASS  " if ok else "FAIL  ") + name + (("   -- " + detail) if detail and not ok else ""))


def report():
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    print("\n==== %d/%d checks passed ====" % (passed, len(RESULTS)))
    return 0 if passed == len(RESULTS) else 1
