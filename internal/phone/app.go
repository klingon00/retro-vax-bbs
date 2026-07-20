package phone

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/klingon00/retro-vax-bbs/internal/registry"
)

// switchHook is the character that enters command mode inside PHONE.
const switchHook = '%'

// Tips shown on the % command line (line 2 of the header area).
// These are embedded with the % prompt, matching the original VAX PHONE layout.
const (
	// idleModeTip: shown at the % when idle or in command mode.
	idleModeTip = "type HELP for commands"
	// activeCallTip: shown at the % during an active call.
	activeCallTip = "type '%' to enter commands or CTRL/Z to hangup/exit"
)

// ---- Bubble Tea message types -----------------------------------------------

// charArrivedMsg carries a rune and the sender's username, received from
// another participant's typing. The sender field is used to route the
// character to the correct viewport in conference calls.
type charArrivedMsg struct {
	r      rune
	sender string
}

// clearMsgMsg fires after a delay to clear a temporary notification message.
// gen must match m.msgGeneration at the time it fires; otherwise a newer
// message has replaced the one we intended to clear and we skip it.
type clearMsgMsg struct{ gen int }

// PhoneEventMsg is exported so the lobby can forward phone events to the
// active PHONE app. The lobby is the sole consumer of the session's notify
// channel — one channel per session, not per account — and forwards from there.
type PhoneEventMsg struct {
	Event registry.PhoneEvent
}

// ---- Model ------------------------------------------------------------------

// Model is the Bubble Tea model for the PHONE app. It implements
// internal/app.App so the lobby can launch it generically.
type Model struct {
	username  string
	sessionID string // this session's registry ID; identifies us as a call participant
	callID    string
	isCaller  bool

	state     CallState
	callsWith []string // other participants (not including self)

	viewports []*ViewportText
	myChars   chan CharEvent // incoming CharEvents from other participants

	calls *Calls
	reg   *registry.Registry

	// Pending incoming call (received while in PHONE idle).
	pendingIncomingCallID string
	pendingIncomingCaller string

	// pendingAddTarget is the username being rung via ADD/DIAL from an
	// active call. Any keypress while this is set cancels the ring.
	pendingAddTarget string

	// Command mode (after user presses switchHook).
	inCommandMode bool
	commandBuf    string

	// Status line override (e.g. "Ringing alice..."). Empty = show tip.
	status string
	// Message line — errors, notifications, help text.
	msg string

	width  int
	height int
	done   bool

	// out is the SSH session's output writer for direct terminal writes
	// (bell character) that bypass BubbleTea's cellbuf renderer.
	out io.Writer

	// msgGeneration is incremented each time m.msg is set as a notification.
	// clearMsgMsg carries the generation at the time the clear was scheduled;
	// if a newer message has since arrived the clear is a no-op.
	msgGeneration int

	// bellPending and helpActive handled without persistent fields now:
	// bell is rung via ringBellCmd() direct write; helpActive via a flag below.
	helpActive bool
}

// NewIdle opens PHONE in idle state (no call in progress). Used when the
// user types PHONE from the lobby without specifying a username.
func NewIdle(username, sessionID string, calls *Calls, reg *registry.Registry, out io.Writer, width, height int) Model {
	reg.SetApp(username, "PHONE")
	return Model{
		username:  username,
		sessionID: sessionID,
		state:     CallIdle,
		viewports: []*ViewportText{{Username: username}},
		calls:     calls,
		reg:       reg,
		out:       out,
		width:     width,
		height:    height,
	}
}

// New opens PHONE and immediately starts ringing calleeUsername.
func New(username, sessionID, callID, calleeUsername string, calls *Calls, reg *registry.Registry,
	myChars chan CharEvent, out io.Writer, width, height int) Model {

	reg.SetApp(username, "PHONE")
	return Model{
		username:  username,
		sessionID: sessionID,
		callID:    callID,
		isCaller:  true,
		state:     CallPending,
		callsWith: []string{calleeUsername},
		viewports: []*ViewportText{
			{Username: username},
			{Username: calleeUsername},
		},
		calls:   calls,
		reg:     reg,
		myChars: myChars,
		out:     out,
		status:  fmt.Sprintf("Ringing %s...  (Press any key to cancel call and continue.)", calleeUsername),
		width:   width,
		height:  height,
	}
}

// NewAnswering opens PHONE in the active state for someone who just answered.
// others contains ALL other participants' usernames (not just the original
// caller), ensuring correct viewport setup for conference calls.
func NewAnswering(username, sessionID, callID string, others []string, calls *Calls, reg *registry.Registry,
	myChars chan CharEvent, out io.Writer, width, height int) Model {

	reg.SetApp(username, "PHONE")
	viewports := []*ViewportText{{Username: username}}
	for _, other := range others {
		viewports = append(viewports, &ViewportText{Username: other})
	}
	return Model{
		username:  username,
		sessionID: sessionID,
		callID:    callID,
		isCaller:  false,
		state:     CallActive,
		callsWith: others,
		viewports: viewports,
		calls:     calls,
		reg:       reg,
		myChars:   myChars,
		out:       out,
		width:     width,
		height:    height,
	}
}

func (m Model) Done() bool { return m.done }

// ringBellCmd writes BEL directly to the SSH session output, bypassing
// BubbleTea's cellbuf renderer (which strips \a as a non-printable C0 control).
func (m Model) ringBellCmd() tea.Cmd {
	if m.out == nil {
		return nil
	}
	out := m.out
	return func() tea.Msg {
		out.Write([]byte("\a")) //nolint:errcheck
		return nil
	}
}

// ---- Init / Update / View ---------------------------------------------------

func (m Model) Init() tea.Cmd {
	// waitForChar returns nil when myChars is nil (idle state with no call).
	// It will be started explicitly when a call is established.
	return waitForChar(m.myChars)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case charArrivedMsg:
		// \a (BEL) from a participant: ring our bell via direct write.
		if msg.r == '\a' {
			return m, tea.Batch(waitForChar(m.myChars), m.ringBellCmd())
		}
		// Route char to the sender's viewport. \f clears it; \x15 clears current line.
		for _, vp := range m.viewports {
			if vp.Username == msg.sender {
				switch msg.r {
				case '\f': // Ctrl-L: clear whole viewport
					vp.Lines = nil
					vp.Current = ""
				case '\x15': // Ctrl-U: clear current line only
					vp.Current = ""
				default:
					vp.Append(msg.r, m.width)
				}
				break
			}
		}
		return m, waitForChar(m.myChars)

	case clearMsgMsg:
		// Only clear if this matches the current generation — i.e., no newer
		// notification has overwritten the one we were scheduled to clear.
		if msg.gen == m.msgGeneration {
			m.msg = ""
		}
		return m, nil

	case PhoneEventMsg:
		m, cmd := m.handlePhoneEvent(msg.Event)
		return m, cmd

	case tea.KeyMsg:
		m, cmd := m.handleKey(msg)
		return m, cmd
	}
	return m, nil
}

// ---- Phone event handling ---------------------------------------------------

func (m Model) handlePhoneEvent(event registry.PhoneEvent) (Model, tea.Cmd) {
	const notifyDur = 10 * time.Second

	switch event.Type {

	case registry.EventAnswer:
		// Defence-in-depth: per-session routing should only deliver an answer
		// for THIS session's call, but ignore any that isn't — a stray answer
		// must never be misread as a conference join (the finding 11 failure).
		if event.CallID != m.callID {
			return m, nil
		}
		if m.state == CallPending {
			m.state = CallActive
			m.status = ""
		} else if m.state == CallActive {
			newUser := event.Callee
			alreadyPresent := false
			for _, vp := range m.viewports {
				if vp.Username == newUser {
					alreadyPresent = true
					break
				}
			}
			if !alreadyPresent {
				m.callsWith = append(m.callsWith, newUser)
				m.viewports = append(m.viewports, &ViewportText{Username: newUser})
			}
			// Clear the pending ADD target if this is who we were ringing.
			if m.pendingAddTarget == event.Callee {
				m.pendingAddTarget = ""
			}
		}
		return m.setNotification(fmt.Sprintf("*** %s joined the call ***", event.Callee), notifyDur)

	case registry.EventHangup:
		if m.state == CallIdle {
			// Check if this cancels a pending incoming ring we were waiting
			// to answer (e.g. caller cancelled the ADD ring before we answered).
			// Guarded on the ring's own CallID (pendingIncomingCallID), which is
			// how the answer-elsewhere ring retract also clears a losing session.
			if event.CallID == m.pendingIncomingCallID {
				m.pendingIncomingCallID = ""
				m.pendingIncomingCaller = ""
				return m.setNotification(fmt.Sprintf("*** %s cancelled the call ***", event.Caller), notifyDur)
			}
			return m, nil
		}
		// In a call (pending or active): act only on hangups for OUR call.
		if event.CallID != m.callID {
			return m, nil
		}
		if m.state == CallActive {
			// Callee non-empty: a pending ring for event.Callee was cancelled
			// by event.Caller (via CancelAdd). Not a participant departure —
			// just clear the EventRinging notification we were showing.
			if event.Callee != "" {
				m.msg = "" // clear the "*** alice is ringing bob ***" notification
				return m, nil
			}
			// Callee empty: event.Caller has left the call.
			notification := fmt.Sprintf("*** %s has left the call ***", event.Caller)
			remaining := m.viewports[:0]
			for _, vp := range m.viewports {
				if vp.Username != event.Caller {
					remaining = append(remaining, vp)
				}
			}
			m.viewports = remaining
			cw := m.callsWith[:0]
			for _, u := range m.callsWith {
				if u != event.Caller {
					cw = append(cw, u)
				}
			}
			m.callsWith = cw
			if len(m.callsWith) == 0 {
				m.calls.Hangup(m.callID, m.sessionID)
				m = m.goIdle()
			}
			return m.setNotification(notification, notifyDur)
		}
		m.pendingIncomingCallID = ""
		m.pendingIncomingCaller = ""
		return m.setNotification(fmt.Sprintf("*** %s cancelled the call ***", event.Caller), notifyDur)

	case registry.EventReject:
		// Only act on a reject for THIS session's call (defence-in-depth).
		if event.CallID != m.callID {
			return m, nil
		}
		if m.state == CallActive {
			// Clear pending ADD target if this person declined.
			if m.pendingAddTarget == event.Callee {
				m.pendingAddTarget = ""
			}
			// Conference ADD declined. Stay in the active call.
			return m.setNotification(fmt.Sprintf("*** %s declined to join the call ***", event.Callee), notifyDur)
		}
		// Pending outbound call rejected. Return to idle.
		msg := fmt.Sprintf("*** %s rejected the call ***", event.Callee)
		m = m.goIdle()
		return m.setNotification(msg, notifyDur)

	case registry.EventAnswerElsewhere:
		// We were being rung for this call inside PHONE (idle at the % prompt),
		// and another session of this account answered it. Clear our pending
		// ring — but only if it's the one we're showing.
		if event.CallID == m.pendingIncomingCallID {
			m.pendingIncomingCallID = ""
			m.pendingIncomingCaller = ""
			return m.setNotification("*** Call answered on another session ***", notifyDur)
		}
		return m, nil

	case registry.EventRinging:
		// Advisory for OUR call only (defence-in-depth); a stray one must not
		// paint a bogus "X is ringing Y" line on an unrelated call.
		if event.CallID != m.callID {
			return m, nil
		}
		// Another participant in the call is ringing someone. Show as a
		// temporary notification — auto-clears and re-fires each ring interval.
		return m.setNotification(
			fmt.Sprintf("*** %s is ringing %s ***", event.Caller, event.Callee),
			notifyDur)

	case registry.EventRing:
		t := time.Now()
		timeStr := fmt.Sprintf("%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
		if m.state == CallIdle {
			// Don't auto-clear ring notifications — user needs to read them.
			m.pendingIncomingCallID = event.CallID
			m.pendingIncomingCaller = event.Caller
			m.msg = fmt.Sprintf("%s is phoning you (%s) — ANSWER to accept, REJECT to decline",
				event.Caller, timeStr)
			return m, m.ringBellCmd()
		}
		// Ring while in an active call: show on info line, bell rings.
		m, cmd := m.setNotification(
			fmt.Sprintf("*** %s is calling (%s) — %%ADD to conference ***", event.Caller, timeStr),
			notifyDur)
		return m, tea.Batch(cmd, m.ringBellCmd())
	}
	return m, nil
}

// ---- Key handling -----------------------------------------------------------

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	// Ctrl+Z / Ctrl+C: HANGUP if in a call, EXIT if idle.
	if msg.Type == tea.KeyCtrlZ || msg.Type == tea.KeyCtrlC {
		if m.state == CallIdle {
			return m.doExit()
		}
		return m.doHangup()
	}

	// HELP mode: any keypress clears help text from the viewport and returns
	// to normal operation. The keypress itself is consumed.
	if m.helpActive {
		m.helpActive = false
		m.msg = ""
		for _, vp := range m.viewports {
			if vp.Username == m.username {
				vp.Lines = nil
				vp.Current = ""
				break
			}
		}
		return m, nil
	}

	// While pending (ringing), any key cancels the outbound call.
	if m.state == CallPending {
		return m.doHangup()
	}

	// While in an active call with a pending ADD ring, any key cancels it.
	// The "(press any key to cancel)" hint in m.msg tells the user.
	// After cancelling the keypress is consumed — not sent to conversation.
	if m.state == CallActive && m.pendingAddTarget != "" {
		return m.cancelPendingAdd()
	}

	// In idle state the switch-hook is optional (original VAX PHONE behavior:
	// "the switch-hook character is optional because there is no ambiguity
	// between a command and conversation"). All keypresses go directly to the
	// command buffer — no % required.
	if m.state == CallIdle {
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == switchHook {
			// % pressed: enter command mode without adding % to the buffer.
			m.inCommandMode = true
			m.commandBuf = ""
			return m, nil
		}
		// Any other key: auto-enter command mode and handle it immediately.
		if !m.inCommandMode {
			m.inCommandMode = true
			m.commandBuf = ""
		}
		return m.handleCommandKey(msg)
	}

	// Active call: switch hook enters command mode.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 &&
		msg.Runes[0] == switchHook && !m.inCommandMode {
		m.inCommandMode = true
		m.commandBuf = ""
		return m, nil
	}

	if m.inCommandMode {
		return m.handleCommandKey(msg)
	}

	return m.handleConvKey(msg)
}

func (m Model) handleCommandKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Pass original case to runCommand — it uppercases internally for
		// verb matching but preserves case for username arguments.
		raw := strings.TrimSpace(m.commandBuf)
		m.inCommandMode = false
		m.commandBuf = ""
		return m.runCommand(raw)

	case tea.KeyBackspace:
		if len(m.commandBuf) > 0 {
			m.commandBuf = m.commandBuf[:len(m.commandBuf)-1]
		} else {
			// Deleted back past the start — exit command mode.
			m.inCommandMode = false
		}

	case tea.KeyEscape:
		m.inCommandMode = false
		m.commandBuf = ""

	case tea.KeyRunes:
		m.commandBuf += string(msg.Runes)

	case tea.KeySpace:
		m.commandBuf += " "
	}
	return m, nil
}

func (m Model) handleConvKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	var r rune
	switch msg.Type {
	case tea.KeyCtrlG:
		// Broadcast BEL to all participants — rings their terminal bell.
		// Also ring the sender's own bell; BroadcastChar skips the sender
		// by design, so we add it explicitly here.
		m.calls.BroadcastChar(m.callID, m.username, '\a')
		return m, m.ringBellCmd()

	case tea.KeyTab:
		// Insert a tab stop (~5 spaces) into the conversation.
		for _, vp := range m.viewports {
			if vp.Username == m.username {
				for i := 0; i < 5; i++ {
					vp.Append(' ', m.width)
				}
				break
			}
		}
		for i := 0; i < 5; i++ {
			m.calls.BroadcastChar(m.callID, m.username, ' ')
		}
		return m, nil

	case tea.KeyCtrlL:
		// Clear own viewport and broadcast the clear to all participants
		// so their view of this viewport also resets.
		for _, vp := range m.viewports {
			if vp.Username == m.username {
				vp.Lines = nil
				vp.Current = ""
				break
			}
		}
		m.calls.BroadcastChar(m.callID, m.username, '\f')
		return m, nil

	case tea.KeyCtrlU:
		// Clear the current line — broadcast \x15 so all participants'
		// view of this user's viewport also clears the in-progress line.
		for _, vp := range m.viewports {
			if vp.Username == m.username {
				vp.Current = ""
				break
			}
		}
		m.calls.BroadcastChar(m.callID, m.username, '\x15')
		return m, nil

	case tea.KeyEnter:
		r = '\r'
	case tea.KeyBackspace:
		r = '\b'
	case tea.KeySpace:
		r = ' '
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			r = msg.Runes[0]
		}
	default:
		return m, nil
	}
	// Append to own viewport.
	for _, vp := range m.viewports {
		if vp.Username == m.username {
			vp.Append(r, m.width)
			break
		}
	}
	m.calls.BroadcastChar(m.callID, m.username, r)
	return m, nil
}

// ---- Commands ---------------------------------------------------------------

func (m Model) runCommand(cmd string) (Model, tea.Cmd) {
	// upper is used only for verb matching. Argument extraction always
	// uses cmd (original case) so usernames aren't mangled.
	upper := strings.ToUpper(cmd)

	// arg is everything after the first word, original case preserved.
	arg := ""
	if idx := strings.Index(cmd, " "); idx >= 0 {
		arg = strings.TrimSpace(cmd[idx+1:])
	}

	switch {
	case upper == "HANGUP" || upper == "H":
		if m.state == CallIdle {
			m.msg = "%PHONE-W-NOCALL, not currently in a call."
			return m, nil
		}
		return m.doHangup()

	case upper == "EXIT" || upper == "EX":
		return m.doExit()

	case upper == "HELP":
		helpLines := []rune{
			// Written as a slice of rune-encoded lines via Append below.
		}
		_ = helpLines
		// Write help text directly into own viewport so it fills the
		// conversation area. helpActive suppresses normal key routing until
		// any key is pressed (which clears the viewport and restores flow).
		helpText := []string{
			"",
			"  PHONE commands:",
			"  ───────────────────────────────────────────────",
			"  DIAL <user>    Start a call, or add to active conference",
			"  ANSWER         Accept an incoming call",
			"  REJECT         Decline an incoming call",
			"  HANGUP  (H)    End call and return to idle",
			"  ADD <user>     Add a user to the active conference",
			"  EXIT           Leave PHONE and return to the lobby",
			"  HELP           Show this text",
			"",
			"  Keyboard shortcuts (in conversation):",
			"  ───────────────────────────────────────────────",
			"  %              Enter command mode (switch-hook)",
			"  Ctrl-Z         Hang up / exit",
			"  Ctrl-G         Ring bell for all participants",
			"  Tab            Insert 5 spaces",
			"  Ctrl-L         Clear your viewport",
			"  Ctrl-U         Clear current line being typed",
			"",
		}
		for _, vp := range m.viewports {
			if vp.Username == m.username {
				vp.Lines = nil
				vp.Current = ""
				for _, line := range helpText {
					for _, ch := range line {
						vp.Append(ch, m.width)
					}
					vp.Append('\r', m.width) // commit each line
				}
				break
			}
		}
		m.helpActive = true
		m.msg = "  Press any key to continue."
		return m, nil

	case upper == "ANSWER":
		return m.doAnswer()

	case upper == "REJECT":
		return m.doReject()

	case strings.HasPrefix(upper, "DIAL ") || strings.HasPrefix(upper, "PHONE "):
		return m.doDial(arg)

	case upper == "DIAL" || upper == "PHONE":
		m.msg = "Usage: DIAL <username>"
		return m, nil

	case strings.HasPrefix(upper, "ADD "):
		if arg == "" {
			m.msg = "Usage: ADD <username>"
			return m, nil
		}
		if m.state == CallIdle {
			// ADD when not in a call is equivalent to DIAL.
			return m.doDial(arg)
		}
		return m.doAddToCall(arg)

	case upper == "ADD":
		m.msg = "Usage: ADD <username>"
		return m, nil

	default:
		m.msg = fmt.Sprintf("%%PHONE-W-UNDEF, unknown command: %s  (HELP for list)", cmd)
		return m, nil
	}
}

func (m Model) doDial(target string) (Model, tea.Cmd) {
	if target == "" {
		m.msg = "Usage: DIAL <username>"
		return m, nil
	}
	if m.state == CallActive {
		// DIAL when already in a call adds the user to the conference,
		// same as ADD — symmetric with ADD routing to DIAL when idle.
		return m.doAddToCall(target)
	}
	if m.state != CallIdle {
		m.msg = "Use HANGUP first, then DIAL."
		return m, nil
	}
	call, callerP, err := m.calls.Dial(m.sessionID, m.username, target)
	if err != nil {
		m.msg = ErrorMessage(err, target)
		return m, nil
	}
	m.callID = call.ID
	m.state = CallPending
	m.callsWith = []string{target}
	m.viewports = append(m.viewports, &ViewportText{Username: target})
	m.myChars = callerP.IncomingChar
	m.status = fmt.Sprintf("Ringing %s...  (Press any key to cancel call and continue.)", target)
	m.msg = ""
	// Ring the caller's own bell so they hear confirmation that the call
	// is being placed — the callee hears a bell via EventRing, but the
	// caller's side was previously silent.
	return m, tea.Batch(waitForChar(m.myChars), m.ringBellCmd())
}

// doAddToCall invites a user into the current active call (conference).
// Called directly by %ADD and by %DIAL when already in an active call.
func (m Model) doAddToCall(target string) (Model, tea.Cmd) {
	if target == "" {
		m.msg = "Usage: ADD <username>"
		return m, nil
	}
	if m.state != CallActive {
		m.msg = "Must be in an active call to add someone."
		return m, nil
	}
	if err := m.calls.Add(m.callID, m.username, target); err != nil {
		m.msg = ErrorMessage(err, target)
		return m, nil
	}
	// Track the pending ring so any keypress can cancel it.
	// Don't auto-clear: the message should persist until the ring resolves.
	m.pendingAddTarget = target
	m.msg = fmt.Sprintf("Ringing %s...  (press any key to cancel)", target)
	return m, nil
}

func (m Model) doAnswer() (Model, tea.Cmd) {
	if m.pendingIncomingCallID == "" {
		m.msg = "%PHONE-W-NOCALL, no incoming call to answer."
		return m, nil
	}
	call, calleeP, err := m.calls.Answer(m.pendingIncomingCallID, m.sessionID, m.username)
	if err != nil {
		// ErrAlreadyAnswered means another session of this account won the race
		// to answer this call (first-answer-wins). Show a specific note instead
		// of the generic error and drop back to idle.
		if errors.Is(err, ErrAlreadyAnswered) {
			m.pendingIncomingCallID = ""
			m.pendingIncomingCaller = ""
			return m.setNotification("%PHONE-I-ANSWERED, call was already answered elsewhere.", 5*time.Second)
		}
		m.msg = fmt.Sprintf("%%PHONE-E-ANSWER, %v", err)
		m.pendingIncomingCallID = ""
		return m, nil
	}
	m.callID = call.ID
	m.state = CallActive
	m.myChars = calleeP.IncomingChar
	m.pendingIncomingCallID = ""
	m.pendingIncomingCaller = ""
	m.msg = ""
	m.status = ""

	// Build viewports and callsWith from all call participants so
	// conference calls show all parties, not just the original caller.
	allParticipants := m.calls.Participants(call.ID)
	m.callsWith = make([]string, 0, len(allParticipants)-1)
	m.viewports = []*ViewportText{{Username: m.username}}
	for _, p := range allParticipants {
		if p != m.username {
			m.callsWith = append(m.callsWith, p)
			m.viewports = append(m.viewports, &ViewportText{Username: p})
		}
	}
	return m, waitForChar(m.myChars)
}

func (m Model) doReject() (Model, tea.Cmd) {
	if m.pendingIncomingCallID == "" {
		return m.setNotification("%PHONE-W-NOCALL, no incoming call to reject.", 5*time.Second)
	}
	callID := m.pendingIncomingCallID
	m.pendingIncomingCallID = ""
	m.pendingIncomingCaller = ""
	if err := m.calls.Reject(callID, m.username); err != nil {
		return m.setNotification(fmt.Sprintf("%%PHONE-E-REJECT, %v", err), 5*time.Second)
	}
	return m.setNotification("Call rejected.", 5*time.Second)
}

// ---- Lifecycle helpers ------------------------------------------------------

// goIdle transitions the model to CallIdle without exiting PHONE.
func (m Model) goIdle() Model {
	m.state = CallIdle
	m.callID = ""
	m.callsWith = nil
	m.myChars = nil
	m.viewports = []*ViewportText{{Username: m.username}}
	m.status = ""
	m.msg = "" // clear any stale call messages
	m.inCommandMode = false
	m.commandBuf = ""
	m.pendingIncomingCallID = ""
	m.pendingIncomingCaller = ""
	m.reg.SetApp(m.username, "PHONE")
	return m
}

// doHangup ends the current call and returns to the PHONE idle state.
// Does NOT exit PHONE — use doExit for that.
func (m Model) doHangup() (Model, tea.Cmd) {
	if m.state != CallIdle {
		m.calls.Hangup(m.callID, m.sessionID)
	}
	return m.goIdle(), nil
}

// cancelPendingAdd cancels an in-progress conference ring initiated by
// this user. Any keypress while pendingAddTarget is set triggers this.
func (m Model) cancelPendingAdd() (Model, tea.Cmd) {
	target := m.pendingAddTarget
	m.pendingAddTarget = ""
	m.calls.CancelAdd(m.callID, target, m.username)
	return m.setNotification(fmt.Sprintf("Cancelled ringing %s.", target), 5*time.Second)
}

// doExit ends any current call and exits the PHONE app, returning to
// the lobby.
func (m Model) doExit() (Model, tea.Cmd) {
	if m.state != CallIdle {
		m.calls.Hangup(m.callID, m.sessionID)
	}
	m.reg.SetApp(m.username, "LOBBY")
	m.done = true
	return m, tea.Quit
}

// ---- View -------------------------------------------------------------------

func (m Model) View() string {
	layout := Compute(m.width, m.height, len(m.viewports))

	var b strings.Builder

	// SACRIFICE LINE: BubbleTea v1.3.x + wish SSH renders line 1 of View()
	// at row 0 — one row above the terminal's visible top. Everything shifts
	// up by 1. We burn line 1 as a blank so the header lands at screen row 1.
	// layout.go compensates with available = termHeight-chromeRows-1 so the
	// total line count stays at termHeight+1 across all participant counts.
	b.WriteString("\n")

	// Header (line 2 of View → screen row 1): only the title is in inverse
	// video — spaces and date are normal text, matching the original VAX
	// PHONE look. This works on line 2 (not line 1); plain spaces before
	// an ANSI code are fine for any line after the sacrifice blank.
	title := "VAX-BBS Phone Facility"
	now := strings.ToUpper(time.Now().Format("02-Jan-2006"))
	w := m.width
	if w < len(title)+len(now)+2 {
		w = len(title) + len(now) + 2
	}
	gaps := w - len(title) - len(now)
	leftSp := gaps / 2
	if leftSp < 0 {
		leftSp = 0
	}
	rightSp := gaps - leftSp
	if rightSp < 1 {
		rightSp = 1
	}
	titleStyled := lipgloss.NewStyle().Reverse(true).Render(title)
	b.WriteString(strings.Repeat(" ", leftSp) + titleStyled +
		strings.Repeat(" ", rightSp) + now + "\n")

	// Command line (screen row 2): always just %, plus typed input in command
	// mode. Status/tip moved to the info line so the command line stays clean.
	if m.inCommandMode {
		b.WriteString(string(switchHook) + " " + m.commandBuf + "█\n")
	} else {
		b.WriteString(string(switchHook) + "\n")
	}

	// Info line (screen row 3): shows, in priority order:
	//   1. Active notification (m.msg) — auto-clears via setNotification
	//   2. Status override (m.status) — e.g. "Ringing alice..."
	//   3. Base tip for current state — restores when notification clears
	infoLine := m.msg
	if infoLine == "" {
		if m.status != "" {
			infoLine = m.status
		} else if m.state == CallIdle || m.inCommandMode {
			infoLine = idleModeTip
		} else if m.state == CallActive {
			infoLine = activeCallTip
		}
	}
	b.WriteString(infoLine + "\n")

	// Viewports with separators.
	rendered := 0
	for i, vp := range m.viewports {
		if i >= layout.Participants {
			break
		}
		rendered++
		b.WriteString(strings.Repeat("-", m.width) + "\n")
		label := strings.ToUpper(vp.Username)
		labelPad := (m.width - len(label)) / 2
		if labelPad < 0 {
			labelPad = 0
		}
		b.WriteString(strings.Repeat(" ", labelPad) + label + "\n")

		isSelf := vp.Username == m.username
		showCursor := isSelf && m.state == CallActive && !m.inCommandMode
		lines, cursorRow := vp.DisplayLines(layout.ViewportTextRows)
		for j, line := range lines {
			if showCursor && j == cursorRow {
				b.WriteString(line + "█\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	// Filler: floor division in Compute can leave total = termHeight (not
	// termHeight+1) when available%N != 0. Without the extra line, the
	// sacrifice blank has nothing to absorb and becomes visible at row 1.
	// Add the exact number of blank lines needed to reach termHeight+1.
	// expected = sacrifice(1) + chrome(3) + rendered*(sep+label+textRows) + bottom(1)
	expected := 5 + rendered*(2+layout.ViewportTextRows)
	for expected < m.height+1 {
		b.WriteString("\n")
		expected++
	}

	b.WriteString(strings.Repeat("-", m.width))
	return b.String()
}

var headerStyle = lipgloss.NewStyle().Bold(true)

// ---- Async cmds -------------------------------------------------------------

func waitForChar(ch <-chan CharEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return charArrivedMsg{r: evt.R, sender: evt.Sender}
	}
}

// setNotification sets a temporary notification message and schedules
// a clearMsgMsg to fire after d, so the message auto-clears unless a
// newer one has arrived in the meantime.
func (m Model) setNotification(text string, d time.Duration) (Model, tea.Cmd) {
	m.msg = text
	m.msgGeneration++
	gen := m.msgGeneration
	return m, func() tea.Msg {
		time.Sleep(d)
		return clearMsgMsg{gen: gen}
	}
}
