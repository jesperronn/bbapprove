package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/app"
)

// withBG paints bg behind a whole line. Every inner reset would cancel the background, so it is
// re-emitted after each one; the line is closed with a final reset.
func withBG(s, bg string) string {
	s = strings.ReplaceAll(s, cReset, cReset+bg)
	s = strings.ReplaceAll(s, "\033[m", cReset+bg)
	return bg + s + cReset
}

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripSGR removes colour/style escapes (OSC 8 hyperlinks are left alone).
func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }

func (m *Model) footerHeight() int { return 2 }

func (m *Model) bodyHeight() int {
	h := m.height - 1 - m.footerHeight()
	if h < 1 {
		h = 1
	}
	return h
}

const osc8Close = "\x1b]8;;\x1b\\"

func (m *Model) fit(s string) string {
	if m.width <= 0 || ansi.StringWidth(s) <= m.width {
		return s
	}
	return ansi.Truncate(s, m.width, "…") + cReset + osc8Close
}

func (m *Model) renderRow(idx int, selected, hovered bool) string {
	r := m.rows[idx]
	word, code, reason := m.status(r)
	st := colorStyle
	line := st.Row(word, code, r.pr, reason)
	ref := st.Ref(r.pr)
	if r.pr.URL != "" {
		line = strings.Replace(line, ref, "\x1b]8;;"+r.pr.URL+"\x1b\\"+ref+osc8Close, 1)
	}
	cur, mark := " ", " "
	if selected {
		cur = "›"
	}
	if m.marks[idx] {
		mark = cBold + cYellow + "▌" + cReset
	}
	if r.gone { // merged or declined elsewhere: the whole line dim, like an inactive entry
		line = cDim + stripSGR(line) + cReset
	}
	out := m.fit(cur + mark + line)
	bg := ""
	switch {
	case m.cfg.NoColor:
	case selected:
		bg = bgCursor
	case hovered:
		bg = bgHover
	}
	if bg == "" {
		return out
	}
	if pad := m.width - ansi.StringWidth(out); m.width > 0 && pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	return withBG(out, bg)
}

// visibleInput returns the tail of the input that fits in room columns so the cursor stays visible.
func (m *Model) visibleInput(room int) string {
	r := m.input
	if room < 1 {
		room = 1
	}
	for len(r) > 0 && ansi.StringWidth(string(r)) > room {
		r = r[1:]
	}
	return string(r)
}

// openHintWidth is the fixed width of the dim hint after the footer URL ("  o open").
const openHintWidth = 8

func openHint() string { return "  " + cBold + "o" + cReset + cDim + " open" + cReset }

// middleEllipsis shortens a URL to w columns keeping the start and the tail (the PR number):
// https://host/…/pull-requests/4961.
func middleEllipsis(u string, w int) string {
	total := ansi.StringWidth(u)
	if total <= w {
		return u
	}
	if w <= 1 {
		return strings.Repeat("…", w)
	}
	tailW := 0
	if i := strings.LastIndex(u, "/"); i > 0 {
		if j := strings.LastIndex(u[:i], "/"); j >= 0 {
			tailW = ansi.StringWidth(u[j:])
		}
	}
	const minHead = 8 // "https://"
	if tailW > w-1-minHead {
		tailW = w - 1 - minHead
	}
	if tailW < 1 {
		tailW = (w - 1) / 2
	}
	headW := w - 1 - tailW
	return ansi.Truncate(u, headW, "") + "…" + ansi.TruncateLeft(u, total-tailW, "")
}

// urlSlot is the default content of footer line 1: the cursor row's URL (an OSC 8 hyperlink to the
// full URL, middle-ellipsized to fit) plus the "o open" hint, avail columns at most (avail <= 0 means
// unlimited). It returns "" and 0 when there is no URL to show.
func (m *Model) urlSlot(avail int) (string, int) {
	i, ok := m.current()
	if !ok || m.rows[i].pr.URL == "" {
		return "", 0
	}
	u := m.rows[i].pr.URL
	shown := u
	if avail > 0 {
		shown = middleEllipsis(u, max(avail-openHintWidth, 1))
	}
	out := cDim + "\x1b]8;;" + u + "\x1b\\" + shown + osc8Close + cReset
	w := ansi.StringWidth(shown)
	if avail <= 0 || avail >= w+openHintWidth {
		out += openHint()
		w += openHintWidth
	}
	return out, w
}

// footerAvail is the width of footer line 1's left side and the version shown on its right.
func (m *Model) footerAvail() (avail int, right string) {
	if m.cfg.Version != "" {
		right = "bbapprove " + m.cfg.Version
	}
	rw := ansi.StringWidth(right)
	avail = m.width
	if m.width > 0 {
		// The "o open" hint and the URL's start outrank the version on narrow screens.
		minLeft := 8
		if m.mode == modeList && m.msg == "" {
			minLeft = openHintWidth + 12
		}
		if m.width-rw-1 >= minLeft {
			avail = m.width - rw - 1
		} else {
			right = ""
		}
	}
	return avail, right
}

// urlHitWidth is how many columns of footer line 1 (from the left) open the URL on click; 0 when
// the line does not show the URL.
func (m *Model) urlHitWidth() int {
	if m.mode != modeList || m.msg != "" {
		return 0
	}
	avail, _ := m.footerAvail()
	_, w := m.urlSlot(avail)
	return w
}

// statusLine is footer line 1: message / prompt / cursor URL with an "o open" hint on the left, dim
// version on the right. The version is dropped before the hint; the URL is cut in the middle.
func (m *Model) statusLine() string {
	avail, right := m.footerAvail()
	var left string
	switch {
	case m.mode == modeConfirm:
		left = cBold + m.confirmPrompt() + cReset
	case m.mode == modeInput:
		p := m.inputPrompt()
		room := 1 << 20
		if avail > 0 {
			room = avail - ansi.StringWidth(p) - 1
		}
		left = cBold + p + m.visibleInput(room) + "█" + cReset
	case m.msg != "":
		left = cBold + m.msg + cReset
	default:
		left, _ = m.urlSlot(avail)
		if avail <= 0 {
			return left + "  " + cDim + right + cReset
		}
		return joinStatus(left, avail, right)
	}
	if avail <= 0 {
		return left + "  " + cDim + right + cReset
	}
	if ansi.StringWidth(left) > avail {
		left = ansi.Truncate(left, avail, "…") + cReset
	}
	return joinStatus(left, avail, right)
}

func joinStatus(left string, avail int, right string) string {
	out := left + strings.Repeat(" ", max(avail-ansi.StringWidth(left), 0))
	if right != "" {
		out += " " + cDim + right + cReset
	}
	return out
}

// hintItems is the default hint set (instant actions on the cursor row); shiftHintItems replaces it
// while Shift is held (batch actions on the marked rows).
var hintItems = [][2]string{
	{"a", "approve"}, {"m", "merge"}, {"d", "decline"}, {"c", "comment"}, {"i", "ignore"},
	{"x", "mark"}, {"o", "open"}, {"?", "help"}, {"q", "quit"},
}

var shiftHintItems = [][2]string{
	{"M", "force merge (skips the rules)"}, {"ctrl+a", "mark all eligible"}, {"x", "mark"}, {"o", "open"}, {"?", "help"}, {"q", "quit"},
}

// hintLine is footer line 2: counts first when they fit, then as many key hints as fit; hints are
// dropped from the end, never wrapped.
func (m *Model) hintLine(nVisible int) string {
	var b strings.Builder
	w := 0
	add := func(plain, styled string) bool {
		need := ansi.StringWidth(plain)
		if w > 0 {
			need += 2
		}
		if m.width > 0 && w+need > m.width {
			return false
		}
		if w > 0 {
			b.WriteString("  ")
		}
		b.WriteString(styled)
		w += need
		return true
	}
	counts := fmt.Sprintf("%d PRs, %d marked", nVisible, m.markedVisible())
	items := hintItems
	if m.shiftHints() {
		items = shiftHintItems
	}
	first := items[0][0] + " " + items[0][1]
	if m.width <= 0 || ansi.StringWidth(counts)+2+ansi.StringWidth(first) <= m.width {
		add(counts, cDim+counts+cReset)
	}
	// Drop priority: "o open" survives longest, then the rest in listed order (so "? help" and
	// "q quit" go first). Kept items are shown in listed order.
	order := []int{}
	for i, h := range items {
		if h[0] == "o" {
			order = append([]int{i}, order...)
		} else {
			order = append(order, i)
		}
	}
	keep := map[int]bool{}
	used := w
	for _, i := range order {
		need := ansi.StringWidth(items[i][0] + " " + items[i][1])
		if used > 0 {
			need += 2
		}
		if m.width > 0 && used+need > m.width {
			break
		}
		used += need
		keep[i] = true
	}
	for i, h := range items {
		if keep[i] {
			add(h[0]+" "+h[1], cBold+h[0]+cReset+cDim+" "+h[1]+cReset)
		}
	}
	return b.String()
}

// View renders the whole screen and declares the terminal features the program wants: the alternate
// screen, mouse reporting (all motion, for the hover highlight) unless disabled, and the window title.
func (m *Model) View() tea.View {
	v := tea.NewView(m.content())
	v.AltScreen = true
	v.WindowTitle = "bbapprove"
	if !m.cfg.NoMouse {
		v.MouseMode = tea.MouseModeAllMotion
	}
	if !m.cfg.NoKitty {
		// Kitty keyboard protocol: release events plus all keys as escape codes (the only way a bare
		// Shift press is reported), with alternate keys and associated text so typed text is unchanged.
		v.KeyboardEnhancements = tea.KeyboardEnhancements{
			ReportEventTypes: true, ReportAlternateKeys: true,
			ReportAllKeysAsEscapeCodes: true, ReportAssociatedText: true,
		}
		v.ReportFocus = true // a Shift release is never seen while unfocused
	}
	return v
}

// content is the rendered screen text.
func (m *Model) content() string {
	v := m.view()
	if m.cfg.NoColor {
		return stripSGR(v)
	}
	return v
}

func (m *Model) view() string {
	var b strings.Builder
	if m.loading {
		b.WriteString("Loading pull requests…\n")
		return b.String()
	}
	if m.loadErr != nil {
		return m.fit("error: "+oneLine(m.loadErr.Error())) + "\n" + m.fit("q quit") + "\n"
	}
	m.clamp()
	st := colorStyle
	if m.showHelp {
		b.WriteString(m.fit("  "+cBold+"Help"+cReset+cDim+"  (press ? to close)"+cReset) + "\n")
	} else {
		b.WriteString(m.fit("  "+st.Header()) + "\n")
	}
	v := m.visible()
	h := m.bodyHeight()
	help := m.helpArea()
	for n := 0; n < h; n++ {
		if m.showHelp {
			if n < len(help) {
				b.WriteString(m.fit(help[n]))
			}
		} else if m.top+n < len(v) {
			b.WriteString(m.renderRow(v[m.top+n], m.top+n == m.cursor, m.top+n == m.hover))
		} else if len(v) == 0 && n == 0 {
			b.WriteString(cDim + "no pull requests" + cReset)
		}
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	// No trailing newline: a View of exactly m.height lines plus "\n" would make Bubble Tea
	// drop the first line (the header) in the alt screen.
	b.WriteString(m.hintLine(len(v)))
	return b.String()
}

// colorStyle formats rows and refs with colour; the TUI always draws in colour.
var colorStyle = app.Style{Color: true}
