package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func helpWords() map[string]bool {
	w := map[string]bool{}
	for _, p := range helpParagraphs {
		for _, f := range strings.Fields(p.text) {
			w[f] = true
		}
	}
	return w
}

func TestHelpWrapsWithoutLosingWords(t *testing.T) {
	for _, width := range []int{60, 80, 100, 120} {
		m, _, _, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 60})
		send(m, "?")
		lines := m.helpArea()
		got := map[string]bool{}
		for _, l := range lines {
			if strings.Contains(l, "…") {
				t.Errorf("width %d: ellipsis in %q", width, l)
			}
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: help line is %d wide: %q", width, w, l)
			}
			for _, f := range strings.Fields(l) {
				got[f] = true
			}
		}
		for w := range helpWords() {
			if !got[w] {
				t.Errorf("width %d: word %q missing from help", width, w)
			}
		}
		if m.footerHeight() != len(lines)+2 {
			t.Errorf("width %d: footerHeight %d, help lines %d", width, m.footerHeight(), len(lines))
		}
		for i, l := range strings.Split(m.View().Content, "\n") {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: view line %d is %d wide", width, i, w)
			}
		}
		if n := len(strings.Split(m.View().Content, "\n")); n != 60 {
			t.Errorf("width %d: view is %d lines, want 60", width, n)
		}
	}
}

func TestHelpKeepsListRowsOnShortTerminal(t *testing.T) {
	for _, width := range []int{60, 100} {
		m, _, _, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		send(m, "?")
		if m.bodyHeight() < minListRows {
			t.Errorf("width %d: only %d list rows", width, m.bodyHeight())
		}
		if n := len(strings.Split(m.View().Content, "\n")); n != 20 {
			t.Errorf("width %d: view is %d lines, want 20", width, n)
		}
	}
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	send(m, "?")
	if m.bodyHeight() < minListRows || m.footerHeight() != len(m.helpArea())+2 {
		t.Errorf("height 10: body %d footer %d", m.bodyHeight(), m.footerHeight())
	}
}

func TestHelpToggleTwiceRestoresLayout(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	before, h := m.View().Content, m.footerHeight()
	send(m, "?")
	if m.footerHeight() == h {
		t.Fatal("help did not change the footer")
	}
	send(m, "?")
	if m.footerHeight() != h || m.View().Content != before {
		t.Error("toggling ? twice must restore the layout")
	}
}

func TestHelpFooterClicksFollowWrappedHeight(t *testing.T) {
	m, _, opened, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	send(m, "?")
	// A click on a help line is not a row click.
	m.Update(tea.MouseClickMsg{X: 10, Y: m.height - 3, Button: tea.MouseLeft})
	if len(m.marks) != 0 || len(*opened) != 0 {
		t.Fatal("click on the help area must do nothing")
	}
	// The URL line stays second to last.
	m.Update(tea.MouseClickMsg{X: 1, Y: m.height - 2, Button: tea.MouseLeft})
	if len(*opened) != 1 {
		t.Fatalf("URL click opened %v", *opened)
	}
	// A click just below the last list row is not a row either.
	if m.rowAt(1+m.bodyHeight()) != -1 {
		t.Error("row hit-test reaches into the help area")
	}
}
