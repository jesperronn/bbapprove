package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
}

func (f *fakeBackend) List(context.Context) ([]adapter.PR, error) { return f.prs, nil }
func (f *fakeBackend) ApproveAndMerge(_ context.Context, pr adapter.PR) error {
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
	return f.errs[pr.URL]
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

func key(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
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
	if !strings.Contains(m.View(), "Loading") {
		t.Fatal("expected loading line")
	}
	m.Update(m.Init()())
	if strings.Contains(m.View(), "Loading") || !strings.Contains(plain(m.View()), "PRJ/repo#1") {
		t.Fatalf("bad view:\n%s", m.View())
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
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 6}) // header+status+help = 3 -> body 3... help 1 line
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 5})
	if m.bodyHeight() != 2 {
		t.Fatalf("body %d", m.bodyHeight())
	}
	send(m, "j", "j", "j")
	if m.top != 2 || m.cursor != 3 {
		t.Fatalf("top=%d cursor=%d", m.top, m.cursor)
	}
	out := plain(m.View())
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
	if !m.marks[0] || !strings.Contains(plain(m.View()), "▌") {
		t.Fatal("row 0 should be marked")
	}
	send(m, "space") // toggles same row off
	if len(m.marks) != 0 {
		t.Fatal("should be unmarked")
	}
	send(m, "X")
	if len(m.marks) != 3 || m.marks[2] { // row 3 (index 2) is SKIP
		t.Fatalf("marks=%v", m.marks)
	}
	send(m, "u")
	if len(m.marks) != 0 {
		t.Fatal("u should clear")
	}
	send(m, "X", "ctrl+d")
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
	out := plain(m.View())
	if strings.Contains(out, "#1 ") || strings.Contains(out, "#4 ") {
		t.Fatalf("npm rows should be hidden:\n%s", out)
	}
	send(m, ".")
	out = plain(m.View())
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
	if strings.Contains(plain(m.View()), "IGNORED") {
		t.Fatal("rows should be back to ELIGIBLE")
	}
}

func TestApproveRefusedWhenNotEligible(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "j", "j", "a") // row 3: major
	if m.mode != modeList || !strings.Contains(m.msg, "refused") {
		t.Fatalf("msg=%q mode=%v", m.msg, m.mode)
	}
	if len(fb.called) != 0 {
		t.Fatal("backend must not be called")
	}
}

func TestApproveMarkedConfirmAndParallel(t *testing.T) {
	m, fb, _, _ := fixture(t)
	fb.errs["https://bb/pr/2"] = errors.New("boom\nstderr")
	fb.errs["https://bb/pr/4"] = &PartialError{Err: errors.New("merge conflict")}
	send(m, "X")
	c := send(m, "a")
	if c != nil || !strings.Contains(plain(m.View()), "approve+merge 3 PR(s)? y/N") {
		t.Fatalf("expected confirm prompt:\n%s", m.View())
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
	out := plain(m.View())
	for _, want := range []string{"MERGED", "FAIL", "boom stderr", "APPROVED, NOT MERGED", "merge conflict"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "SKIP") || strings.Count(out, "PRJ/repo#") != 4 {
		t.Errorf("rows must stay in list:\n%s", out)
	}
}

func TestApproveCurrentRowWhenNoMarks(t *testing.T) {
	m, fb, _, _ := fixture(t)
	send(m, "j", "a")
	drain(m, send(m, "y"))
	if len(fb.called) != 1 || fb.called[0] != "https://bb/pr/2" {
		t.Fatalf("called %v", fb.called)
	}
}

func TestConfirmCancel(t *testing.T) {
	for _, k := range []string{"n", "esc", "N"} {
		m, fb, _, _ := fixture(t)
		send(m, "x", "a")
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
	before := len(strings.Split(m.View(), "\n"))
	send(m, "?")
	if len(strings.Split(m.View(), "\n")) <= before-1 && !strings.Contains(plain(m.View()), "show/hide ignored") {
		t.Fatal("extended help missing")
	}
	if m.bodyHeight() >= 20-3 {
		t.Fatal("body should shrink")
	}
}

func TestResizeRetruncatesAndWidthBound(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.rows[0].pr.Title = "chore(deps): update dependency npm to v1.2.3 " + strings.Repeat("long ", 40)
	for _, w := range []int{120, 60, 30, 12} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		for i, l := range strings.Split(strings.TrimRight(m.View(), "\n"), "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Fatalf("width %d: line %d is %d wide: %q", w, i, got, plain(l))
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if !strings.Contains(plain(m.View()), "…") {
		t.Fatal("expected ellipsis at width 60")
	}
}

func TestOSC8Link(t *testing.T) {
	m, _, _, _ := fixture(t)
	v := m.View()
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
	if !strings.Contains(m.View(), "error: nope") {
		t.Fatal(m.View())
	}
}
