package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// helpSection is one titled group of the help panel: an optional note, then one key per line.
type helpSection struct {
	title string
	note  string
	items [][2]string
}

var helpSections = []helpSection{
	{"Move", "", [][2]string{
		{"↑/↓  j/k", "move the cursor"},
		{". or h", "show or hide ignored rows"},
	}},
	{"Mark", "", [][2]string{
		{"x  space", "mark or unmark the cursor row"},
		{"ctrl+a", "mark all eligible rows"},
		{"u", "clear marks"},
	}},
	{"Act", "a, m, M, d and c act on the MARKED rows, or on the cursor row when nothing is marked; marks are cleared afterwards. On the cursor row a and m run at once; marked rows and M ask y/N first. A failed approval never stops the merge.", [][2]string{
		{"a", "approve only"},
		{"m", "approve and merge (eligible only)"},
		{"M", "force approve and merge"},
		{"d", "decline (needs a comment)"},
		{"c", "comment"},
		{"i", "ignore"},
		{"o", "open"},
	}},
	{"Refresh", "Both keep FAIL, NOT MERGED and APPROVED until the PR changed.", [][2]string{
		{"r", "re-read the MARKED rows (or the cursor row)"},
		{"R", "reload the list, add new PRs and keep a PR merged or declined elsewhere as a dim row that cannot be marked"},
	}},
	{"Mouse", "Mouse capture disables native text selection and cmd-click on links; hold Shift to bypass it (Ghostty, iTerm2, kitty). Off with --no-mouse or BBAPPROVE_MOUSE=0.", [][2]string{
		{"click row", "move the cursor"},
		{"left edge", "mark the row"},
		{"click URL", "open it (the URL at the bottom)"},
		{"wheel", "scroll"},
	}},
	{"Hints", "", [][2]string{
		{"bottom line", "key hints; while Shift is held (terminals with the kitty keyboard protocol) they show the batch set: M force merge, ctrl+a mark all eligible, x mark, o open"},
		{"? / q", "close this help / quit"},
	}},
}

// helpMaxWidth caps the help panel so paragraphs stay readable on a wide terminal.
const helpMaxWidth = 96

// helpKeyCol is the width of the key column of the help panel.
const helpKeyCol = 14

// wrapText word-wraps s to width columns (at least 1).
func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, l := range strings.Split(ansi.Wrap(s, width, ""), "\n") {
		out = append(out, strings.TrimRight(l, " "))
	}
	return out
}

// helpPanel renders the help as lines at most width wide: titled sections, one key per line in a
// key/description layout. A terminal too narrow for two columns stacks the description below the key.
func helpPanel(width int) []string {
	if width <= 0 || width > helpMaxWidth {
		width = helpMaxWidth
	}
	const indent = 2
	keyCol := helpKeyCol
	stacked := width < indent+keyCol+16
	var out []string
	for _, sec := range helpSections {
		out = append(out, cBold+sec.title+cReset)
		if sec.note != "" {
			for _, l := range wrapText(sec.note, width-indent) {
				out = append(out, cDim+strings.Repeat(" ", indent)+l+cReset)
			}
		}
		for _, it := range sec.items {
			key := ansi.Truncate(it[0], width-indent, "")
			if stacked {
				out = append(out, strings.Repeat(" ", indent)+cBold+key+cReset)
				for _, l := range wrapText(it[1], width-indent-2) {
					out = append(out, strings.Repeat(" ", indent+2)+l)
				}
				continue
			}
			pad := strings.Repeat(" ", max(keyCol-ansi.StringWidth(key), 1))
			for i, l := range wrapText(it[1], width-indent-keyCol) {
				if i == 0 {
					out = append(out, strings.Repeat(" ", indent)+cBold+key+cReset+pad+l)
				} else {
					out = append(out, strings.Repeat(" ", indent+keyCol)+l)
				}
			}
		}
		out = append(out, "")
	}
	return out[:len(out)-1]
}

// helpArea is the help panel while `?` is on: it replaces the list, so it has exactly bodyHeight
// lines at most. When the terminal is too short its tail is replaced by a "more" line.
func (m *Model) helpArea() []string {
	if !m.showHelp {
		return nil
	}
	out := helpPanel(m.width)
	if h := m.bodyHeight(); m.height > 0 && len(out) > h {
		out = append(out[:h-1], cDim+"… (make the window taller to see all of the help)"+cReset)
	}
	return out
}
