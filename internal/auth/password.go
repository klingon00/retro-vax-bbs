// Package auth implements password hashing for account credentials.
//
// Uses argon2id (the side-channel-resistant hybrid variant — the design
// doc calls for argon2id specifically, not argon2i or argon2d) via
// golang.org/x/crypto/argon2, which only exposes the raw key-derivation
// primitive. This file owns everything around it: salt generation,
// encoding the parameters alongside the hash so they can change later
// without breaking existing stored hashes, and constant-time comparison.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters, per the design doc's starting point and OWASP's current
// argon2id recommendation for this memory/time tradeoff (the "m=65536
// (64 MiB), t=3, p=4" profile from the OWASP Password Storage Cheat
// Sheet). Revisit against real deployment hardware later, per the open
// questions doc — these are encoded into every hash, so changing them
// won't invalidate already-stored passwords; only new hashes use the
// new values.
const (
	argonMemory  uint32 = 65536 // KiB = 64 MiB
	argonTime    uint32 = 3
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	saltLen      int    = 16
)

// HashPassword derives an argon2id hash for the given password and
// returns it encoded as a single self-describing string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64 salt>$<base64 hash>
//
// This is the same shape the reference argon2 CLI and most other
// language implementations use (a PHC-style string), so the parameters
// travel with the hash — a future change to argonMemory/argonTime/
// argonThreads doesn't break verification of passwords hashed under the
// old parameters, since VerifyPassword reads the params back out of the
// stored string rather than assuming the current constants.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
	return encoded, nil
}

// VerifyPassword checks password against an encoded hash produced by
// HashPassword. Returns (true, nil) on match, (false, nil) on a clean
// mismatch, and (false, err) only if the stored hash is malformed —
// callers should treat both false cases as "reject," and reserve the
// err for logging/diagnostics, not for distinguishing user-facing
// behavior (no detail about *why* a check failed should ever reach the
// person attempting to log in, per the design doc's no-enumeration rule).
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// Expected: ["", "argon2id", "v=19", "m=..,t=..,p=..", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unrecognized hash format")
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("parsing hash parameters: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decoding salt: %w", err)
	}
	storedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decoding hash: %w", err)
	}

	computedHash := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(storedHash)))

	// subtle.ConstantTimeCompare, not bytes.Equal — a length/early-exit
	// comparison here would leak timing information about how much of
	// the hash matched. Argon2id's own cost makes this a secondary
	// defense at best, but it's the standard, correct way to compare
	// secrets and costs nothing to do right.
	if subtle.ConstantTimeCompare(computedHash, storedHash) == 1 {
		return true, nil
	}
	return false, nil
}
