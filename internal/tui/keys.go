package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"bbapprove/internal/style"
)

// isModifierKey reports a press of a bare modifier or lock key (only reported by the kitty protocol).
func isModifierKey(c rune) bool {
	return c >= tea.KeyLeftShift && c <= tea.KeyIsoLevel5Shift ||
		c == tea.KeyCapsLock || c == tea.KeyScrollLock || c == tea.KeyNumLock
}

// keyName is the key's string with a lone Shift+letter normalised to the uppercase letter, so a
// terminal that reports "A" as shift+a still triggers the same action as one that sends "A".
func keyName(k tea.KeyPressMsg) string {
	if k.Text == "" && k.Mod == tea.ModShift && k.Code >= 'a' && k.Code <= 'z' {
		return strings.ToUpper(string(k.Code))
	}
	return k.String()
}

// shiftHints reports whether the hint line shows the batch set: Shift is held and the terminal
// reports modifier keys (kitty keyboard protocol). Anywhere else the instant set is always shown.
func (m *Model) shiftHints() bool {
	return m.kitty && !m.cfg.NoKitty && (m.shiftL || m.shiftR)
}

func (m *Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyLeftShift:
		m.shiftL = true
		return m, nil
	case tea.KeyRightShift:
		m.shiftR = true
		return m, nil
	}
	if !k.Mod.Contains(tea.ModShift) {
		// Safety net for a lost release: a key press without Shift means Shift is not held.
		m.shiftL, m.shiftR = false, false
	}
	if isModifierKey(k.Code) {
		return m, nil // a bare modifier never cancels a prompt or clears the message
	}
	s := keyName(k)
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		return m.inputKey(k)
	case modeConfirm:
		m.mode = modeList
		if s == "y" || s == "Y" {
			return m, m.runPending()
		}
		m.pending = nil
		m.msg = "cancelled"
		return m, nil
	}
	m.msg = ""
	var cmd tea.Cmd
	n := len(m.visible())
	switch s {
	case "q":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "x", "space":
		if i, ok := m.current(); ok {
			m.toggleMark(i)
		}
	case "ctrl+a":
		for i := range m.rows {
			// eligible() is false for IGNORED rows, so hidden/ignored rows are never marked.
			if m.eligible(i) {
				m.marks[i] = true
			}
		}
		m.msg = fmt.Sprintf("%d marked", m.markedVisible())
	case "ctrl+d", "u":
		m.marks = map[int]bool{}
	case "a":
		cmd = m.approve(actApprove)
	case "m":
		cmd = m.approve(actMerge)
	case "M":
		cmd = m.approve(actForce)
	case "d":
		m.askDecline()
	case "c":
		m.askComment()
	case "r":
		cmd = m.refresh()
	case "R":
		cmd = m.reload()
	case "i":
		m.toggleIgnore()
	case ".", "h":
		m.showIgnored = !m.showIgnored
	case "o":
		m.openCurrent()
	case "?":
		m.showHelp = !m.showHelp
	}
	m.pruneMarks()
	m.clamp()
	return m, cmd
}

// openCurrent opens the cursor row's URL through the Opener (the `o` key and a click on the footer URL).
func (m *Model) openCurrent() {
	if i, ok := m.current(); ok {
		if err := m.cfg.Opener(m.rows[i].pr.URL); err != nil {
			m.msg = "open failed: " + style.OneLine(err.Error())
		} else {
			m.msg = "opened " + m.rows[i].pr.URL
		}
	}
}

// rowAt maps a screen line to a visible index (-1 when it is not a list row).
func (m *Model) rowAt(y int) int {
	n := y - 1 // line 0 is the header
	if m.loading || m.loadErr != nil || m.showHelp || n < 0 || n >= m.bodyHeight() {
		return -1
	}
	if idx := m.top + n; idx < len(m.visible()) {
		return idx
	}
	return -1
}

func (m *Model) mouse(e tea.MouseMsg) {
	if m.cfg.NoMouse || m.loading || m.loadErr != nil || m.mode != modeList {
		return
	}
	v := m.visible()
	switch e := e.(type) {
	case tea.MouseMotionMsg:
		m.hover = m.rowAt(e.Y)
	case tea.MouseWheelMsg:
		switch e.Button {
		case tea.MouseWheelUp:
			m.cursor -= 3
			m.clamp()
		case tea.MouseWheelDown:
			m.cursor += 3
			m.clamp()
		}
	case tea.MouseClickMsg:
		if e.Button != tea.MouseLeft {
			return
		}
		if e.Y == m.height-2 { // footer line 1: the URL and its hint are a link
			if w := m.urlHitWidth(); w > 0 && e.X >= 0 && e.X < w {
				m.openCurrent()
			}
			return
		}
		n := m.rowAt(e.Y)
		if n < 0 {
			return
		}
		if e.X < 3 {
			m.toggleMark(v[n])
		} else {
			m.cursor = n
		}
		m.clamp()
	}
	// tea.MouseReleaseMsg changes nothing.
}

// askDecline prompts for the comment a decline needs; the PRs are declined on Enter.
func (m *Model) askDecline() {
	t, batch, ok := m.targets()
	if !ok {
		return
	}
	m.pending, m.pendingBatch, m.pendingSkipped = t, batch, 0
	m.startInput(inputDecline)
}

func (m *Model) askComment() {
	t, batch, ok := m.targets()
	if !ok {
		return
	}
	m.pending, m.pendingBatch, m.pendingSkipped = t, batch, 0
	m.startInput(inputComment)
}

func (m *Model) startInput(k inputKind) {
	m.mode, m.inputKind, m.input = modeInput, k, nil
}

func (m *Model) inputPrompt() string {
	if m.inputKind == inputDecline {
		return fmt.Sprintf("decline %d PR(s), reason (enter = decline, esc = cancel): ", len(m.pending))
	}
	return fmt.Sprintf("comment on %d PR(s) (enter = send, esc = cancel): ", len(m.pending))
}

// inputKey is the minimal single-line editor: printable runes, backspace, ctrl+u, enter, esc.
// Every key is consumed here so no list action can fire while a prompt is active.
func (m *Model) inputKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.Code == tea.KeyEscape:
		m.mode, m.pending, m.input = modeList, nil, nil
		m.msg = "cancelled"
	case k.Code == tea.KeyEnter:
		text := strings.TrimSpace(string(m.input))
		m.mode, m.input = modeList, nil
		if text == "" {
			m.pending = nil
			m.msg = "refused: empty comment"
			if m.inputKind == inputDecline {
				m.msg = "refused: a decline needs a comment"
			}
			return m, nil
		}
		if m.inputKind == inputComment {
			return m, m.runComment(text)
		}
		return m, m.runDecline(text)
	case k.Code == tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
	case k.String() == "ctrl+u":
		m.input = nil
	case k.Code == tea.KeySpace:
		m.input = append(m.input, ' ')
	default:
		for _, r := range k.Text {
			if r >= ' ' && r != 0x7f {
				m.input = append(m.input, r)
			}
		}
	}
	return m, nil
}
