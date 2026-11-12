package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func helpWords() map[string]bool {
	w := map[string]bool{}
	add := func(s string) {
		for _, f := range strings.Fields(s) {
			w[f] = true
		}
	}
	for _, sec := range helpSections {
		add(sec.title)
		add(sec.note)
		for _, it := range sec.items {
			add(it[0])
			add(it[1])
		}
	}
	return w
}

func TestHelpWrapsWithoutLosingWords(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 120, 200} {
		m, _, _, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 80})
		send(m, "?")
		lines := m.helpArea()
		got := map[string]bool{}
		for _, l := range lines {
			if strings.Contains(l, "…") {
				t.Errorf("width %d: ellipsis in %q", width, l)
			}
			if w := ansi.StringWidth(l); w > width || w > helpMaxWidth {
				t.Errorf("width %d: help line is %d wide: %q", width, w, plain(l))
			}
			for _, f := range strings.Fields(plain(l)) {
				got[f] = true
			}
		}
		for w := range helpWords() {
			if !got[w] {
				t.Errorf("width %d: word %q missing from help", width, w)
			}
		}
		if m.footerHeight() != 2 {
			t.Errorf("width %d: footerHeight %d", width, m.footerHeight())
		}
		for i, l := range strings.Split(m.View().Content, "\n") {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: view line %d is %d wide", width, i, w)
			}
		}
		if n := len(strings.Split(m.View().Content, "\n")); n != 80 {
			t.Errorf("width %d: view is %d lines, want 80", width, n)
		}
	}
}

func TestHelpLayoutNarrowAndWide(t *testing.T) {
	for _, tc := range []struct{ width, wantKeyCol int }{{60, 2 + helpKeyCol}, {200, 2 + helpKeyCol}} {
		m, _, _, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 70})
		send(m, "?")
		var lines []string
		for _, l := range m.helpArea() {
			lines = append(lines, plain(l))
		}
		text := strings.Join(lines, "\n")
		for _, title := range []string{"Move", "Mark", "Act", "Refresh", "Mouse", "Hints"} {
			if !strings.Contains("\n"+text, "\n"+title) {
				t.Errorf("width %d: section %q missing", tc.width, title)
			}
		}
		// One key per line: the key starts the line and its description starts at the key column.
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, "  M ") {
				found = true
				if idx := strings.Index(l, "force"); idx != tc.wantKeyCol {
					t.Errorf("width %d: description at column %d, want %d: %q", tc.width, idx, tc.wantKeyCol, l)
				}
			}
		}
		if !found {
			t.Errorf("width %d: no line for key M", tc.width)
		}
		if tc.width == 200 {
			for _, l := range lines {
				if ansi.StringWidth(l) > helpMaxWidth {
					t.Errorf("wide help line is %d columns: %q", ansi.StringWidth(l), l)
				}
			}
		}
		// The footer is the normal two-line status bar, with no help text mixed in.
		view := strings.Split(plain(m.View().Content), "\n")
		if !strings.Contains(view[len(view)-1], "PRs") || strings.Contains(view[len(view)-1], "Hints") {
			t.Errorf("width %d: last line is not the plain hint line: %q", tc.width, view[len(view)-1])
		}
	}
	// A terminal too narrow for two columns stacks the description under the key.
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 90})
	send(m, "?")
	for _, l := range m.helpArea() {
		if ansi.StringWidth(l) > 30 {
			t.Errorf("narrow help line is too wide: %q", plain(l))
		}
	}
}

func TestHelpFitsShortTerminal(t *testing.T) {
	for _, h := range []int{10, 20} {
		for _, width := range []int{60, 100} {
			m, _, _, _ := fixture(t)
			m.Update(tea.WindowSizeMsg{Width: width, Height: h})
			send(m, "?")
			if n := len(strings.Split(m.View().Content, "\n")); n != h {
				t.Errorf("width %d height %d: view is %d lines", width, h, n)
			}
			if got := plain(m.View().Content); !strings.Contains(got, "taller") {
				t.Errorf("width %d height %d: no hint that help is clipped", width, h)
			}
		}
	}
}

func TestHelpToggleTwiceRestoresLayout(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	before, h := m.View().Content, m.footerHeight()
	send(m, "?")
	if m.View().Content == before {
		t.Fatal("help did not change the view")
	}
	_ = h
	send(m, "?")
	if m.footerHeight() != h || m.View().Content != before {
		t.Error("toggling ? twice must restore the layout")
	}
}

func TestHelpPanelIgnoresRowClicks(t *testing.T) {
	m, _, opened, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	send(m, "?")
	m.Update(tea.MouseClickMsg{X: 10, Y: 2, Button: tea.MouseLeft})
	if len(m.marks) != 0 || len(*opened) != 0 {
		t.Fatal("click on the help panel must do nothing")
	}
	// The URL line stays second to last.
	m.Update(tea.MouseClickMsg{X: 1, Y: m.height - 2, Button: tea.MouseLeft})
	if len(*opened) != 1 {
		t.Fatalf("URL click opened %v", *opened)
	}
}
