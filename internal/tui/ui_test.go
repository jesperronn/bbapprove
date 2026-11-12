package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
)

// drainQuit is drain, but reports whether anything (a cmd result or a follow-up cmd) asked to quit.
func drainQuit(m *Model, c tea.Cmd) (quit bool) {
	if c == nil {
		return false
	}
	var msgs []tea.Msg
	switch msg := c().(type) {
	case tea.BatchMsg:
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, sub := range msg {
			if sub == nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := sub()
				mu.Lock()
				msgs = append(msgs, r)
				mu.Unlock()
			}()
		}
		wg.Wait()
	case nil:
	default:
		msgs = append(msgs, msg)
	}
	for _, r := range msgs {
		if _, ok := r.(tea.QuitMsg); ok {
			quit = true
		}
		if _, c2 := m.Update(r); c2 != nil {
			if _, ok := c2().(tea.QuitMsg); ok {
				quit = true
			}
		}
	}
	return quit
}

func lines(m *Model) []string { return strings.Split(plain(m.View().Content), "\n") }

func TestNoMarksActsOnCursorRow(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "j", "j", "j") // cursor on row 4 (npm#4), nothing marked
	drain(m, send(m, "m"))
	if len(fb.called) != 1 || fb.called[0] != "https://bb/pr/4" {
		t.Fatalf("m must hit only the cursor row, called %v", fb.called)
	}
	send(m, "k", "k") // row 2 (lodash)
	send(m, "d")
	if m.mode != modeInput || !strings.Contains(plain(m.View().Content), "decline 1 PR(s), reason (enter = decline, esc = cancel): █") {
		t.Fatalf("d goes straight to the reason prompt:\n%s", m.View().Content)
	}
	typeText(m, "nope")
	drain(m, send(m, "enter"))
	if len(fb.declined) != 1 || fb.declined[0] != (call{"https://bb/pr/2", "nope"}) {
		t.Fatalf("declined %v", fb.declined)
	}
	send(m, "k", "c")
	typeText(m, "hello")
	drain(m, send(m, "enter"))
	if len(fb.comments) != 1 || fb.comments[0] != (call{"https://bb/pr/1", "hello"}) {
		t.Fatalf("comments %v", fb.comments)
	}
}

func TestUppercaseKeysADCDoNothing(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "x")
	for _, k := range []string{"A", "D", "C"} {
		if c := send(m, k); c != nil || m.mode != modeList {
			t.Fatalf("%s: mode=%v", k, m.mode)
		}
	}
	if len(fb.called)+len(fb.approved)+len(fb.declined)+len(fb.comments) != 0 || len(m.marks) != 1 {
		t.Fatal("A, D and C must do nothing")
	}
}

func TestMarksAreTheTargetsAndGetUnmarked(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "x", "j", "j", "x", "j", "x") // marks 0 (ok), 2 (SKIP), 3 (ok); cursor 3
	send(m, "k", "k", "k")                // cursor on an unmarked row: m must still use the marks
	if send(m, "m") != nil || !strings.Contains(plain(m.View().Content), "approve+merge 2 PR(s)? y/N") {
		t.Fatalf("confirm count:\n%s", m.View().Content)
	}
	c := send(m, "y")
	if !strings.Contains(m.msg, "skipped 1") {
		t.Fatalf("msg %q", m.msg)
	}
	drain(m, c)
	if len(fb.called) != 2 {
		t.Fatalf("called %v msg %q", fb.called, m.msg)
	}
	if len(m.marks) != 1 || !m.marks[2] {
		t.Fatalf("acted rows must be un-marked, skipped one stays: %v", m.marks)
	}
	// shared decline comment for the remaining marked row plus another mark
	send(m, "x") // mark row 0 again (cursor 0)
	send(m, "d")
	if !strings.Contains(plain(m.View().Content), "decline 2 PR(s), reason (enter = decline, esc = cancel): ") {
		t.Fatalf("decline count:\n%s", m.View().Content)
	}
	typeText(m, "same")
	drain(m, send(m, "enter"))
	if len(fb.declined) != 2 || fb.declined[0].text != "same" || fb.declined[1].text != "same" || len(m.marks) != 0 {
		t.Fatalf("declined %v marks %v", fb.declined, m.marks)
	}
	send(m, "j", "x", "j", "j", "x", "c")
	if !strings.Contains(plain(m.View().Content), "comment on 2 PR(s)") {
		t.Fatalf("comment prompt:\n%s", m.View().Content)
	}
	send(m, "enter") // empty: refused
	if !strings.Contains(m.msg, "empty comment") || len(fb.comments) != 0 || len(m.marks) != 2 {
		t.Fatalf("msg %q marks %v", m.msg, m.marks)
	}
	send(m, "c")
	typeText(m, "shared")
	drain(m, send(m, "enter"))
	if len(fb.comments) != 2 || len(m.marks) != 0 {
		t.Fatalf("comments %v marks %v", fb.comments, m.marks)
	}
}

func TestCtrlAMarksEligibleOnly(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "ctrl+a")
	if len(m.marks) != 3 || m.marks[2] || m.msg != "3 marked" {
		t.Fatalf("marks %v msg %q", m.marks, m.msg)
	}
	send(m, "X") // unused now
	if len(m.marks) != 3 {
		t.Fatal("X must do nothing")
	}
}

func TestReloadUpdatesInPlaceAddsNewAndKeepsClosedRows(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "x", "j", "x", "j", "j") // marks #1 and #2; cursor on #4
	m.rows[3].result = wFail
	m.rows[1].result = wNotMerged
	merged := mkPR(1, fb.prs[0].Title)
	merged.State = "MERGED"
	changed := fb.prs[1]
	changed.Build = "failed"
	fb.views = map[string]adapter.PR{merged.URL: merged}
	fb.errs["view https://bb/pr/3"] = errors.New("boom")
	fb.prs = []adapter.PR{fb.prs[3], mkPR(9, "chore(deps): update dependency new to v1.0.1"), changed}
	c := send(m, "R")
	if m.msg != "reloading…" {
		t.Fatalf("msg %q", m.msg)
	}
	drain(m, c)
	if m.msg != "reloaded 3 PRs, 1 new, 1 closed elsewhere, 1 not re-read" || len(m.rows) != 5 {
		t.Fatalf("msg %q rows %d", m.msg, len(m.rows))
	}
	if m.rows[4].pr.Number != 9 {
		t.Fatalf("new PR not appended: %+v", m.rows[4].pr)
	}
	if i, _ := m.current(); m.rows[i].pr.URL != "https://bb/pr/4" {
		t.Fatalf("cursor on %s", m.rows[i].pr.URL)
	}
	if r := m.rows[0]; !r.gone || !r.locked || r.result != "MERGED" || r.resMsg != "merged elsewhere" {
		t.Fatalf("closed row: %+v", r)
	}
	if len(m.marks) != 1 || !m.marks[1] {
		t.Fatalf("the closed row loses its mark, #2 keeps it: %v", m.marks)
	}
	if r := m.rows[1]; r.result != "" || r.pr.Build != "failed" {
		t.Fatalf("changed PR: stale NOT MERGED must clear and the build follow: %+v", r)
	}
	if m.rows[3].result != wFail {
		t.Fatal("unchanged PR keeps its FAIL")
	}
	if m.rows[2].gone || m.rows[2].pr.Number != 3 {
		t.Fatalf("a row whose View failed stays as it was: %+v", m.rows[2])
	}
	line := m.renderRow(0, false, false)
	if !strings.HasPrefix(line, "  "+cDim) || strings.Contains(line, cGreen) {
		t.Fatalf("closed row must be dim only: %q", line)
	}
	m.cursor = 0
	send(m, "x")
	if m.marks[0] || m.msg != "refused: merged elsewhere" {
		t.Fatalf("closed row markable: %v %q", m.marks, m.msg)
	}
	send(m, "u", "M", "d")
	if m.mode != modeList || m.msg != "refused: merged elsewhere" {
		t.Fatalf("closed row actionable: mode %v msg %q", m.mode, m.msg)
	}
}

func TestReloadLeavesRowsMergedHereAlone(t *testing.T) {
	m, fb, _, _ := fixture(t)
	drain(m, send(m, "m")) // #1 merged by this session
	fb.prs = fb.prs[1:]
	drain(m, send(m, "R"))
	if len(fb.viewed) != 0 || m.rows[0].result != wMerged || m.rows[0].gone {
		t.Fatalf("viewed %v row %+v", fb.viewed, m.rows[0])
	}
}

func TestRefreshRereadsTargetsOnly(t *testing.T) {
	m, fb, _, _ := fixture(t)
	m.rows[0].result, m.rows[1].result, m.rows[3].result = wApproved, wFail, wFail
	upd := fb.prs[0]
	upd.Approvals, upd.CommentCount = 2, 5
	fb.views = map[string]adapter.PR{upd.URL: upd}
	drain(m, send(m, "r")) // nothing marked: cursor row #1
	if strings.Join(fb.viewed, " ") != "https://bb/pr/1" || m.msg != "refreshed 1 PR(s)" {
		t.Fatalf("viewed %v msg %q", fb.viewed, m.msg)
	}
	if r := m.rows[0]; r.result != "" || r.pr.Approvals != 2 || r.pr.CommentCount != 5 {
		t.Fatalf("refreshed row: %+v", r)
	}
	fb.viewed = nil
	declined := fb.prs[3]
	declined.State = "DECLINED"
	fb.views[declined.URL] = declined
	send(m, "j", "x", "j", "j", "x") // mark #2 and #4, cursor on #4
	drain(m, send(m, "r"))
	if len(fb.viewed) != 2 || m.msg != "refreshed 2 PR(s)" {
		t.Fatalf("viewed %v msg %q", fb.viewed, m.msg)
	}
	if m.rows[1].result != wFail {
		t.Fatal("unchanged PR keeps its FAIL")
	}
	if r := m.rows[3]; !r.gone || r.result != "DECLINED" {
		t.Fatalf("declined elsewhere: %+v", r)
	}
	if !m.marks[1] || m.marks[3] {
		t.Fatalf("marks kept, except on the closed row: %v", m.marks)
	}
	if i, _ := m.current(); i != 3 {
		t.Fatalf("cursor moved to %d", i)
	}
	fb.errs["view https://bb/pr/2"] = errors.New("nope")
	send(m, "u", "k", "k")
	drain(m, send(m, "r"))
	if m.msg != "refreshed 0 PR(s), FAIL 1: PRJ/repo#2: nope" {
		t.Fatalf("msg %q", m.msg)
	}
}

func TestStaysOpenAfterActions(t *testing.T) {
	m, fb, _, _ := fixture(t)
	if drainQuit(m, send(m, "m")) {
		t.Fatal("approve quit")
	}
	send(m, "j", "d")
	typeText(m, "x")
	if drainQuit(m, send(m, "enter")) {
		t.Fatal("decline quit")
	}
	send(m, "j", "c")
	typeText(m, "x")
	if drainQuit(m, send(m, "enter")) {
		t.Fatal("comment quit")
	}
	if len(fb.called) != 1 || len(fb.declined) != 1 || len(fb.comments) != 1 {
		t.Fatal("actions did not run")
	}
}

func TestBusyRowRefused(t *testing.T) {
	m, fb, _, _ := fixture(t)
	c := send(m, "m") // row is WORKING until the cmd runs
	for _, k := range []string{"a", "m", "M", "d", "c"} {
		if c2 := send(m, k); c2 != nil || m.msg != "row busy" || m.mode != modeList {
			t.Fatalf("%s: msg %q mode %v", k, m.msg, m.mode)
		}
	}
	drain(m, c)
	if len(fb.called) != 1 || len(fb.approved) != 0 {
		t.Fatalf("called %v approved %v", fb.called, fb.approved)
	}
}

func TestBackgroundMix(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "x", "j", "x", "d") // decline prompt for rows 0,1 is open...
	typeText(m, "bye")
	cd := send(m, "enter") // ...and runs in the background
	send(m, "j", "j")      // cursor row 4 (row 3 is SKIP)
	send(m, "j")
	ca := send(m, "m")
	drain(m, cd)
	drain(m, ca)
	if len(fb.declined) != 2 || len(fb.called) != 1 || fb.called[0] != "https://bb/pr/4" {
		t.Fatalf("declined %v called %v", fb.declined, fb.called)
	}
}

func TestWithBG(t *testing.T) {
	got := withBG("a"+cReset+"b\x1b[mc", bgCursor)
	want := bgCursor + "a" + cReset + bgCursor + "b" + cReset + bgCursor + "c" + cReset
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCursorRowBackground(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "x")
	ls := strings.Split(m.View().Content, "\n")
	cur, other := ls[1], ls[2]
	if !strings.HasPrefix(cur, bgCursor) || strings.Contains(other, bgCursor) {
		t.Fatalf("cursor bg: %q / %q", cur, other)
	}
	if !strings.Contains(plain(cur), "›") {
		t.Fatal("› marker kept")
	}
	if w := ansi.StringWidth(cur); w != 100 {
		t.Fatalf("cursor line must be padded to the width, got %d", w)
	}
	for i := strings.Index(cur, cReset); i >= 0; {
		rest := cur[i+len(cReset):]
		if rest != "" && !strings.HasPrefix(rest, bgCursor) {
			t.Fatalf("reset not followed by bg at %q", rest[:10])
		}
		j := strings.Index(rest, cReset)
		if j < 0 {
			break
		}
		i += len(cReset) + j
	}
	if !strings.Contains(cur, cBold+cYellow+"▌") {
		t.Fatal("mark bar should be bold yellow")
	}
	// truncated cursor row stays within the width and keeps the background
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	c30 := strings.Split(m.View().Content, "\n")[1]
	if ansi.StringWidth(c30) > 30 || !strings.HasPrefix(c30, bgCursor) {
		t.Fatalf("narrow: %d %q", ansi.StringWidth(c30), c30)
	}
}

func TestNoColorHasNoEscapesOrBackground(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.cfg.NoColor = true
	send(m, "x")
	v := m.View().Content
	if strings.Contains(v, "\x1b[") || strings.Contains(v, "48;5") {
		t.Fatalf("escapes under NO_COLOR: %q", v)
	}
	if !strings.Contains(v, "›") || !strings.Contains(v, "▌") {
		t.Fatal("markers must stay")
	}
}

func TestHoverBackgroundFainter(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.MouseMotionMsg{X: 10, Y: 3})
	if m.hover != 2 {
		t.Fatalf("hover %d", m.hover)
	}
	ls := strings.Split(m.View().Content, "\n")
	if !strings.HasPrefix(ls[3], bgHover) || !strings.HasPrefix(ls[1], bgCursor) {
		t.Fatal("hover/cursor backgrounds missing")
	}
	m.Update(tea.MouseMotionMsg{X: 10, Y: 0})
	if m.hover != -1 {
		t.Fatal("header clears hover")
	}
}

func TestViewNeverExceedsWidthOrHeight(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.cfg.Version = "v1.0.0-rc.1"
	m.rows[0].pr.Title += strings.Repeat(" long", 30)
	for _, help := range []bool{false, true} {
		m.showHelp = help
		for _, w := range []int{200, 100, 60, 40, 20, 8} {
			m.Update(tea.WindowSizeMsg{Width: w, Height: 16})
			ls := strings.Split(m.View().Content, "\n")
			if len(ls) != 16 {
				t.Fatalf("help=%v w=%d: %d lines", help, w, len(ls))
			}
			for i, l := range ls {
				if ansi.StringWidth(l) > w {
					t.Fatalf("help=%v w=%d line %d too wide (%d): %q", help, w, i, ansi.StringWidth(l), plain(l))
				}
			}
		}
	}
}

func TestFooterLineOne(t *testing.T) {
	m, _, opened, _ := fixture(t)
	m.cfg.Version = "v1.0.0-rc.1"
	status := func() string { ls := lines(m); return ls[len(ls)-2] }
	s := status()
	if !strings.HasPrefix(s, "https://bb/pr/1") || !strings.HasSuffix(s, "bbapprove v1.0.0-rc.1") || ansi.StringWidth(s) != 100 {
		t.Fatalf("line 1: %q", s)
	}
	if !strings.Contains(m.View().Content, cDim+"bbapprove v1.0.0-rc.1") {
		t.Fatal("version must be dim")
	}
	send(m, "j")
	if !strings.HasPrefix(status(), "https://bb/pr/2") {
		t.Fatalf("url follows cursor: %q", status())
	}
	send(m, "o")
	if s := status(); !strings.HasPrefix(s, "opened https://bb/pr/2") || !strings.HasSuffix(s, "v1.0.0-rc.1") || len(*opened) != 1 {
		t.Fatalf("after o: %q", s)
	}
	// a long message is truncated on the left, the version stays
	m.msg = strings.Repeat("very long message ", 20)
	if s := status(); !strings.HasSuffix(s, "bbapprove v1.0.0-rc.1") || !strings.Contains(s, "…") || ansi.StringWidth(s) != 100 {
		t.Fatalf("truncation: %q", s)
	}
	// prompts take the slot too
	m.msg = ""
	send(m, "c")
	if s := status(); !strings.HasPrefix(s, "comment on 1 PR(s)") || !strings.HasSuffix(s, "v1.0.0-rc.1") {
		t.Fatalf("prompt: %q", s)
	}
}

func TestHintLineDropsFromTheEnd(t *testing.T) {
	m, _, _, _ := fixture(t)
	hint := func() string { ls := lines(m); return ls[len(ls)-1] }
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 20})
	full := hint()
	if !strings.HasPrefix(full, "4 PRs, 0 marked") || !strings.Contains(full, "a approve") || !strings.HasSuffix(full, "q quit") {
		t.Fatalf("wide: %q", full)
	}
	prev := len(full)
	for _, w := range []int{80, 60, 45, 30, 12} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		h := hint()
		if ansi.StringWidth(h) > w || len(h) > prev {
			t.Fatalf("w=%d: %q", w, h)
		}
		prev = len(h)
		if len(lines(m)) != 20 {
			t.Fatalf("w=%d wrapped: %d lines", w, len(lines(m)))
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 45, Height: 20})
	if h := hint(); strings.Contains(h, "q quit") || !strings.Contains(h, "a approve") {
		t.Fatalf("w=45: %q", h)
	}
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func TestMouse(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(click(20, 2)) // header is line 0, so line 2 is the second row
	if m.cursor != 1 || len(m.marks) != 0 {
		t.Fatalf("cursor %d marks %v", m.cursor, m.marks)
	}
	m.Update(click(1, 4)) // mark column of the 4th row
	if !m.marks[3] || m.cursor != 1 {
		t.Fatalf("mark column: cursor %d marks %v", m.cursor, m.marks)
	}
	m.Update(click(2, 4))
	if len(m.marks) != 0 {
		t.Fatal("second click unmarks")
	}
	for _, y := range []int{0, 5, 17, 18, 19} { // header, empty body, footer
		m.Update(click(20, y))
		m.Update(click(0, y))
	}
	if m.cursor != 1 || len(m.marks) != 0 {
		t.Fatalf("outside clicks must be ignored: cursor %d marks %v", m.cursor, m.marks)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.cursor != 3 {
		t.Fatalf("wheel down: %d", m.cursor)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.cursor != 0 {
		t.Fatalf("wheel up: %d", m.cursor)
	}
}

func TestMouseWheelScrollsViewport(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 5}) // body 2
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.cursor != 3 || m.top != 2 {
		t.Fatalf("cursor %d top %d", m.cursor, m.top)
	}
}

func TestMouseIgnoredInPromptAndWhenDisabled(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "c")
	m.Update(click(20, 3))
	m.Update(click(0, 2))
	m.Update(tea.MouseMotionMsg{X: 10, Y: 3})
	if m.cursor != 0 || len(m.marks) != 0 || m.hover != -1 || m.mode != modeInput {
		t.Fatalf("prompt: cursor %d marks %v hover %d", m.cursor, m.marks, m.hover)
	}
	m, _, _, _ = fixture(t)
	m.cfg.NoMouse = true
	m.Update(click(20, 3))
	if m.cursor != 0 {
		t.Fatal("NoMouse must ignore clicks")
	}
}

func TestMouseEnabled(t *testing.T) {
	for _, tc := range []struct {
		env  string
		args []string
		want bool
	}{{"", nil, true}, {"1", nil, true}, {"0", nil, false}, {"", []string{"-i", "--no-mouse"}, false}} {
		if got := MouseEnabled(tc.env, tc.args); got != tc.want {
			t.Errorf("%q %v: %v", tc.env, tc.args, got)
		}
	}
}

func TestFailedRowReopensForRetry(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/1"] = &PartialError{Err: errors.New("merge HTTP 409: conflict")}
	drain(m, send(m, "m"))
	if w, _, _ := m.status(m.rows[0]); w != wNotMerged || !m.eligible(0) {
		t.Fatalf("after a failed merge: status %q eligible %v", w, m.eligible(0))
	}
	delete(fb.errs, "https://bb/pr/1")
	drain(m, send(m, "m"))
	if w, _, _ := m.status(m.rows[0]); w != wMerged || m.eligible(0) || len(fb.called) != 2 {
		t.Fatalf("retry: status %q eligible %v called %v", w, m.eligible(0), fb.called)
	}
	send(m, "ctrl+a")
	if m.marks[0] {
		t.Fatal("a merged row must not be marked again")
	}
}
