package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
)

// Style controls colors and truncation.
type Style struct {
	Color bool
	Width int // 0 = no truncation
}

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

func (s Style) c(code, text string) string {
	if !s.Color || code == "" {
		return text
	}
	return code + text + reset
}

// Header is the column header row.
func (s Style) Header() string {
	return s.fit(s.c(dim, "STATUS       🔨 🔎 👍 💬  ✅  PR   TITLE"))
}

func (s Style) cell(code, glyph string) string { return s.c(code, glyph) + "  " }

func (s Style) buildGlyph(b string) string {
	switch b {
	case "passed":
		return s.cell(green, "✓")
	case "failed":
		return s.cell(red, "✗")
	case "running":
		return s.cell(yellow, "…")
	}
	return s.cell(dim, "?")
}

func (s Style) sonarGlyph(q string) string {
	switch q {
	case "passed":
		return s.cell(green, "✓")
	case "failed":
		return s.cell(red, "✗")
	}
	return s.cell(dim, "?")
}

func (s Style) approvalGlyph(pr adapter.PR) string {
	switch {
	case pr.NeedsWork > 0:
		return s.cell(red, "✗")
	case pr.Approvals > 0:
		return s.cell(green, "✓")
	}
	return s.cell(dim, "–")
}

func (s Style) count(n int, on string) string {
	txt := fmt.Sprintf("%3d", n)
	if n > 0 {
		return s.c(on, txt) + " "
	}
	return s.c(dim, txt) + " "
}

// Ref renders PROJECT/repo#id.
func (s Style) Ref(pr adapter.PR) string {
	return s.c(dim+cyan, pr.Project) + s.c(dim, "/") + s.c(cyan, pr.Repo) + s.c(bold, fmt.Sprintf("#%d", pr.Number))
}

// StatusWidth is the width of the status column (fits "COMMENTING…").
const StatusWidth = 11

// Status renders a status word (blank = empty cell) padded to StatusWidth columns plus a 2-column gap.
func (s Style) Status(word, code string) string {
	pad := StatusWidth - ansi.StringWidth(word)
	if pad < 0 {
		pad = 0
	}
	return s.c(code, word) + strings.Repeat(" ", pad+2)
}

// Row renders a full table row; reason (if non-empty) is appended dim after the title. Truncated to Width.
func (s Style) Row(word, code string, pr adapter.PR, reason string) string {
	return s.fit(s.rowFull(word, code, pr, reason))
}

func (s Style) rowFull(word, code string, pr adapter.PR, reason string) string {
	line := s.Status(word, code) + s.buildGlyph(pr.Build) + s.sonarGlyph(pr.QualityGate) + s.approvalGlyph(pr) +
		s.count(pr.CommentCount, bold+cyan) + s.count(pr.OpenTasks, yellow) + s.Ref(pr) + " " + pr.Title
	if reason != "" {
		line += " " + s.c(dim, "— "+reason)
	}
	return line
}

// fit truncates to the terminal width, ANSI- and rune-width aware.
func (s Style) fit(line string) string {
	if s.Width <= 0 || ansi.StringWidth(line) <= s.Width {
		return line
	}
	out := ansi.Truncate(line, s.Width, "…")
	if s.Color {
		out += reset
	}
	return out
}
