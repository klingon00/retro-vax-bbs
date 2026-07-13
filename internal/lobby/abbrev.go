package lobby

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DCL-style command abbreviation: typing the shortest unambiguous prefix of
// each command word resolves to the full command, e.g. "WH" -> "WHO",
// "LI P" -> "LIST PENDING", "DEL U alice" -> "DELETE USER alice".
//
// Design decisions are recorded in docs/open-questions.md's "VAX/VMS command
// abbreviation — design settled" entry. In brief:
//  1. Per-token prefix matching — each word is matched independently against the
//     valid words at that position, left to right (not the whole line as one
//     prefix).
//  2. Exact match wins over prefix ambiguity — a token equal to a valid word
//     resolves even if it is also a prefix of a longer word (SHOW USER vs USERS).
//  3. Role-scoped candidate list, computed before matching — admin commands are
//     never candidates for a non-admin, so they can't be matched, can't cause an
//     ambiguity, and can't appear in an ambiguity message. Preserves the
//     admin-command anti-enumeration property dispatch() already enforces.
//  4. Aliases (WHO/SHOW USERS, TIME/SHOW TIME) are independent candidates — they
//     fall out of the trie as separate paths, no linking.
//  5. Ambiguity errors list only the role-visible candidates.
//  6. Resolution runs ahead of the existing exact/prefix tables (commands /
//     argCommands); it rewrites the line, which those tables then handle
//     unchanged.
//
// The vocabulary is derived once from the same commands/argCommands tables that
// drive dispatch, so a new command auto-enrolls in abbreviation with no second
// list to hand-sync — the same single-source-of-truth pattern adminCommandKeys
// uses for the role gate.

// abbrevNode is one node in the command-keyword trie. A node can be both a
// terminal command and have children (e.g. SET PLAN is a command and also the
// parent of SET PLAN CLEAR; SHOW is a command and the parent of SHOW TIME/USERS/
// USER).
type abbrevNode struct {
	children    map[string]*abbrevNode // canonical UPPERCASE token -> child
	isCommand   bool                   // a complete command ends at this node
	canonical   string                 // full canonical command (set when isCommand)
	takesArg    bool                   // command consumes a free-form argument (in argCommands)
	userVisible bool                   // subtree holds >=1 command a non-admin may see
}

var (
	abbrevOnce sync.Once
	abbrevRoot *abbrevNode
)

// abbrevTrie lazily builds (once) and returns the command-keyword trie.
//
// Built lazily rather than in an init(): it derives from commands/argCommands,
// which are populated by commands.go's init(). Go runs a package's init()
// functions in source-file-name order, and "abbrev.go" sorts before
// "commands.go" — a separate init() here would see empty tables. sync.Once
// defers the build to first use (first dispatch call), long after all init().
func abbrevTrie() *abbrevNode {
	abbrevOnce.Do(func() {
		root := &abbrevNode{children: map[string]*abbrevNode{}}
		add := func(canonical string, takesArg bool) {
			up := strings.ToUpper(canonical)
			n := root
			for _, tok := range strings.Fields(up) {
				child := n.children[tok]
				if child == nil {
					child = &abbrevNode{children: map[string]*abbrevNode{}}
					n.children[tok] = child
				}
				n = child
			}
			n.isCommand = true
			n.canonical = up
			if takesArg {
				n.takesArg = true
			}
		}
		for k := range commands {
			add(k, false)
		}
		for _, ac := range argCommands {
			add(ac.prefix, true)
		}
		computeUserVisible(root)
		abbrevRoot = root
	})
	return abbrevRoot
}

// computeUserVisible marks, bottom-up, every node whose subtree contains at
// least one command a non-admin may see (canonical not in adminCommandKeys) —
// the same gate dispatch() applies. Used to role-scope candidate lists so a
// non-admin never sees, matches, or is told about an admin command (decision 3).
func computeUserVisible(n *abbrevNode) bool {
	v := n.isCommand && !adminCommandKeys[n.canonical]
	for _, c := range n.children {
		if computeUserVisible(c) {
			v = true
		}
	}
	n.userVisible = v
	return v
}

// resolveAbbrev expands a DCL-style abbreviated command line to canonical form,
// scoped to role ("admin" or anything else). It runs ahead of the exact/prefix
// dispatch tables (decision 6).
//
//   - resolved / already-exact / nothing-matched -> (canonicalOrOriginalLine, "")
//   - ambiguous prefix                            -> ("", ambiguityMessage)
//
// The free-form argument (everything past the keyword portion) is passed through
// in its ORIGINAL case, and argument tokens are never resolved as keywords — so
// "KICK DELETE" keeps DELETE as the target username. When nothing matches, the
// original line is returned unchanged so it falls through to dispatch's
// unknown-command path exactly as before.
func resolveAbbrev(line, role string) (canonical string, ambiguityMsg string) {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return line, ""
	}
	admin := role == "admin"
	node := abbrevTrie()

	resolved := make([]string, 0, len(tokens))
	i := 0
	for i < len(tokens) {
		typed := tokens[i]
		up := strings.ToUpper(typed)

		// Candidate continuations at this node, role-scoped (decision 3).
		cands := make([]string, 0, len(node.children))
		for tok, child := range node.children {
			if admin || child.userVisible {
				cands = append(cands, tok)
			}
		}
		if len(cands) == 0 {
			break // nothing continues the keyword path; the rest is argument/unknown
		}

		// Exact match wins over prefix ambiguity (decision 2).
		matched := ""
		if abbrevContains(cands, up) {
			matched = up
		} else {
			pfx := make([]string, 0, len(cands))
			for _, c := range cands {
				if strings.HasPrefix(c, up) {
					pfx = append(pfx, c)
				}
			}
			switch len(pfx) {
			case 0:
				matched = "" // no keyword match; the rest is argument
			case 1:
				matched = pfx[0]
			default:
				// Ambiguous (decision 5): name only the role-visible candidates,
				// as the paths resolved-so-far plus each ambiguous next token.
				sort.Strings(pfx)
				names := make([]string, len(pfx))
				for j, c := range pfx {
					names[j] = strings.TrimSpace(strings.Join(resolved, " ") + " " + c)
				}
				return "", ambiguityMessage(typed, names)
			}
		}
		if matched == "" {
			break
		}
		resolved = append(resolved, matched)
		node = node.children[matched]
		i++
		if node.takesArg {
			break // remaining tokens are a literal argument; never resolve them
		}
	}

	if len(resolved) == 0 {
		return line, "" // nothing matched; unchanged, dispatch reports unknown
	}
	out := strings.Join(resolved, " ")
	if i < len(tokens) {
		out += " " + strings.Join(tokens[i:], " ")
	}
	return out, ""
}

func abbrevContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func ambiguityMessage(typed string, names []string) string {
	return fmt.Sprintf("Ambiguous command %q — did you mean: %s? Type more of the command name.",
		typed, strings.Join(names, ", "))
}
