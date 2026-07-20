// Package debuglog provides opt-in diagnostic logging for the PHONE call
// path — dial, ring, answer, hangup, and the per-session event routing that
// carries them.
//
// It exists because that path had no logging at all. The server log showed
// connection-level events (auth, connect, disconnect) and the admin-action
// audit lines, and nothing else, so an intermittent routing fault left no
// evidence behind and could only be described from memory of what flashed on
// a terminal. Two PHONE bugs (audit findings 11 and 12) already escaped a
// green test suite and were caught only by driving real sessions by hand;
// this is the instrument for the next one.
//
// Deliberately gated and default-off, for three reasons:
//
//   - Timing. Emit points sit near lock-held regions. With logging off the
//     cost of one is a single boolean test, so an un-instrumented build and a
//     default build behave identically — the binary verified in a manual pass
//     is the binary that ships.
//   - Content. The lines name accounts and record who called whom, which is
//     call metadata and has no business streaming to stdout by default.
//   - Longevity. Instrumentation that is permanent but dormant is still there
//     the next time an intermittent fault appears, including after release.
//     Debug scaffolding stripped before commit is gone exactly when it is
//     needed again.
//
// Set PHONE_DEBUG_LOG=1 to enable. Output goes to the standard logger,
// alongside the existing admin-action audit lines.
package debuglog

import (
	"log"
	"os"
)

// enabled is resolved once at init rather than per call. That is what makes an
// emit point cheap enough to place on a hot path: when logging is off, the
// call costs a boolean test and nothing else.
var enabled = os.Getenv("PHONE_DEBUG_LOG") == "1"

// Enabled reports whether diagnostic logging is on. Check it before building a
// message that is expensive to format, so the formatting is skipped too and
// not merely discarded.
func Enabled() bool { return enabled }

// Logf writes one diagnostic line, prefixed "phone-debug:" so PHONE routing
// detail can be grepped out of an otherwise connection-level log.
//
// Callers holding a mutex should capture what they need under the lock and
// emit after releasing it — see the deferred-emit pattern in
// internal/phone/call.go. A log write is a syscall, and one made inside a
// critical section widens the very window an intermittent race depends on.
// Instrumentation that suppresses the fault it was added to find is worse
// than none.
func Logf(format string, args ...any) {
	if !enabled {
		return
	}
	log.Printf("phone-debug: "+format, args...)
}
