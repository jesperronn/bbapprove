package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"bbapprove/internal/adapter"
	"bbapprove/internal/app"
	"bbapprove/internal/rules"
)

func TestStatusColumnBlankForEligibleAndInFlightWords(t *testing.T) {
	m, _, _, _ := fixture(t)
	ls := lines(m)
	if !strings.HasPrefix(ls[0], "  STATUS       🔨") {
		t.Fatalf("header %q", ls[0])
	}
	if !strings.HasPrefix(ls[1], "›              ✓  ") || !strings.HasPrefix(ls[3], "  SKIP        ") {
		t.Fatalf("eligible must be blank, SKIP kept:\n%s", strings.Join(ls, "\n"))
	}
	if strings.Contains(strings.Join(ls, "\n"), "ELIGIBLE") {
		t.Fatal("no ELIGIBLE word any more")
	}
	cA := send(m, "a")
	send(m, "j")
	send(m, "d", "y")
	typeText(m, "bye")
	cD := send(m, "enter")
	send(m, "j")
	send(m, "c")
	typeText(m, "hey")
	cC := send(m, "enter")
	ls = lines(m)
	for i, w := range []string{"APPROVING…  ", "DECLINING…  ", "COMMENTING…"} {
		if !strings.HasPrefix(strings.TrimLeft(ls[i+1], "› "), w) {
			t.Errorf("row %d want %q: %q", i+1, w, ls[i+1])
		}
	}
	drain(m, cA)
	drain(m, cD)
	drain(m, cC)
	ls = lines(m)
	for i, w := range []string{"MERGED", "DECLINED", "COMMENTED"} {
		if !strings.HasPrefix(strings.TrimLeft(ls[i+1], "› "), w) {
			t.Errorf("row %d want %q: %q", i+1, w, ls[i+1])
		}
	}
	m.Update(reloadedMsg{prs: m.rowsPRs()})
	if ls = lines(m); strings.Contains(strings.Join(ls, "\n"), "MERGED") || strings.Contains(strings.Join(ls, "\n"), "COMMENTED") {
		t.Fatalf("a reload resets row states:\n%s", strings.Join(ls, "\n"))
	}
}

func (m *Model) rowsPRs() []adapter.PR {
	var prs []adapter.PR
	for _, r := range m.rows {
		prs = append(prs, r.pr)
	}
	return prs
}

func TestHAliasesDotToggle(t *testing.T) {
	for _, k := range []string{".", "h"} {
		m, _, _, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 20})
		send(m, "i")
		if strings.Contains(plain(m.View().Content), "#1 ") {
			t.Fatalf("%s: npm rows should be hidden", k)
		}
		send(m, k)
		out := plain(m.View().Content)
		if strings.Count(out, "IGNORED") != 2 || !strings.Contains(out, "— on your ignore list: npm") {
			t.Fatalf("%s: want 2 IGNORED rows with reason:\n%s", k, out)
		}
		send(m, k)
		if strings.Contains(plain(m.View().Content), "IGNORED") {
			t.Fatalf("%s: toggles back", k)
		}
	}
	m, _, _, _ := fixture(t)
	send(m, "?")
	if !strings.Contains(plain(m.View().Content), ". or h ignored") {
		t.Fatal("help lacks h")
	}
}

func TestIgnoredRowCannotBeApproved(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "i", "h") // npm ignored; show them; cursor on row 1
	if c := send(m, "a"); c != nil || !strings.Contains(m.msg, "refused") {
		t.Fatalf("msg %q", m.msg)
	}
	send(m, "ctrl+a", "A")
	if m.mode == modeConfirm && len(m.pending) != 1 {
		t.Fatalf("pending %v", m.pending)
	}
	if len(fb.called) != 0 {
		t.Fatalf("called %v", fb.called)
	}
}

func TestSummaryNoChanges(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "j", "x", "r")
	if got := m.Summary(); got != "No changes made.\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSummaryScriptedSession(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/4"] = &PartialError{Err: errors.New("merge conflict")}
	drain(m, send(m, "a")) // row 1 -> MERGED
	send(m, "j", "d", "y")
	typeText(m, "bye")
	drain(m, send(m, "enter")) // row 2 -> DECLINED
	send(m, "j", "c")
	typeText(m, "hello")
	drain(m, send(m, "enter")) // row 3 -> COMMENTED
	send(m, "j")
	drain(m, send(m, "a")) // row 4 -> NOT MERGED
	send(m, "i")           // ignore npm (row 4 stays: it is part of an action)
	send(m, "i")           // un-ignore it again
	drain(m, m.reload())   // a reload resets rows, not the log
	if strings.Contains(strings.Join(lines(m), "\n"), "MERGED") {
		t.Fatal("reload should reset the rows")
	}
	want := []string{
		"STATUS       🔨 🔎 👍 💬  ✅  PR   TITLE",
		"MERGED       ✓  ?  –    0   0 PRJ/repo#1 chore(deps): update dependency npm to v1.2.3",
		"DECLINED     ✓  ?  –    0   0 PRJ/repo#2 chore(deps): update dependency lodash to v4.17.21",
		"COMMENTED    ✓  ?  –    0   0 PRJ/repo#3 chore(deps): update dependency react to v19",
		"NOT MERGED   ✓  ?  –    0   0 PRJ/repo#4 chore(deps): update dependency npm to v1.2.4 — merge conflict",
		"IGNORED      ✓  ?  –    0   0 PRJ/repo#4 chore(deps): update dependency npm to v1.2.4 — dependency: npm",
		"UN-IGNORED   ✓  ?  –    0   0 PRJ/repo#4 chore(deps): update dependency npm to v1.2.4 — dependency: npm",
		"1 merged, 1 not merged, 1 declined, 1 commented, 1 ignored, 1 un-ignored",
		"",
	}
	if got := m.Summary(); got != strings.Join(want, "\n") {
		t.Fatalf("summary:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestSummaryFailAndRepeatReplaces(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/1"] = errors.New("boom")
	drain(m, send(m, "a"))
	delete(fb.errs, "https://bb/pr/1")
	send(m, "c")
	typeText(m, "x")
	drain(m, send(m, "enter")) // same PR, other verb: its own entry
	send(m, "c")
	typeText(m, "y")
	drain(m, send(m, "enter")) // same verb again: replaces
	got := m.Summary()
	if strings.Count(got, "PRJ/repo#1") != 2 || !strings.Contains(got, "FAIL") || !strings.HasSuffix(got, "1 commented, 1 failed\n") {
		t.Fatalf("summary:\n%s", got)
	}
}

func TestSummaryInFlightAtQuit(t *testing.T) {
	m, _, _, _ := fixture(t)
	c := send(m, "a")
	got := m.Summary()
	if !strings.Contains(got, "WORKING…") || !strings.HasSuffix(got, "1 working (still running)\n") {
		t.Fatalf("summary:\n%s", got)
	}
	drain(m, c)
	if got = m.Summary(); strings.Contains(got, "WORKING") || strings.Contains(got, "still running") {
		t.Fatalf("after completion:\n%s", got)
	}
	// an action finishing for a PR that a reload dropped still lands in the log
	m2, _, _, _ := fixture(t)
	c = send(m2, "a")
	m2.Update(reloadedMsg{prs: m2.rowsPRs()[1:]})
	drain(m2, c)
	if got = m2.Summary(); !strings.Contains(got, "MERGED") || !strings.HasSuffix(got, "1 merged\n") {
		t.Fatalf("dropped row:\n%s", got)
	}
}

func TestSummaryStyle(t *testing.T) {
	build := func(st app.Style) *Model {
		fb := &fakeBackend{prs: []adapter.PR{mkPR(1, "chore(deps): update dependency lodash to v4.17.21")}, errs: map[string]error{}}
		cfg := rules.Config{AuthorEmail: bot}
		m := New(Config{Backend: fb, IgnorePath: filepath.Join(t.TempDir(), "ignore"), Check: cfg.Check, Summary: st})
		m.Update(m.Init()())
		drain(m, send(m, "a"))
		return m
	}
	tests := []struct {
		name  string
		st    app.Style
		check func(t *testing.T, s string)
	}{
		{"piped: no truncation, no colour", app.Style{}, func(t *testing.T, s string) {
			if strings.Contains(s, "\033") || !strings.Contains(s, "lodash to v4.17.21") {
				t.Fatalf("%q", s)
			}
		}},
		{"tty width truncates", app.Style{Width: 30}, func(t *testing.T, s string) {
			for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
				if w := len([]rune(l)); w > 31 { // tally line is not a table row but is short here
					t.Fatalf("line too wide (%d): %q", w, l)
				}
			}
			if strings.Contains(s, "lodash to v4.17.21") {
				t.Fatalf("not truncated: %q", s)
			}
		}},
		{"colour", app.Style{Color: true}, func(t *testing.T, s string) {
			if !strings.Contains(s, "\033[") {
				t.Fatalf("no colour: %q", s)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, build(tc.st).Summary()) })
	}
}

func TestSummaryStyleNoColorEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if st := summaryStyle(&bytes.Buffer{}, env(map[string]string{"FORCE_COLOR": "1"})); !st.Color || st.Width != 0 {
		t.Fatalf("FORCE_COLOR piped: %+v", st)
	}
	if st := summaryStyle(&bytes.Buffer{}, env(map[string]string{"FORCE_COLOR": "1", "NO_COLOR": "1"})); st.Color {
		t.Fatal("NO_COLOR must win")
	}
	if st := summaryStyle(&bytes.Buffer{}, env(nil)); st.Color || st.Width != 0 {
		t.Fatalf("plain pipe: %+v", st)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// runWithKeys drives the real tea.Program (headless, no TTY) with a fake backend: the keys are fed one at
// a time and the exit summary is read from stdout after quitting.
func runWithKeys(t *testing.T, fb *fakeBackend, keys ...string) string {
	t.Helper()
	cfg := rules.Config{AuthorEmail: bot}
	m := New(Config{Backend: fb, IgnorePath: filepath.Join(t.TempDir(), "ignore"), Check: cfg.Check, Concurrency: 2})
	pr, pw := io.Pipe()
	out := &lockedBuf{}
	done := make(chan int, 1)
	go func() {
		done <- runModel(context.Background(), RunOptions{Stdin: pr, Stdout: out, Stderr: os.Stderr, Width: 100, Height: 20}, m, false)
	}()
	waitFor(t, "list rendered", func() bool { return strings.Contains(out.String(), "chore(deps)") })
	for _, k := range keys {
		pw.Write([]byte(k))
		time.Sleep(20 * time.Millisecond)
	}
	pw.Write([]byte("q"))
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("exit code %d", rc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program did not quit")
	}
	return out.String()
}

func TestRunPrintsSummaryAfterQuit(t *testing.T) {
	fb := &fakeBackend{prs: []adapter.PR{
		mkPR(1, "chore(deps): update dependency npm to v1.2.3"),
		mkPR(2, "chore(deps): update dependency lodash to v4.17.21"),
	}, errs: map[string]error{}}
	out := runWithKeys(t, fb, "a", "j", "c", "hi", "\r")
	i := strings.LastIndex(out, "\x1b[?1049l") // alt screen left; the summary comes after it
	if i < 0 {
		t.Fatal("no alt screen exit in output")
	}
	tail := out[i:]
	for _, w := range []string{"MERGED", "PRJ/repo#1", "COMMENTED", "PRJ/repo#2", "1 merged, 1 commented\n"} {
		if !strings.Contains(tail, w) {
			t.Errorf("summary lacks %q:\n%q", w, tail)
		}
	}
	if len(fb.called) != 1 || len(fb.comments) != 1 {
		t.Fatalf("backend calls %v %v", fb.called, fb.comments)
	}
}

func TestRunNoChanges(t *testing.T) {
	fb := &fakeBackend{prs: []adapter.PR{mkPR(1, "chore(deps): update dependency npm to v1.2.3")}, errs: map[string]error{}}
	out := runWithKeys(t, fb, "j", "x")
	if !strings.HasSuffix(out, "No changes made.\n") {
		t.Fatalf("tail %q", out[max(0, len(out)-80):])
	}
}
