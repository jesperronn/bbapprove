package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
	"bbapprove/internal/rules"
)

type fakeBackend struct {
	prs      []adapter.PR
	mu       sync.Mutex
	called   []string
	inflight int32
	maxSeen  int32
	errs     map[string]error
	warns    map[string]string // ApproveAndMerge warnings by url
	declined []call            // Decline calls: url + comment
	comments []call            // Comment calls: url + body
	approved []string          // Approve (approve-only) calls
}

func (f *fakeBackend) Approve(_ context.Context, pr adapter.PR) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = append(f.approved, pr.URL)
	return f.errs[pr.URL]
}

type call struct{ url, text string }

func (f *fakeBackend) Comment(_ context.Context, pr adapter.PR, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, call{pr.URL, body})
	return f.errs[pr.URL]
}

func (f *fakeBackend) Decline(_ context.Context, pr adapter.PR, comment string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.declined = append(f.declined, call{pr.URL, comment})
	return f.errs[pr.URL]
}

func (f *fakeBackend) List(context.Context) ([]adapter.PR, error) { return f.prs, nil }
func (f *fakeBackend) ApproveAndMerge(_ context.Context, pr adapter.PR) (string, error) {
	n := atomic.AddInt32(&f.inflight, 1)
	for {
		o := atomic.LoadInt32(&f.maxSeen)
		if n <= o || atomic.CompareAndSwapInt32(&f.maxSeen, o, n) {
			break
		}
	}
	defer atomic.AddInt32(&f.inflight, -1)
	f.mu.Lock()
	f.called = append(f.called, pr.URL)
	f.mu.Unlock()
	return f.warns[pr.URL], f.errs[pr.URL]
}

const bot = "bot@example.com"

func mkPR(n int, title string) adapter.PR {
	pr := adapter.PR{URL: fmt.Sprintf("https://bb/pr/%d", n), Number: n, Project: "PRJ", Repo: "repo", Title: title, Build: "passed"}
	pr.Author.Email = bot
	return pr
}

func fixture(t *testing.T) (*Model, *fakeBackend, *[]string, string) {
	t.Helper()
	fb := &fakeBackend{prs: []adapter.PR{
		mkPR(1, "chore(deps): update dependency npm to v1.2.3"),
		mkPR(2, "chore(deps): update dependency lodash to v4.17.21"),
		mkPR(3, "chore(deps): update dependency react to v19"), // major -> SKIP
		mkPR(4, "chore(deps): update dependency npm to v1.2.4"),
	}, errs: map[string]error{}}
	var opened []string
	ign := filepath.Join(t.TempDir(), "ignore")
	cfg := rules.Config{AuthorEmail: bot}
	m := New(Config{Backend: fb, IgnorePath: ign, Check: cfg.Check, Concurrency: 2,
		Opener: func(u string) error { opened = append(opened, u); return nil }})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.Update(m.Init()())
	return m, fb, &opened, ign
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+a":
		return tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func send(m *Model, keys ...string) tea.Cmd {
	var c tea.Cmd
	for _, k := range keys {
		_, c = m.Update(key(k))
	}
	return c
}

// drain runs a cmd (recursing into batches) and feeds results back; returns when done.
func drain(m *Model, c tea.Cmd) {
	if c == nil {
		return
	}
	switch msg := c().(type) {
	case tea.BatchMsg:
		var wg sync.WaitGroup
		var mu sync.Mutex
		var msgs []tea.Msg
		for _, sub := range msg {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if sub == nil {
					return
				}
				r := sub()
				mu.Lock()
				msgs = append(msgs, r)
				mu.Unlock()
			}()
		}
		wg.Wait()
		for _, r := range msgs {
			m.Update(r)
		}
	case nil:
	default:
		m.Update(msg)
	}
}

func plain(s string) string { return ansi.Strip(s) }

func TestLoadingThenList(t *testing.T) {
	fb := &fakeBackend{prs: []adapter.PR{mkPR(1, "x")}}
	m := New(Config{Backend: fb})
	if !strings.Contains(m.View().Content, "Loading") {
		t.Fatal("expected loading line")
	}
	m.Update(m.Init()())
	if strings.Contains(m.View().Content, "Loading") || !strings.Contains(plain(m.View().Content), "PRJ/repo#1") {
		t.Fatalf("bad view:\n%s", m.View().Content)
	}
}

func TestNavigationBounds(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "up")
	if m.cursor != 0 {
		t.Fatalf("cursor %d", m.cursor)
	}
	send(m, "j", "j", "down", "down", "down", "j")
	if m.cursor != 3 {
		t.Fatalf("cursor %d want 3", m.cursor)
	}
	send(m, "k")
	if m.cursor != 2 {
		t.Fatalf("cursor %d want 2", m.cursor)
	}
}

func TestScrollSmallViewport(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 5}) // header + 2 footer lines = 3 -> body 2
	if m.bodyHeight() != 2 {
		t.Fatalf("body %d", m.bodyHeight())
	}
	send(m, "j", "j", "j")
	if m.top != 2 || m.cursor != 3 {
		t.Fatalf("top=%d cursor=%d", m.top, m.cursor)
	}
	out := plain(m.View().Content)
	if !strings.Contains(out, "#4") || strings.Contains(out, "#1 ") {
		t.Fatalf("viewport wrong:\n%s", out)
	}
	send(m, "k", "k", "k")
	if m.top != 0 {
		t.Fatalf("top=%d", m.top)
	}
}

func TestMarking(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "x")
	if !m.marks[0] || !strings.Contains(plain(m.View().Content), "▌") {
		t.Fatal("row 0 should be marked")
	}
	send(m, "space") // toggles same row off
	if len(m.marks) != 0 {
		t.Fatal("should be unmarked")
	}
	send(m, "ctrl+a")
	if len(m.marks) != 3 || m.marks[2] { // row 3 (index 2) is SKIP
		t.Fatalf("marks=%v", m.marks)
	}
	send(m, "u")
	if len(m.marks) != 0 {
		t.Fatal("u should clear")
	}
	send(m, "ctrl+a", "ctrl+d")
	if len(m.marks) != 0 {
		t.Fatal("ctrl+d should clear")
	}
}

func TestIgnoreToggleAndHidden(t *testing.T) {
	m, _, _, ign := fixture(t)
	send(m, "i") // row 0: npm
	b, _ := os.ReadFile(ign)
	if !strings.Contains(string(b), "npm") {
		t.Fatalf("ignore file: %q", b)
	}
	if !strings.Contains(m.msg, "ignored 'npm'") {
		t.Fatalf("msg %q", m.msg)
	}
	out := plain(m.View().Content)
	if strings.Contains(out, "#1 ") || strings.Contains(out, "#4 ") {
		t.Fatalf("npm rows should be hidden:\n%s", out)
	}
	send(m, ".")
	out = plain(m.View().Content)
	if strings.Count(out, "IGNORED") != 2 {
		t.Fatalf("want 2 IGNORED rows:\n%s", out)
	}
	send(m, "i") // cursor on first row (npm#1) -> un-ignore
	if !strings.Contains(m.msg, "un-ignored 'npm'") {
		t.Fatalf("msg %q", m.msg)
	}
	b, _ = os.ReadFile(ign)
	if strings.Contains(string(b), "npm") {
		t.Fatalf("entry not removed: %q", b)
	}
	if strings.Contains(plain(m.View().Content), "IGNORED") {
		t.Fatal("rows should be back to ELIGIBLE")
	}
}

func TestApproveRefusedWhenNotEligible(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "j", "j", "m") // row 3: major
	if m.mode != modeList || m.msg != "refused: not eligible (major update); M merges anyway" {
		t.Fatalf("msg=%q mode=%v", m.msg, m.mode)
	}
	if len(fb.called) != 0 || len(fb.approved) != 0 {
		t.Fatal("backend must not be called")
	}
}

func TestApproveOnlyOnSkipRow(t *testing.T) {
	m, fb, _, _ := fixture(t)
	c := send(m, "j", "j", "a") // row 3: major (SKIP)
	if m.mode != modeList {
		t.Fatal("a on the cursor row must not ask")
	}
	drain(m, c)
	if len(fb.approved) != 1 || fb.approved[0] != "https://bb/pr/3" || len(fb.called) != 0 {
		t.Fatalf("approved %v called %v", fb.approved, fb.called)
	}
	if m.rows[2].result != "APPROVED" || m.rows[2].locked {
		t.Fatalf("result %q locked=%v", m.rows[2].result, m.rows[2].locked)
	}
}

func TestMergeFollowsApprove(t *testing.T) {
	m, fb, _, _ := fixture(t)
	drain(m, send(m, "a"))
	if m.rows[0].result != "APPROVED" {
		t.Fatalf("result %q", m.rows[0].result)
	}
	drain(m, send(m, "m"))
	if len(fb.called) != 1 || m.rows[0].result != "MERGED" {
		t.Fatalf("called %v result %q", fb.called, m.rows[0].result)
	}
	// a locked row refuses every approve key
	send(m, "a")
	if !strings.HasPrefix(m.msg, "refused") || len(fb.approved) != 1 {
		t.Fatalf("msg %q approved %v", m.msg, fb.approved)
	}
}

func TestApproveRefusedOnIgnoredRow(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "i", ".") // ignore npm, show ignored rows; cursor on #1
	send(m, "a")
	if m.msg != "refused: ignored (i un-ignores)" || len(fb.approved) != 0 {
		t.Fatalf("msg %q approved %v", m.msg, fb.approved)
	}
	send(m, "M")
	if m.mode != modeList || len(fb.called) != 0 {
		t.Fatalf("M must refuse an ignored row: mode=%v msg=%q", m.mode, m.msg)
	}
}

func TestForceMergeOnSkipRow(t *testing.T) {
	m, fb, _, _ := fixture(t)
	c := send(m, "j", "j", "M")
	if c != nil || m.mode != modeConfirm {
		t.Fatalf("M must always ask: mode=%v", m.mode)
	}
	want := "FORCE approve+merge 1 PR(s) overriding PRJ/repo#3: major update? y/N"
	if !strings.Contains(plain(m.View().Content), want) {
		t.Fatalf("want %q in:\n%s", want, plain(m.View().Content))
	}
	if len(fb.called) != 0 {
		t.Fatal("called before confirmation")
	}
	drain(m, send(m, "y"))
	if len(fb.called) != 1 || fb.called[0] != "https://bb/pr/3" || m.rows[2].result != "MERGED" {
		t.Fatalf("called %v result %q", fb.called, m.rows[2].result)
	}
	// n cancels
	m, fb, _, _ = fixture(t)
	send(m, "M", "n")
	if len(fb.called) != 0 || m.mode != modeList {
		t.Fatal("n must cancel a forced merge")
	}
}

func TestApproveMarkedOnlyAsksAndSkips(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "x", "j", "j", "x") // rows 1 and 3 (SKIP)
	if c := send(m, "a"); c != nil || !strings.Contains(plain(m.View().Content), "approve 2 PR(s) (no merge)? y/N") {
		t.Fatalf("prompt:\n%s", plain(m.View().Content))
	}
	drain(m, send(m, "y"))
	sort.Strings(fb.approved)
	if !reflect.DeepEqual(fb.approved, []string{"https://bb/pr/1", "https://bb/pr/3"}) || len(fb.called) != 0 || len(m.marks) != 0 {
		t.Fatalf("approved %v called %v marks %v", fb.approved, fb.called, m.marks)
	}
	// m on marks incl. a SKIP row skips it and says so
	m, fb, _, _ = fixture(t)
	send(m, "x", "j", "j", "x", "m")
	if !strings.Contains(plain(m.View().Content), "approve+merge 1 PR(s)? y/N") {
		t.Fatalf("prompt:\n%s", plain(m.View().Content))
	}
	drain(m, send(m, "y"))
	if len(fb.called) != 1 || fb.called[0] != "https://bb/pr/1" {
		t.Fatalf("called %v", fb.called)
	}
}

func TestApproveMarkedConfirmAndParallel(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/2"] = errors.New("boom\nstderr")
	fb.errs["https://bb/pr/4"] = &PartialError{Err: errors.New("merge conflict")}
	send(m, "ctrl+a")
	c := send(m, "m")
	if c != nil || !strings.Contains(plain(m.View().Content), "approve+merge 3 PR(s)? y/N") {
		t.Fatalf("expected confirm prompt:\n%s", m.View().Content)
	}
	if len(fb.called) != 0 {
		t.Fatal("called before confirmation")
	}
	c = send(m, "y")
	drain(m, c)
	if len(fb.called) != 3 {
		t.Fatalf("called %v", fb.called)
	}
	if fb.maxSeen > 2 {
		t.Fatalf("concurrency exceeded: %d", fb.maxSeen)
	}
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 20})
	out := plain(m.View().Content)
	for _, want := range []string{"MERGED", "FAIL", "boom stderr", "NOT MERGED", "merge conflict"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "SKIP") || strings.Count(strings.Join(strings.Split(out, "\n")[:5], "\n"), "PRJ/repo#") != 4 {
		t.Errorf("rows must stay in list:\n%s", out)
	}
}

func TestApproveCurrentRowInstantly(t *testing.T) {
	m, fb, _, _ := fixture(t)
	c := send(m, "j", "m")
	if m.mode != modeList {
		t.Fatal("m must not ask for confirmation")
	}
	drain(m, c)
	if len(fb.called) != 1 || fb.called[0] != "https://bb/pr/2" {
		t.Fatalf("called %v", fb.called)
	}
}

func TestConfirmCancel(t *testing.T) {
	for _, k := range []string{"n", "esc", "N"} {
		m, fb, _, _ := fixture(t)
		send(m, "x", "m")
		c := send(m, k)
		drain(m, c)
		if len(fb.called) != 0 || m.mode != modeList || len(m.marks) != 1 {
			t.Fatalf("%s: cancel failed called=%v", k, fb.called)
		}
	}
}

func TestOpen(t *testing.T) {
	m, _, opened, _ := fixture(t)
	send(m, "j", "o")
	if len(*opened) != 1 || (*opened)[0] != "https://bb/pr/2" {
		t.Fatalf("opened %v", *opened)
	}
}

func TestQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m, _, _, _ := fixture(t)
		c := send(m, k)
		if c == nil || c() != tea.Quit() {
			t.Fatalf("%s should quit", k)
		}
	}
}

func TestHelpToggle(t *testing.T) {
	m, _, _, _ := fixture(t)
	closed := m.bodyHeight()
	send(m, "?")
	if !strings.Contains(plain(m.View().Content), "ctrl+a mark all") {
		t.Fatal("extended help missing")
	}
	if m.bodyHeight() != closed-len(m.helpArea()) {
		t.Fatalf("body %d should shrink from %d", m.bodyHeight(), closed)
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != 20 {
		t.Fatalf("view is %d lines, want exactly the height", n)
	}
}

func TestResizeRetruncatesAndWidthBound(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.rows[0].pr.Title = "chore(deps): update dependency npm to v1.2.3 " + strings.Repeat("long ", 40)
	for _, w := range []int{120, 60, 30, 12} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		for i, l := range strings.Split(strings.TrimRight(m.View().Content, "\n"), "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Fatalf("width %d: line %d is %d wide: %q", w, i, got, plain(l))
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if !strings.Contains(plain(m.View().Content), "…") {
		t.Fatal("expected ellipsis at width 60")
	}
}

func TestOSC8Link(t *testing.T) {
	m, _, _, _ := fixture(t)
	v := m.View().Content
	if !strings.Contains(v, "\x1b]8;;https://bb/pr/1\x1b\\") || !strings.Contains(v, osc8Close) {
		t.Fatalf("OSC 8 hyperlink missing: %q", v)
	}
	if !strings.Contains(plain(v), "PRJ/repo#1") {
		t.Fatal("ref text missing after strip")
	}
}

func TestLoadError(t *testing.T) {
	m := New(Config{Backend: &fakeBackend{}})
	m.Update(loadedMsg{err: errors.New("nope")})
	if !strings.Contains(m.View().Content, "error: nope") {
		t.Fatal(m.View().Content)
	}
}

// typeText sends each rune of s as its own key (spaces as KeySpace), like a real terminal.
func typeText(m *Model, s string) {
	for _, r := range s {
		if r == ' ' {
			send(m, "space")
		} else {
			send(m, string(r))
		}
	}
}

func TestDeclineFlow(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string // before typing
		text    string
		after   []string
		want    []call
		wantMsg string
	}{
		{"marked rows incl SKIP with comment", []string{"x", "j", "j", "x", "d"}, "no longer needed", []string{"enter"},
			[]call{{"https://bb/pr/1", "no longer needed"}, {"https://bb/pr/3", "no longer needed"}}, ""},
		{"empty comment is refused", []string{"x", "d"}, "", []string{"enter"},
			nil, "refused: a decline needs a comment"},
		{"blank comment is refused", []string{"d"}, "  ", []string{"enter"},
			nil, "refused: a decline needs a comment"},
		{"cursor row with d", []string{"j", "j", "d"}, "x", []string{"enter"},
			[]call{{"https://bb/pr/3", "x"}}, ""},
		{"esc at comment prompt", []string{"x", "d"}, "abc", []string{"esc"}, nil, "cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, fb, _, _ := fixture(t)
			send(m, tc.keys...)
			typeText(m, tc.text)
			c := send(m, tc.after...)
			drain(m, c)
			if m.mode != modeList {
				t.Fatalf("mode %v", m.mode)
			}
			sort.Slice(fb.declined, func(i, j int) bool { return fb.declined[i].url < fb.declined[j].url })
			if !reflect.DeepEqual(fb.declined, tc.want) {
				t.Fatalf("declined %v want %v", fb.declined, tc.want)
			}
			if tc.wantMsg != "" && m.msg != tc.wantMsg {
				t.Fatalf("msg %q want %q", m.msg, tc.wantMsg)
			}
			if len(fb.comments) != 0 || len(fb.called) != 0 {
				t.Fatal("unexpected other backend calls")
			}
		})
	}
}

func TestDeclinePromptsAndStatuses(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/2"] = errors.New("403\nforbidden")
	send(m, "x", "j", "x", "d")
	if m.mode != modeInput {
		t.Fatalf("d must go straight to the text prompt, mode %v", m.mode)
	}
	typeText(m, "gone")
	if !strings.Contains(plain(m.View().Content), "decline 2 PR(s), reason (enter = decline, esc = cancel): gone█") {
		t.Fatalf("comment prompt:\n%s", m.View().Content)
	}
	c := send(m, "enter")
	drain(m, c)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 20})
	lines := strings.Split(plain(m.View().Content), "\n")
	if !strings.Contains(lines[1], "DECLINED") || !strings.Contains(lines[2], "FAIL") || !strings.Contains(lines[2], "403 forbidden") {
		t.Fatalf("statuses:\n%s", strings.Join(lines, "\n"))
	}
	if len(m.marks) != 0 {
		t.Fatal("marks should be cleared")
	}
	if fb.maxSeen > 2 {
		t.Fatal("concurrency")
	}
}

func TestCommentFlow(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/3"] = errors.New("nope")
	send(m, "x", "j", "j", "x", "c")
	if !strings.Contains(plain(m.View().Content), "comment on 2 PR(s) (enter = send, esc = cancel): █") {
		t.Fatalf("prompt:\n%s", m.View().Content)
	}
	typeText(m, "hi there")
	before := plain(m.View().Content)
	drain(m, send(m, "enter"))
	sort.Slice(fb.comments, func(i, j int) bool { return fb.comments[i].url < fb.comments[j].url })
	want := []call{{"https://bb/pr/1", "hi there"}, {"https://bb/pr/3", "hi there"}}
	if !reflect.DeepEqual(fb.comments, want) {
		t.Fatalf("comments %v", fb.comments)
	}
	if !strings.Contains(before, "hi there█") {
		t.Fatalf("input not shown:\n%s", before)
	}
	if !strings.Contains(m.msg, "COMMENTED on 1") || !strings.Contains(m.msg, "FAIL 1") || !strings.Contains(m.msg, "nope") {
		t.Fatalf("msg %q", m.msg)
	}
	// The result now shows on the row: row 1 COMMENTED, row 3 FAIL; no declines, and a comment never locks.
	ls := lines(m)
	if !strings.HasPrefix(ls[1], "  COMMENTED ") || !strings.HasPrefix(ls[3], "› FAIL ") || len(fb.declined) != 0 {
		t.Fatalf("state:\n%s", strings.Join(ls, "\n"))
	}
	if m.rows[0].result != "COMMENTED" || m.rows[2].result != "FAIL" || m.rows[0].locked || m.rows[2].locked {
		t.Fatalf("row results %q %q", m.rows[0].result, m.rows[2].result)
	}
	if !m.eligible(0) {
		t.Fatal("a COMMENTED row must stay eligible")
	}
	drain(m, send(m, "k", "k", "m")) // a later action replaces COMMENTED
	if m.rows[0].result != "MERGED" {
		t.Fatalf("result %q", m.rows[0].result)
	}
}

func TestCommentRefusesEmptyAndEsc(t *testing.T) {
	m, fb, _, _ := fixture(t)
	drain(m, send(m, "c", "space", "space", "enter"))
	if !strings.Contains(m.msg, "empty comment") || m.mode != modeList || len(fb.comments) != 0 {
		t.Fatalf("msg=%q mode=%v calls=%v", m.msg, m.mode, fb.comments)
	}
	send(m, "c")
	typeText(m, "abc")
	drain(m, send(m, "esc"))
	if len(fb.comments) != 0 || m.mode != modeList {
		t.Fatal("esc must cancel")
	}
}

func TestPromptSwallowsActionKeys(t *testing.T) {
	for _, kind := range []string{"c", "d"} {
		m, fb, _, ign := fixture(t)
		send(m, kind)
		typeText(m, "aiq x.o?jX")
		if m.mode != modeInput || string(m.input) != "aiq x.o?jX" {
			t.Fatalf("%s: mode=%v input=%q", kind, m.mode, string(m.input))
		}
		send(m, "backspace", "backspace")
		if string(m.input) != "aiq x.o?" {
			t.Fatalf("backspace: %q", string(m.input))
		}
		send(m, "ctrl+u")
		if len(m.input) != 0 {
			t.Fatal("ctrl+u should clear")
		}
		if len(fb.called)+len(fb.declined)+len(fb.comments) != 0 || len(m.marks) != 0 || m.cursor != 0 || m.showIgnored {
			t.Fatalf("%s: a list action fired", kind)
		}
		if _, err := os.Stat(ign); err == nil {
			t.Fatalf("%s: i wrote the ignore file", kind)
		}
	}
}

func TestHeaderAndWidthBound(t *testing.T) {
	m, _, _, _ := fixture(t)
	first := strings.Split(plain(m.View().Content), "\n")[0]
	if !strings.HasPrefix(first, "  STATUS") || !strings.Contains(first, "TITLE") {
		t.Fatalf("header missing: %q", first)
	}
	m.rows[0].pr.Title += strings.Repeat(" long", 40)
	send(m, "d")
	typeText(m, strings.Repeat("comment ", 30))
	for _, w := range []int{20, 40, 100} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 12})
		if strings.HasSuffix(m.View().Content, "\n") {
			t.Fatal("View must not end with a newline")
		}
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > 12 {
			t.Fatalf("w=%d: %d lines > height", w, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Fatalf("w=%d: too wide: %q", w, plain(l))
			}
		}
	}
	if !strings.Contains(plain(m.View().Content), "█") {
		t.Fatal("cursor must stay visible on a long input")
	}
}

func TestIgnoreUnmarksAndCounter(t *testing.T) {
	m, _, _, _ := fixture(t)
	send(m, "ctrl+a")
	if len(m.marks) != 3 {
		t.Fatalf("marks %v", m.marks)
	}
	send(m, "i") // ignore npm: rows 1 and 4 become hidden IGNORED
	if m.marks[0] || m.marks[3] || len(m.marks) != 1 {
		t.Fatalf("hidden rows must be un-marked: %v", m.marks)
	}
	send(m, "u")
	if !strings.Contains(plain(m.View().Content), "3 PRs, 0 marked") && !strings.Contains(plain(m.View().Content), "2 PRs, 0 marked") {
		t.Fatalf("view:\n%s", m.View().Content)
	}
	send(m, "ctrl+a")
	if m.marks[0] || m.marks[3] || len(m.marks) != 1 || m.msg != "1 marked" {
		t.Fatalf("ctrl+a must skip ignored rows: %v %q", m.marks, m.msg)
	}
	// decline targets never include hidden IGNORED rows
	m.marks[0] = true // simulate a stale mark
	if got := m.markedTargets(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("targets %v", got)
	}
}

func TestFooterShowsVersion(t *testing.T) {
	m, _, _, _ := fixture(t)
	if strings.Contains(plain(m.View().Content), "bbapprove ") {
		t.Fatal("no version expected when unset")
	}
	m.cfg.Version = "v1.0.0-rc.1"
	if !strings.Contains(plain(m.View().Content), "bbapprove v1.0.0-rc.1") {
		t.Fatal("footer missing version")
	}
	send(m, "?")
	if !strings.Contains(plain(m.View().Content), "bbapprove v1.0.0-rc.1") {
		t.Fatal("extended help missing version")
	}
}

func TestIgnoreOnSkipRowWinsAndRestores(t *testing.T) {
	m, _, _, ign := fixture(t)
	send(m, "down", "down") // row 3: react to v19 (major, SKIP)
	if w, _, why := m.status(m.rows[2]); w != "SKIP" || why != "major update" {
		t.Fatalf("before: %q %q", w, why)
	}
	send(m, "i")
	b, _ := os.ReadFile(ign)
	if !strings.Contains(string(b), "react") {
		t.Fatalf("ignore file: %q", b)
	}
	if w, _, _ := m.status(m.rows[2]); w != wIgnored {
		t.Fatalf("SKIP row must become IGNORED, got %q", w)
	}
	if strings.Contains(plain(m.View().Content), "#3 ") {
		t.Fatalf("ignored SKIP row must be hidden:\n%s", m.View().Content)
	}
	send(m, "h")
	if !strings.Contains(plain(m.View().Content), "IGNORED") {
		t.Fatalf("h must show it:\n%s", m.View().Content)
	}
	for m.rows[m.cursor].pr.Number != 3 {
		send(m, "down")
	}
	send(m, "i")
	if w, _, why := m.status(m.rows[2]); w != "SKIP" || why != "major update" {
		t.Fatalf("un-ignore must restore the original reason: %q %q", w, why)
	}
}

func TestViewDeclaresTerminalFeatures(t *testing.T) {
	m, _, _, _ := fixture(t)
	v := m.View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeAllMotion || v.WindowTitle != "bbapprove" {
		t.Fatalf("view fields: alt=%v mouse=%v title=%q", v.AltScreen, v.MouseMode, v.WindowTitle)
	}
	m.cfg.NoMouse = true
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("NoMouse must not request mouse reporting")
	}
}
