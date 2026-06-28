// Package setplan provides the SET PLAN editor as a lobby app.
//
// It implements the internal/app.App interface (via AppAdapter in app.go)
// so the lobby can delegate input and rendering to it via the same
// activeApp mechanism used by PHONE.
//
// Security notes:
//   - ANSI escape sequences are stripped on input (store.SetPlan) and output
//     (store.StripANSI called in FINGER display). Belt and suspenders.
//   - Hard limit: store.MaxPlanLength runes (512). The textarea widget enforces
//     CharLimit; we also re-check in the save path.
//   - Plan text is stored and displayed as plain UTF-8. No eval, no templating.
//
// Rendering notes:
//   - Inline mode (no alt screen) — lobby delegates via activeApp.
//   - Line 1 of View() is a blank sacrifice line to absorb the BubbleTea
//     v1.3.x off-by-one rendering bug documented in open-questions.md.
package setplan

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

var (
	headerStyle  = lipgloss.NewStyle().Bold(true)
	counterStyle = lipgloss.NewStyle().Faint(true)
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	helpStyle    = lipgloss.NewStyle().Faint(true)
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

const (
	editorHeight = 6
	editorMaxW   = 72
)

// Model is the SET PLAN editor state.
type Model struct {
	St        *store.Store
	Username  string
	ta        textarea.Model
	termWidth int

	ValidationErr string
	StatusMsg     string
	IsDone        bool
}

// New creates a SET PLAN Model pre-populated with the user's current plan.
// Pass an empty string if no plan is currently set.
func New(st *store.Store, username, currentPlan string, termWidth int) Model {
	ta := textarea.New()

	w := clamp(termWidth-4, 20, editorMaxW)
	ta.SetWidth(w)
	ta.SetHeight(editorHeight)
	ta.ShowLineNumbers = false
	ta.CharLimit = store.MaxPlanLength
	ta.Placeholder = "Write a short blurb about yourself..."
	ta.Prompt = "  "

	if currentPlan != "" {
		ta.SetValue(currentPlan)
		ta.CursorEnd()
	}

	// Focus must be called on the textarea value before it is stored in
	// the Model. Focus() sets an internal focus=true flag; if called after
	// storage, it mutates a copy and the stored ta remains unfocused.
	// The returned tea.Cmd is only a cursor-blink starter — Init() covers
	// that with textarea.Blink, so we safely discard it here.
	_ = ta.Focus()

	return Model{
		St:        st,
		Username:  username,
		ta:        ta,
		termWidth: termWidth,
	}
}

// Init returns the initial Cmd for the editor (cursor blink).
func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

// Update processes a message and returns the updated Model and any Cmd.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.IsDone {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlS:
			return m.trySave()
		case tea.KeyEsc, tea.KeyCtrlC:
			m.IsDone = true
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.ta.SetWidth(clamp(msg.Width-4, 20, editorMaxW))
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.ValidationErr = ""
	return m, cmd
}

// View renders the editor. Line 1 is a sacrifice blank (see package doc).
func (m Model) View() string {
	if m.IsDone {
		return ""
	}

	runeCount := utf8.RuneCountInString(strings.TrimSpace(m.ta.Value()))
	counter := fmt.Sprintf("%d/%d", runeCount, store.MaxPlanLength)
	w := clamp(m.termWidth-4, 20, editorMaxW)

	var sb strings.Builder

	// Sacrifice blank — absorbs BubbleTea v1.3.x off-by-one.
	sb.WriteString("\n")

	sb.WriteString(headerStyle.Render("SET PLAN — Edit your FINGER blurb"))
	sb.WriteString("\n\n")
	sb.WriteString(m.ta.View())
	sb.WriteString("\n")

	padding := w - len(counter)
	if padding < 0 {
		padding = 0
	}
	sb.WriteString(counterStyle.Render(strings.Repeat(" ", padding) + counter))
	sb.WriteString("\n")

	switch {
	case m.ValidationErr != "":
		sb.WriteString(errorStyle.Render("  " + m.ValidationErr))
	case m.StatusMsg != "":
		sb.WriteString(successStyle.Render("  " + m.StatusMsg))
	default:
		sb.WriteString(" ")
	}
	sb.WriteString("\n\n")

	sb.WriteString(helpStyle.Render("  Ctrl+S  save      Esc / Ctrl+C  cancel"))
	sb.WriteString("\n")

	return sb.String()
}

// Done reports whether the editor has finished (saved or cancelled).
func (m Model) Done() bool {
	return m.IsDone
}

func (m Model) trySave() (Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	runeCount := utf8.RuneCountInString(text)

	if runeCount > store.MaxPlanLength {
		m.ValidationErr = fmt.Sprintf(
			"Plan too long (%d/%d chars). Please shorten it.",
			runeCount, store.MaxPlanLength,
		)
		return m, nil
	}

	var err error
	if text == "" {
		err = m.St.ClearPlan(m.Username)
	} else {
		err = m.St.SetPlan(m.Username, text)
	}

	if err != nil {
		m.ValidationErr = fmt.Sprintf("Save failed: %v", err)
		return m, nil
	}

	if text == "" {
		m.StatusMsg = "%VAX-BBS-S-PLANCLR, Plan cleared."
	} else {
		m.StatusMsg = "%VAX-BBS-S-PLANSAV, Plan saved."
	}
	m.ValidationErr = ""
	m.IsDone = true
	return m, nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
