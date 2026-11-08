package app

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
)

func mk() adapter.PR {
	return adapter.PR{Project: "PRJ", Repo: "repo", Number: 7, Title: "chore(deps): update foo", Build: "passed", QualityGate: "unknown"}
}

func TestGlyphs(t *testing.T) {
	st := Style{}
	tests := []struct {
		name string
		mod  func(*adapter.PR)
		want string // glyph cells for build/sonar/approval
	}{
		{"passed unknown none", func(p *adapter.PR) {}, "✓  ?  –  "},
		{"failed passed approved", func(p *adapter.PR) { p.Build, p.QualityGate, p.Approvals = "failed", "passed", 1 }, "✗  ✓  ✓  "},
		{"running failed needswork", func(p *adapter.PR) { p.Build, p.QualityGate, p.NeedsWork, p.Approvals = "running", "failed", 1, 1 }, "…  ✗  ✗  "},
		{"none/empty fields", func(p *adapter.PR) { p.Build, p.QualityGate = "", "" }, "?  ?  –  "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := mk()
			tc.mod(&p)
			row := st.Row("", "", p, "")
			if !strings.HasPrefix(row, strings.Repeat(" ", 13)+tc.want) {
				t.Fatalf("row %q lacks glyphs %q", row, tc.want)
			}
		})
	}
}

func TestRowLayoutAndSkipReason(t *testing.T) {
	p := mk()
	p.CommentCount, p.OpenTasks = 12, 0
	row := Style{}.Row("SKIP", yellow, p, "major update")
	want := "SKIP         ✓  ?  –   12   0 PRJ/repo#7 chore(deps): update foo — major update"
	if row != want {
		t.Fatalf("got  %q\nwant %q", row, want)
	}
	if h := (Style{}).Header(); h != "STATUS       🔨 🔎 👍 💬  ✅  PR   TITLE" {
		t.Fatalf("header %q", h)
	}
}

func TestColor(t *testing.T) {
	p := mk()
	p.CommentCount, p.OpenTasks = 2, 3
	row := Style{Color: true}.Row("MERGED", green, p, "")
	for _, s := range []string{green + "MERGED", green + "✓", dim + "?", bold + cyan + "  2", yellow + "  3", bold + "#7"} {
		if !strings.Contains(row, s) {
			t.Errorf("missing %q in %q", s, row)
		}
	}
	if plain := (Style{}).Row("MERGED", green, p, ""); strings.Contains(plain, "\033") {
		t.Fatalf("escape codes without color: %q", plain)
	}
}

func TestTruncation(t *testing.T) {
	p := mk()
	p.Title = "chore(deps): update 日本語日本語日本語 very long title indeed"
	for _, color := range []bool{false, true} {
		for _, w := range []int{20, 40, 47, 60} {
			row := Style{Color: color, Width: w}.Row("SKIP", yellow, p, "major update")
			if got := ansi.StringWidth(row); got > w {
				t.Errorf("color=%v width %d: row width %d: %q", color, w, got, row)
			}
		}
	}
	// short rows untouched
	short := mk()
	a, b := Style{Width: 500}.Row("SKIP", yellow, short, ""), Style{}.Row("SKIP", yellow, short, "")
	if a != b {
		t.Fatalf("short row changed: %q vs %q", a, b)
	}
	// wide runes are never cut in half
	row := Style{Width: 45}.Row("SKIP", yellow, p, "")
	if strings.ContainsRune(row, '�') || !regexp.MustCompile(`…$`).MatchString(row) {
		t.Fatalf("bad truncation %q", row)
	}
}
