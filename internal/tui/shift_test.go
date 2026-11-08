package tui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
	"bbapprove/internal/rules"
)

const (
	instantHints = "a approve  d decline  c comment  i ignore  x mark  o open  ? help  q quit"
	batchHints   = "A approve marked  D decline marked  C comment marked  ctrl+a mark all  x mark  ? help  q quit"
)

// Events as a terminal with the kitty keyboard protocol (all enhancements on) reports them.
func shiftDown(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: tea.ModShift} }
func shiftUp(code rune) tea.KeyReleaseMsg { return tea.KeyReleaseMsg{Code: code} }

// kitty returns the key as the protocol reports it: letters carry their shifted code and text.
func kittyKey(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "ctrl+a":
		return tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	r := []rune(s)[0]
	if r >= 'A' && r <= 'Z' {
		return tea.KeyPressMsg{Code: r + 32, ShiftedCode: r, Text: s, Mod: tea.ModShift}
	}
	shifted := map[rune]rune{'!': '1', '?': '/', ':': ';', '(': '9', ')': '0', '_': '-', '@': '2', '#': '3'}
	if base, ok := shifted[r]; ok {
		return tea.KeyPressMsg{Code: base, ShiftedCode: r, Text: s, Mod: tea.ModShift}
	}
	return tea.KeyPressMsg{Code: r, Text: s}
}

func (m *Model) kittySend(keys ...string) {
	for _, k := range keys {
		m.Update(kittyKey(k))
	}
}

func hintOf(m *Model) string {
	ls := lines(m)
	return ls[len(ls)-1]
}

// supported tells the model the terminal answered the keyboard query, on a screen wide enough for
// the counts plus the whole batch hint set.
func supported(m *Model) {
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 20})
	m.Update(tea.KeyboardEnhancementsMsg{Flags: 31})
}

func TestShiftHeldSwitchesHints(t *testing.T) {
	m, _, _, _ := fixture(t)
	supported(m)
	if h := hintOf(m); !strings.Contains(h, instantHints) {
		t.Fatalf("default hints: %q", h)
	}
	m.Update(shiftDown(tea.KeyLeftShift))
	if h := hintOf(m); !strings.Contains(h, batchHints) || strings.Contains(h, "a approve ") {
		t.Fatalf("shift held: %q", h)
	}
	m.Update(shiftUp(tea.KeyLeftShift))
	if h := hintOf(m); !strings.Contains(h, instantHints) {
		t.Fatalf("shift released: %q", h)
	}
	m.Update(shiftDown(tea.KeyRightShift))
	if h := hintOf(m); !strings.Contains(h, batchHints) {
		t.Fatalf("right shift: %q", h)
	}
	m.Update(shiftUp(tea.KeyRightShift))
	if h := hintOf(m); !strings.Contains(h, instantHints) {
		t.Fatalf("right shift released: %q", h)
	}
}

func TestBothShiftsNeedBothReleases(t *testing.T) {
	m, _, _, _ := fixture(t)
	supported(m)
	m.Update(shiftDown(tea.KeyLeftShift))
	m.Update(tea.KeyPressMsg{Code: tea.KeyRightShift, Mod: tea.ModShift})
	m.Update(shiftUp(tea.KeyLeftShift))
	if !strings.Contains(hintOf(m), batchHints) {
		t.Fatal("right shift still held: batch hints expected")
	}
	m.Update(shiftUp(tea.KeyRightShift))
	if !strings.Contains(hintOf(m), instantHints) {
		t.Fatal("both released: instant hints expected")
	}
}

func TestUnsupportedTerminalNeverChangesHints(t *testing.T) {
	m, _, _, _ := fixture(t) // no KeyboardEnhancementsMsg: the terminal does not speak the protocol
	before := hintOf(m)
	m.Update(shiftDown(tea.KeyLeftShift))
	if got := hintOf(m); got != before || !strings.Contains(got, instantHints) {
		t.Fatalf("unsupported terminal changed hints: %q", got)
	}
}

func TestNoKittyNeverChangesHintsNorRequestsEnhancements(t *testing.T) {
	m, _, _, _ := fixture(t)
	if v := m.View(); v.KeyboardEnhancements != (tea.KeyboardEnhancements{
		ReportEventTypes: true, ReportAlternateKeys: true, ReportAllKeysAsEscapeCodes: true, ReportAssociatedText: true,
	}) || !v.ReportFocus {
		t.Fatalf("default view must request the enhancements: %+v", v.KeyboardEnhancements)
	}
	m.cfg.NoKitty = true
	if v := m.View(); v.KeyboardEnhancements != (tea.KeyboardEnhancements{}) || v.ReportFocus {
		t.Fatalf("BBAPPROVE_KITTY=0 must not request enhancements: %+v", v.KeyboardEnhancements)
	}
	supported(m)
	m.Update(shiftDown(tea.KeyLeftShift))
	if !strings.Contains(hintOf(m), instantHints) {
		t.Fatalf("NoKitty changed hints: %q", hintOf(m))
	}
}

func TestShiftStateClearedByBlurAndByKeyWithoutShift(t *testing.T) {
	m, _, _, _ := fixture(t)
	supported(m)
	m.Update(shiftDown(tea.KeyLeftShift))
	m.Update(tea.BlurMsg{})
	if !strings.Contains(hintOf(m), instantHints) {
		t.Fatal("blur must restore the instant hints")
	}
	m.Update(shiftDown(tea.KeyLeftShift))
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // release was lost; this key has no Shift
	if !strings.Contains(hintOf(m), instantHints) {
		t.Fatal("a key press without Shift must restore the instant hints")
	}
	// A key pressed WITH Shift keeps the batch hints.
	m.Update(shiftDown(tea.KeyLeftShift))
	m.Update(kittyKey("J"))
	if !strings.Contains(hintOf(m), batchHints) {
		t.Fatal("a shifted key press must not clear the Shift state")
	}
}

func TestBareModifiersAreInert(t *testing.T) {
	m, _, _, _ := fixture(t)
	supported(m)
	send(m, "x", "j")
	m.msg = "keep me"
	m.Update(shiftDown(tea.KeyLeftShift))
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeftCtrl, Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: tea.KeyCapsLock})
	if m.msg != "keep me" || m.cursor != 1 || !m.marks[0] {
		t.Fatalf("a bare modifier changed state: msg %q cursor %d marks %v", m.msg, m.cursor, m.marks)
	}
	// In a confirm prompt a Shift press must not cancel it; Shift+Y then confirms.
	m.kittySend("A")
	if m.mode != modeConfirm {
		t.Fatalf("mode %v", m.mode)
	}
	m.Update(shiftUp(tea.KeyLeftShift))
	m.Update(shiftDown(tea.KeyLeftShift))
	if m.mode != modeConfirm {
		t.Fatal("Shift press cancelled the confirm prompt")
	}
	m.kittySend("Y")
	if m.mode != modeList {
		t.Fatalf("mode %v", m.mode)
	}
}

func TestPromptTypingUnderEnhancements(t *testing.T) {
	text := "Hello! a:(x)_?  #Z@"
	for _, kind := range []string{"c", "D"} {
		m, fb, _, ign := fixture(t)
		supported(m)
		m.Update(kittyKey(kind))
		if kind == "D" { // D with no marks refuses; mark first
			m.kittySend("x", "D", "y")
		}
		if m.mode != modeInput {
			t.Fatalf("%s: mode %v", kind, m.mode)
		}
		for _, r := range text {
			// Shift is physically held around shifted characters, exactly like a real keyboard.
			shifted := r != ' ' && kittyKey(string(r)).Mod == tea.ModShift
			if shifted {
				m.Update(shiftDown(tea.KeyLeftShift))
			}
			if r == ' ' {
				m.kittySend("space")
			} else {
				m.kittySend(string(r))
			}
			if shifted {
				m.Update(shiftUp(tea.KeyLeftShift))
			}
		}
		if string(m.input) != text {
			t.Fatalf("%s: input %q want %q", kind, string(m.input), text)
		}
		m.kittySend("backspace", "backspace")
		if string(m.input) != text[:len(text)-2] {
			t.Fatalf("%s: backspace: %q", kind, string(m.input))
		}
		m.kittySend("ctrl+u")
		if len(m.input) != 0 {
			t.Fatalf("%s: ctrl+u left %q", kind, string(m.input))
		}
		m.kittySend("H", "i", "!")
		if string(m.input) != "Hi!" {
			t.Fatalf("%s: %q", kind, string(m.input))
		}
		if len(fb.called)+len(fb.declined)+len(fb.comments) != 0 || len(m.marks) > 1 || m.showIgnored {
			t.Fatalf("%s: a list action fired while typing", kind)
		}
		if _, err := os.Stat(ign); err == nil {
			t.Fatalf("%s: i wrote the ignore file", kind)
		}
		cmd := tea.Cmd(nil)
		_, cmd = m.Update(kittyKey("enter"))
		drain(m, cmd)
		if kind == "c" && (len(fb.comments) != 1 || fb.comments[0].text != "Hi!") {
			t.Fatalf("comment not sent: %v", fb.comments)
		}
		if kind == "D" && (len(fb.declined) != 1 || fb.declined[0].text != "Hi!") {
			t.Fatalf("decline not sent: %v", fb.declined)
		}
	}
}

func TestBatchKeysUnderEnhancements(t *testing.T) {
	m, _, _, _ := fixture(t)
	supported(m)
	m.Update(kittyKey("ctrl+a"))
	if len(m.marks) != 3 {
		t.Fatalf("ctrl+a: marks %v", m.marks)
	}
	for key, want := range map[string]mode{"A": modeConfirm, "D": modeConfirmDecline, "C": modeInput} {
		m.Update(shiftDown(tea.KeyLeftShift))
		m.Update(kittyKey(key))
		if m.mode != want {
			t.Fatalf("%s: mode %v want %v", key, m.mode, want)
		}
		m.Update(shiftUp(tea.KeyLeftShift))
		m.Update(kittyKey("esc"))
		m.Update(kittyKey("esc"))
		m.mode = modeList
	}
	// A terminal that reports Shift+a without text: still the A action.
	m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModShift})
	if m.mode != modeConfirm {
		t.Fatalf("shift+a without text: mode %v", m.mode)
	}
	m.mode = modeList
	m.Update(tea.KeyPressMsg{Code: '/', ShiftedCode: '?', Text: "?", Mod: tea.ModShift})
	if !m.showHelp {
		t.Fatal("? must toggle help")
	}
	m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m.Update(tea.KeyPressMsg{Code: '.', Text: "."})
	if m.showIgnored {
		t.Fatal("h then . must toggle twice")
	}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m.Update(tea.MouseClickMsg{X: 10, Y: 2, Button: tea.MouseLeft})
	if m.cursor != 1 {
		t.Fatalf("mouse click under enhancements: cursor %d", m.cursor)
	}
}

func TestShiftHintsNeverWrap(t *testing.T) {
	m, _, _, _ := fixture(t)
	supported(m)
	m.Update(shiftDown(tea.KeyLeftShift))
	for w := 1; w <= 130; w++ {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 12})
		for _, l := range strings.Split(m.View().Content, "\n") {
			if ansi.StringWidth(l) > w {
				t.Fatalf("w=%d: too wide: %q", w, plain(l))
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	h := hintOf(m)
	if !strings.HasPrefix(h, "A approve marked") && !strings.Contains(h, "A approve marked") || strings.Contains(h, "q quit") {
		t.Fatalf("w=60 must drop trailing items: %q", h)
	}
}

func TestHelpListsBothSets(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 24})
	send(m, "?")
	out := plain(m.View().Content)
	for _, w := range []string{instantHints, "A approve marked  D decline marked  C comment marked  ctrl+a mark all  x mark", "Shift"} {
		if !strings.Contains(out, w) {
			t.Fatalf("help lacks %q:\n%s", w, out)
		}
	}
}

// TestShiftThroughRealProgram feeds the raw escape sequences a kitty-protocol terminal sends through
// the real Bubble Tea input parser: the query reply, Left Shift press and release, and kitty-encoded
// text typed into the comment prompt.
func TestShiftThroughRealProgram(t *testing.T) {
	fb := &fakeBackend{prs: []adapter.PR{mkPR(1, "chore(deps): update dependency npm to v1.2.3")}, errs: map[string]error{}}
	cfg := rules.Config{AuthorEmail: bot}
	m := New(Config{Backend: fb, IgnorePath: filepath.Join(t.TempDir(), "ignore"), Check: cfg.Check, Concurrency: 2})
	pr, pw := io.Pipe()
	out := &lockedBuf{}
	done := make(chan int, 1)
	go func() {
		done <- runModel(t.Context(), RunOptions{Stdin: pr, Stdout: out, Stderr: os.Stderr, Width: 120, Height: 12}, m, false)
	}()
	waitFor(t, "list rendered", func() bool { return strings.Contains(out.String(), "chore(deps)") })
	feed := func(s string) { pw.Write([]byte(s)); time.Sleep(60 * time.Millisecond) }
	feed("\x1b[?31u")     // reply to the keyboard query: protocol supported
	feed("\x1b[57441;2u") // Left Shift press
	waitFor(t, "batch hints", func() bool { return strings.Contains(plain(out.String()), "decline marked") })
	feed("\x1b[57441;1:3u") // Left Shift release
	waitFor(t, "instant hints again", func() bool {
		s := plain(out.String())
		return strings.LastIndex(s, "o open") > strings.LastIndex(s, "decline marked")
	})
	feed("c") // comment prompt (kitty: plain key = CSI 99u)
	feed("\x1b[104:72;2;72u")
	feed("\x1b[101u\x1b[108u\x1b[108u\x1b[111u\x1b[49:33;2;33u")
	waitFor(t, "typed text echoed", func() bool { return strings.Contains(plain(out.String()), "ello!") })
	pw.Write([]byte("\x1b[99;5u")) // ctrl+c
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("exit code %d", rc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program did not quit")
	}
	if got := string(m.input); got != "Hello!" { // the program is done: reading the model is race free
		t.Fatalf("typed text %q, want Hello!", got)
	}
}
