#!/usr/bin/env bash
#
# Shared blocklist-matching logic for this repo's pre-commit and
# commit-msg hooks (scripts/pre-commit, scripts/commit-msg). Reads text
# on stdin, checks it for any word on a LOCAL, untracked blocklist, and
# exits nonzero if found.
#
# This script is tracked and public. It deliberately contains zero
# information about what word(s) it blocks -- not even a hash, since a
# hash of a short/guessable string is trivially reversible via a
# dictionary attack and would be obscurity, not protection. The actual
# blocklist
# lives entirely outside git's tracked surface, at
# .git/hooks/pre-commit-blocklist (resolved via `git rev-parse
# --git-path`, not a hardcoded path, so this also works correctly from
# linked worktrees, where --git-dir points at a per-worktree directory
# that has no hooks/ subdirectory of its own). Create that file
# yourself, one blocked word per line, `#` comments and blank lines
# allowed -- see README's Contributing section.
set -euo pipefail
export LC_ALL=C

blocklist="$(git rev-parse --path-format=absolute --git-path hooks/pre-commit-blocklist 2>/dev/null || true)"

if [ -z "$blocklist" ] || [ ! -f "$blocklist" ]; then
  echo "check-blocklist: no local blocklist at .git/hooks/pre-commit-blocklist -- this safety check is currently a no-op for this clone. See README's Contributing section to enable it." >&2
  exit 0
fi

# Strip CR (in case the blocklist was edited on something that writes
# CRLF) and comment/blank lines.
clean_blocklist="$(tr -d '\r' < "$blocklist" | grep -v '^[[:space:]]*#' | grep -v '^[[:space:]]*$' || true)"

if [ -z "$clean_blocklist" ]; then
  exit 0
fi

# Extract whole alphabetic tokens (not \b word-boundary regex -- BSD
# grep on macOS doesn't reliably support \b the same way GNU grep
# does) and check each, case-insensitively, as a fixed whole-line match
# against the blocklist.
matches="$(grep -oE '[A-Za-z]+' | grep -iFxf <(printf '%s\n' "$clean_blocklist") | sort -u || true)"

if [ -n "$matches" ]; then
  echo "check-blocklist: BLOCKED -- content contains a word on the local blocklist:" >&2
  echo "$matches" >&2
  exit 1
fi

exit 0
