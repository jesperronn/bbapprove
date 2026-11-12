package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const longURL = "https://bitbucket.example.com/projects/PROJECT/repos/some-long-repository-name/pull-requests/4961"

var osc8Re = regexp.MustCompile("\x1b\\]8;;([^\x1b]*)\x1b\\\\")

func footerOne(m *Model) string {
	ls := strings.Split(m.View().Content, "\n")
	return ls[len(ls)-2]
}

func urlFixture(t *testing.T, url string) (*Model, *[]string) {
	t.Helper()
	m, _, opened, _ := fixture(t)
	m.cfg.Version = "v1.0.0-rc.1"
	m.rows[0].pr.URL = url
	return m, opened
}

func TestFooterURLKeepsTailAndOpenHint(t *testing.T) {
	m, _ := urlFixture(t, longURL)
	for _, w := range []int{120, 80, 60, 40, 30} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		raw := footerOne(m)
		p := plain(raw)
		if ansi.StringWidth(raw) > w {
			t.Fatalf("w=%d too wide (%d): %q", w, ansi.StringWidth(raw), p)
		}
		if !strings.Contains(p, "  o open") {
			t.Fatalf("w=%d lacks hint: %q", w, p)
		}
		if !strings.HasPrefix(p, "https://") {
			t.Fatalf("w=%d lacks URL start: %q", w, p)
		}
		if !strings.Contains(p, "4961  o open") {
			t.Fatalf("w=%d URL end must precede the hint: %q", w, p)
		}
		if w >= 40 && !strings.Contains(p, "/pull-requests/4961") {
			t.Fatalf("w=%d lacks the PR path tail: %q", w, p)
		}
		if !strings.Contains(p, "…") {
			t.Fatalf("w=%d: middle ellipsis expected: %q", w, p)
		}
		// The ellipsis is in the middle: text remains on both sides of it.
		if i := strings.Index(p, "…"); i >= 0 && (i < 8 || strings.Index(p, "4961") < i) {
			t.Fatalf("w=%d: ellipsis not in the middle: %q", w, p)
		}
		if got := osc8Re.FindStringSubmatch(raw); got == nil || got[1] != longURL {
			t.Fatalf("w=%d: hyperlink target must be the full URL: %q", w, raw)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 20})
	p := plain(footerOne(m))
	if !strings.HasPrefix(p, longURL+"  o open") || !strings.HasSuffix(p, "bbapprove v1.0.0-rc.1") || ansi.StringWidth(p) != 150 {
		t.Fatalf("w=120 shows everything: %q", p)
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	if p := plain(footerOne(m)); strings.Contains(p, "bbapprove") || ansi.StringWidth(p) != 30 {
		t.Fatalf("w=30 drops the version, pads exactly: %q", p)
	}
}

func TestFooterURLEveryWidth(t *testing.T) {
	m, _ := urlFixture(t, longURL)
	for w := 1; w <= 130; w++ {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		for _, l := range strings.Split(m.View().Content, "\n") {
			if ansi.StringWidth(l) > w {
				t.Fatalf("w=%d too wide: %q", w, plain(l))
			}
		}
		if w >= 20 && !strings.Contains(plain(footerOne(m)), "o open") {
			t.Fatalf("w=%d lost the hint: %q", w, plain(footerOne(m)))
		}
	}
}

func TestFooterHyperlinkSurvivesNoColor(t *testing.T) {
	m, _ := urlFixture(t, longURL)
	m.cfg.NoColor = true
	raw := footerOne(m)
	if got := osc8Re.FindStringSubmatch(raw); got == nil || got[1] != longURL || strings.Contains(raw, "\x1b[") {
		t.Fatalf("no-colour footer: %q", raw)
	}
}

func TestFooterMessageAndPromptReplaceURL(t *testing.T) {
	m, opened := urlFixture(t, longURL)
	send(m, "o")
	if p := plain(footerOne(m)); !strings.HasPrefix(p, "opened "+longURL[:20]) || strings.Contains(p, "o open") {
		t.Fatalf("message: %q", p)
	}
	n := len(*opened)
	m.Update(click(3, 18))
	if len(*opened) != n {
		t.Fatal("a click on a message must not open")
	}
}

func TestFooterClickOpensURL(t *testing.T) {
	for _, w := range []int{100, 50} {
		m, opened := urlFixture(t, longURL)
		m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		y := 18
		m.Update(click(5, y))
		if len(*opened) != 1 || (*opened)[0] != longURL || m.msg != "opened "+longURL {
			t.Fatalf("w=%d: opened %v msg %q", w, *opened, m.msg)
		}
	}
	// the hint is part of the click area; the blank part of the line is not
	m, opened := urlFixture(t, "https://bb/pr/1")
	m.Update(click(15+4, 18)) // "o open" hint
	if len(*opened) != 1 {
		t.Fatalf("hint click: %v", *opened)
	}
	m.msg = ""
	m.Update(click(15+8, 18)) // first blank column
	m.Update(click(60, 18))   // blank
	m.Update(click(95, 18))   // version
	m.Update(click(5, 17))    // hint line below
	m.Update(click(5, 16))    // body above
	m.Update(tea.MouseMotionMsg{X: 3, Y: 18})
	m.Update(tea.MouseClickMsg{X: 3, Y: 18, Button: tea.MouseRight})
	if len(*opened) != 1 {
		t.Fatalf("clicks outside the URL must do nothing: %v", *opened)
	}
	send(m, "o")
	if len(*opened) != 2 {
		t.Fatal("o still works")
	}
}

func TestFooterClickNeedsListModeAndMouse(t *testing.T) {
	m, opened := urlFixture(t, longURL)
	send(m, "c")
	m.Update(click(5, 18))
	if len(*opened) != 0 {
		t.Fatal("click during a prompt must do nothing")
	}
	m, opened = urlFixture(t, longURL)
	m.cfg.NoMouse = true
	m.Update(click(5, 18))
	if len(*opened) != 0 {
		t.Fatal("NoMouse")
	}
}

func TestFooterClickReportsOpenerError(t *testing.T) {
	m, _ := urlFixture(t, longURL)
	m.cfg.Opener = func(string) error { return errTest("no browser") }
	m.Update(click(5, 18))
	if m.msg != "open failed: no browser" {
		t.Fatalf("msg %q", m.msg)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestHelpMentionsFooterClick(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 60})
	send(m, "?")
	if out := plain(m.View().Content); !strings.Contains(out, "open it (the URL at the bottom)") {
		t.Fatalf("help:\n%s", out)
	}
}

// hintItemsAt lists the hint keys (as "key word") shown on the hint line at width w.
func hintKeysAt(m *Model, w int, sets [][2]string) map[string]bool {
	m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
	h := plain(hintOf(m))
	got := map[string]bool{}
	for _, it := range sets {
		if strings.Contains(h, it[0]+" "+it[1]) {
			got[it[0]] = true
		}
	}
	return got
}

func TestOpenHintDroppedLast(t *testing.T) {
	for name, tc := range map[string]struct {
		shift bool
		set   [][2]string
	}{"instant": {false, hintItems}, "shift": {true, shiftHintItems}} {
		m, _, _, _ := fixture(t)
		supported(m)
		if tc.shift {
			m.Update(shiftDown(tea.KeyLeftShift))
		}
		if !strings.Contains(plain(hintOf(m)), "o open") {
			t.Fatalf("%s set lacks o open", name)
		}
		// o must be kept whenever any other item besides the counts is kept, and ? / q go first.
		for w := 140; w >= 20; w-- {
			k := hintKeysAt(m, w, tc.set)
			for key := range k {
				if key != "o" && !k["o"] {
					t.Fatalf("%s w=%d: %q kept but o open dropped: %q", name, w, key, plain(hintOf(m)))
				}
			}
			if (k["?"] || k["q"]) && (!k["o"] || !k[tc.set[0][0]] || !k["x"]) {
				t.Fatalf("%s w=%d: ?/q kept before the others: %q", name, w, plain(hintOf(m)))
			}
		}
		if k := hintKeysAt(m, 26, tc.set); !k["o"] || k["q"] || k["?"] {
			t.Fatalf("%s w=26: %v %q", name, k, plain(hintOf(m)))
		}
	}
}
