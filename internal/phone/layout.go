package phone

// Layout describes the screen regions allocated to each participant's
// viewport in a PHONE call. The PHONE screen looks like:
//
//	┌─────────────────────────────────────────────────┐
//	│ VAX-BBS Phone Facility          23-JUN-2026     │  ← headerHeight = 1
//	│ % [status/command line]                          │  ← statusHeight = 1
//	│ [message line]                                   │  ← msgHeight    = 1
//	├─────────────────────────────────────────────────┤
//	│              BOB                                 │  ← viewport header
//	│ [conversation text...]                           │
//	│                                                  │
//	├─────────────────────────────────────────────────┤
//	│              ALICE                               │
//	│ [conversation text...]                           │
//	│                                                  │
//	└─────────────────────────────────────────────────┘
//
// The number of viewports grows with participants (up to MaxViewports).
// Each viewport gets an equal share of remaining screen height after the
// fixed chrome rows are accounted for.

const (
	// Chrome rows that are never part of a viewport.
	headerRows = 1 // "VAX-BBS Phone Facility   DD-MON-YYYY"
	statusRows = 1 // "% [command/status line]"
	msgRows    = 1 // system message line (errors, ring status, etc.)
	chromeRows = headerRows + statusRows + msgRows

	// Each viewport has a 1-line username header and a 1-line dash separator.
	viewportOverheadRows = 2

	// MinViewportTextRows is the minimum text rows per viewport; below
	// this we'd cap participants rather than make viewports unusable.
	MinViewportTextRows = 3

	// MaxViewports caps conference call size at the original PHONE limit.
	MaxViewports = 6
)

// Layout holds the computed geometry for a PHONE session.
type Layout struct {
	TermWidth    int
	TermHeight   int
	Participants int // number of participants including self

	ViewportTextRows int // text rows per viewport (uniform)
}

// Compute derives the layout for a given terminal size and participant
// count. If participantCount exceeds what the terminal can fit at
// MinViewportTextRows, it is capped.
func Compute(termWidth, termHeight, participantCount int) Layout {
	if participantCount < 1 {
		participantCount = 1
	}
	if participantCount > MaxViewports {
		participantCount = MaxViewports
	}

	available := termHeight - chromeRows - 1

	// Try to fit all participants; reduce count if necessary.
	for participantCount > 1 {
		rowsPerViewport := available / participantCount
		textRows := rowsPerViewport - viewportOverheadRows
		if textRows >= MinViewportTextRows {
			break
		}
		participantCount--
	}

	rowsPerViewport := available / participantCount
	textRows := rowsPerViewport - viewportOverheadRows
	if textRows < MinViewportTextRows {
		textRows = MinViewportTextRows
	}

	return Layout{
		TermWidth:        termWidth,
		TermHeight:       termHeight,
		Participants:     participantCount,
		ViewportTextRows: textRows,
	}
}

// ViewportText holds the display state for one participant's viewport —
// the lines of conversation text accumulated so far.
type ViewportText struct {
	Username string
	Lines    []string // display lines, wrapping applied
	Current  string   // the line currently being typed (not yet newline-terminated)
}

// Append adds a rune to a viewport, handling wrapping, backspace, and
// newline. Width is the terminal width for soft-wrap.
func (v *ViewportText) Append(r rune, width int) {
	switch r {
	case '\r', '\n':
		// Commit current line.
		v.Lines = append(v.Lines, v.Current)
		v.Current = ""
	case '\b', 127: // backspace / DEL
		if len(v.Current) > 0 {
			// Remove last UTF-8 rune.
			runes := []rune(v.Current)
			v.Current = string(runes[:len(runes)-1])
		}
	default:
		v.Current += string(r)
		// Soft-wrap at terminal width minus some margin for the username indent.
		if len([]rune(v.Current)) >= width-2 {
			v.Lines = append(v.Lines, v.Current)
			v.Current = ""
		}
	}
}

// DisplayLines returns the lines to show in a viewport of textRows rows,
// plus the row index where the cursor should appear (the active/current
// typing position). Always returns exactly textRows entries, tail-trimmed
// so the most recent content is visible.
func (v *ViewportText) DisplayLines(textRows int) ([]string, int) {
	// Always include Current as the last element so cursor placement is
	// well-defined regardless of whether Current is empty. Without this,
	// the cursor ends up on the last padding line instead of the line where
	// the next character will actually appear.
	all := make([]string, len(v.Lines)+1)
	copy(all, v.Lines)
	all[len(v.Lines)] = v.Current

	if len(all) > textRows {
		all = all[len(all)-textRows:]
	}

	// Cursor is always at the last element before padding (the Current line,
	// possibly trimmed to fit).
	cursorRow := len(all) - 1

	for len(all) < textRows {
		all = append(all, "")
	}

	return all, cursorRow
}
