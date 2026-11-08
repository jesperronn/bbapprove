package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
	"bbapprove/internal/app"
	"bbapprove/internal/ignore"
)

// Config is everything the model needs from the outside world.
type Config struct {
	Backend     Backend
	Opener      Opener
	IgnorePath  string
	Check       func(adapter.PR) (reason string, eligible bool) // eligibility (internal/rules)
	Concurrency int                                             // parallel approve+merge; default runtime.NumCPU()
	Ctx         context.Context
	Version     string // shown dim at the right of the footer's first line when non-empty
	NoColor     bool   // strip every colour/style escape and skip the cursor background
	NoMouse     bool   // ignore mouse events (Run also leaves mouse capture off)
}

const (
	cReset  = "\033[0m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBold   = "\033[1m"

	bgCursor = "\033[48;5;237m" // subtle full-width cursor-row background
	bgHover  = "\033[48;5;235m" // fainter background for the mouse-hovered row
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

type row struct {
	pr      adapter.PR
	result  string // non-empty after an action: WORKING, MERGED, FAIL, APPROVED, NOT MERGED
	resCode string
	resMsg  string
}

type mode int

const (
	modeList           mode = iota
	modeConfirm             // approve+merge confirm (y/N)
	modeConfirmDecline      // decline confirm (y/N)
	modeInput               // single-line text prompt (decline comment or comment-only)
)

// inputKind says what the active text prompt will do on Enter.
type inputKind int

const (
	inputDecline inputKind = iota
	inputComment
)

type loadedMsg struct {
	prs []adapter.PR
	err error
}

type reloadedMsg struct {
	prs []adapter.PR
	err error
}

// The done messages carry the PR URL (not a row index) so a reload in between cannot misdirect them.
type doneMsg struct {
	url string
	err error
}

type declineDoneMsg struct {
	url string
	err error
}

type commentDoneMsg struct {
	url, ref string
	err      error
}

// Model is the Bubble Tea model. It is a plain tea.Model: no TTY is needed to drive it.
type Model struct {
	cfg         Config
	sem         chan struct{}
	rows        []row
	ignored     []string
	loading     bool
	loadErr     error
	cursor, top int
	marks       map[int]bool
	showIgnored bool
	showHelp    bool
	mode        mode
	pending     []int
	// pendingBatch is true when pending came from the marked set (uppercase keys): only then are the
	// acted rows un-marked. pendingSkipped counts marked rows an A skipped as not eligible.
	pendingBatch   bool
	pendingSkipped int
	reloading      bool
	hover          int // visible index under the mouse, -1 for none
	input          []rune
	inputKind      inputKind
	// Comment-only results are not stored on the rows (a comment must not change a PR's status word);
	// they are summarised in the status bar once every outstanding comment finished.
	cmtOutstanding, cmtOK int
	cmtFails              []string
	msg                   string
	width, height         int
}

// New builds the model.
func New(cfg Config) *Model {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = runtime.NumCPU()
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.Opener == nil {
		cfg.Opener = DefaultOpener
	}
	if cfg.Check == nil {
		cfg.Check = func(adapter.PR) (string, bool) { return "", true }
	}
	m := &Model{cfg: cfg, sem: make(chan struct{}, cfg.Concurrency), marks: map[int]bool{}, loading: true, height: 24, hover: -1}
	m.reloadIgnored()
	return m
}

func (m *Model) reloadIgnored() {
	if m.cfg.IgnorePath == "" {
		return
	}
	if e, err := ignore.Load(m.cfg.IgnorePath); err == nil {
		m.ignored = e
	}
}

// Init loads the PR list once.
func (m *Model) Init() tea.Cmd {
	return func() tea.Msg {
		prs, err := m.cfg.Backend.List(m.cfg.Ctx)
		return loadedMsg{prs, err}
	}
}

// status computes the display status of a row.
func (m *Model) status(r row) (word, code, reason string) {
	if r.result != "" {
		return r.result, r.resCode, r.resMsg
	}
	if _, ok := ignore.Matches(m.ignored, r.pr.Title); ok {
		return "IGNORED", cDim, ""
	}
	if why, ok := m.cfg.Check(r.pr); !ok {
		return "SKIP", cYellow, why
	}
	return "ELIGIBLE", cGreen, ""
}

func (m *Model) eligible(i int) bool {
	w, _, _ := m.status(m.rows[i])
	return w == "ELIGIBLE"
}

// visible returns indexes into m.rows currently shown.
func (m *Model) visible() []int {
	var v []int
	for i, r := range m.rows {
		if !m.showIgnored {
			if w, _, _ := m.status(r); w == "IGNORED" {
				continue
			}
		}
		v = append(v, i)
	}
	return v
}

func (m *Model) current() (int, bool) {
	v := m.visible()
	if m.cursor < 0 || m.cursor >= len(v) {
		return 0, false
	}
	return v[m.cursor], true
}

func (m *Model) clamp() {
	n := len(m.visible())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.bodyHeight()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+h {
		m.top = m.cursor - h + 1
	}
	if max := n - h; m.top > max {
		m.top = max
	}
	if m.top < 0 {
		m.top = 0
	}
}

// helpArea is the extended help shown above the two status lines while `?` is on.
func (m *Model) helpArea() []string {
	if !m.showHelp {
		return nil
	}
	return []string{
		"↑/↓ j/k move   x/space mark   ctrl+a mark all eligible   u clear marks   r reload   . ignored",
		"a approve+merge  d decline  c comment  i ignore  o open: instant, on the cursor row only",
		"A D C: approve (eligible only) / decline / comment ALL MARKED rows; no marks = nothing; marks",
		"are cleared afterwards. Mouse: click row = cursor, left edge = mark, wheel = scroll, hover.",
		"Mouse capture disables native text selection and cmd-click on links; hold Shift to bypass it",
		"(Ghostty, iTerm2, kitty). Off with --no-mouse or BBAPPROVE_MOUSE=0.",
	}
}

func (m *Model) footerHeight() int { return 2 + len(m.helpArea()) }

func (m *Model) bodyHeight() int {
	h := m.height - 1 - m.footerHeight()
	if h < 1 {
		h = 1
	}
	return h
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
	case loadedMsg:
		m.loading = false
		m.loadErr = msg.err
		for _, pr := range msg.prs {
			m.rows = append(m.rows, row{pr: pr})
		}
		m.clamp()
	case reloadedMsg:
		m.applyReload(msg)
	case tea.MouseMsg:
		m.mouse(msg)
	case doneMsg:
		r := m.rowByURL(msg.url)
		if r == nil {
			break
		}
		var pe *PartialError
		switch {
		case msg.err == nil:
			r.result, r.resCode, r.resMsg = "MERGED", cGreen+cBold, ""
		case errors.As(msg.err, &pe):
			r.result, r.resCode, r.resMsg = "APPROVED, NOT MERGED", cRed, oneLine(msg.err.Error())
		default:
			r.result, r.resCode, r.resMsg = "FAIL", cRed, oneLine(msg.err.Error())
		}
		m.msg = r.pr.Project + "/" + r.pr.Repo + "#" + fmt.Sprint(r.pr.Number) + " " + r.result
		m.clamp()
	case declineDoneMsg:
		r := m.rowByURL(msg.url)
		if r == nil {
			break
		}
		if msg.err == nil {
			r.result, r.resCode, r.resMsg = "DECLINED", cRed, ""
		} else {
			r.result, r.resCode, r.resMsg = "FAIL", cRed, oneLine(msg.err.Error())
		}
		m.msg = r.pr.Project + "/" + r.pr.Repo + "#" + fmt.Sprint(r.pr.Number) + " " + r.result
		m.clamp()
	case commentDoneMsg:
		if msg.err == nil {
			m.cmtOK++
		} else {
			m.cmtFails = append(m.cmtFails, msg.ref+": "+oneLine(msg.err.Error()))
		}
		if m.cmtOutstanding--; m.cmtOutstanding <= 0 {
			m.msg = fmt.Sprintf("COMMENTED on %d PR(s)", m.cmtOK)
			if len(m.cmtFails) > 0 {
				m.msg += fmt.Sprintf(", FAIL %d: %s", len(m.cmtFails), strings.Join(m.cmtFails, "; "))
			}
			m.cmtOutstanding, m.cmtOK, m.cmtFails = 0, 0, nil
		}
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (m *Model) rowByURL(url string) *row {
	for i := range m.rows {
		if m.rows[i].pr.URL == url {
			return &m.rows[i]
		}
	}
	return nil
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		return m.inputKey(k)
	case modeConfirm, modeConfirmDecline:
		decline := m.mode == modeConfirmDecline
		m.mode = modeList
		if s == "y" || s == "Y" {
			if decline {
				m.startInput(inputDecline)
				return m, nil
			}
			return m, m.runPending()
		}
		m.pending = nil
		m.msg = "cancelled"
		return m, nil
	}
	m.msg = ""
	var cmd tea.Cmd
	n := len(m.visible())
	switch s {
	case "q":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "x", " ":
		if i, ok := m.current(); ok {
			m.toggleMark(i)
		}
	case "ctrl+a":
		for i := range m.rows {
			// eligible() is false for IGNORED rows, so hidden/ignored rows are never marked.
			if m.eligible(i) {
				m.marks[i] = true
			}
		}
		m.msg = fmt.Sprintf("%d marked", m.markedVisible())
	case "ctrl+d", "u":
		m.marks = map[int]bool{}
	case "a":
		cmd = m.approveCursor()
	case "A":
		m.askApproveMarked()
	case "d":
		m.askDecline(false)
	case "D":
		m.askDecline(true)
	case "c":
		m.askComment(false)
	case "C":
		m.askComment(true)
	case "r":
		cmd = m.reload()
	case "i":
		m.toggleIgnore()
	case ".":
		m.showIgnored = !m.showIgnored
	case "o":
		if i, ok := m.current(); ok {
			if err := m.cfg.Opener(m.rows[i].pr.URL); err != nil {
				m.msg = "open failed: " + oneLine(err.Error())
			} else {
				m.msg = "opened " + m.rows[i].pr.URL
			}
		}
	case "?":
		m.showHelp = !m.showHelp
	}
	m.pruneMarks()
	m.clamp()
	return m, cmd
}

func (m *Model) toggleMark(i int) {
	if m.marks[i] {
		delete(m.marks, i)
	} else {
		m.marks[i] = true
	}
}

// reload asks the Backend for the list again; applyReload keeps cursor and marks by URL.
func (m *Model) reload() tea.Cmd {
	if m.reloading {
		m.msg = "already reloading…"
		return nil
	}
	m.reloading = true
	m.msg = "reloading…"
	return func() tea.Msg {
		prs, err := m.cfg.Backend.List(m.cfg.Ctx)
		return reloadedMsg{prs, err}
	}
}

func (m *Model) applyReload(msg reloadedMsg) {
	m.reloading = false
	if msg.err != nil {
		m.msg = "reload failed: " + oneLine(msg.err.Error())
		return
	}
	curURL := ""
	if i, ok := m.current(); ok {
		curURL = m.rows[i].pr.URL
	}
	marked := map[string]bool{}
	for i := range m.marks {
		marked[m.rows[i].pr.URL] = true
	}
	m.rows = nil
	m.marks = map[int]bool{}
	for i, pr := range msg.prs {
		m.rows = append(m.rows, row{pr: pr})
		if marked[pr.URL] {
			m.marks[i] = true
		}
	}
	m.reloadIgnored()
	for n, i := range m.visible() {
		if m.rows[i].pr.URL == curURL {
			m.cursor = n
			break
		}
	}
	m.hover = -1
	if m.mode != modeList { // indexes held by a prompt are stale now
		m.mode, m.pending, m.input = modeList, nil, nil
	}
	m.pruneMarks()
	m.clamp()
	m.msg = fmt.Sprintf("reloaded %d PRs", len(msg.prs))
}

// rowAt maps a screen line to a visible index (-1 when it is not a list row).
func (m *Model) rowAt(y int) int {
	n := y - 1 // line 0 is the header
	if m.loading || m.loadErr != nil || n < 0 || n >= m.bodyHeight() {
		return -1
	}
	if idx := m.top + n; idx < len(m.visible()) {
		return idx
	}
	return -1
}

func (m *Model) mouse(e tea.MouseMsg) {
	if m.cfg.NoMouse || m.loading || m.loadErr != nil || m.mode != modeList {
		return
	}
	v := m.visible()
	switch {
	case e.Action == tea.MouseActionMotion:
		m.hover = m.rowAt(e.Y)
	case e.Action != tea.MouseActionPress:
	case e.Button == tea.MouseButtonWheelUp:
		m.cursor -= 3
		m.clamp()
	case e.Button == tea.MouseButtonWheelDown:
		m.cursor += 3
		m.clamp()
	case e.Button == tea.MouseButtonLeft:
		n := m.rowAt(e.Y)
		if n < 0 {
			return
		}
		if e.X < 3 {
			m.toggleMark(v[n])
		} else {
			m.cursor = n
		}
		m.clamp()
	}
}

// pruneMarks drops marks on rows that are not visible (rows that just became hidden IGNORED rows).
func (m *Model) pruneMarks() {
	vis := map[int]bool{}
	for _, i := range m.visible() {
		vis[i] = true
	}
	for i := range m.marks {
		if !vis[i] {
			delete(m.marks, i)
		}
	}
}

// markedVisible counts marked rows that are currently shown.
func (m *Model) markedVisible() int {
	n := 0
	for _, i := range m.visible() {
		if m.marks[i] {
			n++
		}
	}
	return n
}

func (m *Model) ref(i int) string {
	pr := m.rows[i].pr
	return fmt.Sprintf("%s/%s#%d", pr.Project, pr.Repo, pr.Number)
}

// markedTargets returns the marked visible rows (any status) that have no action in flight.
func (m *Model) markedTargets() []int {
	var t []int
	for _, i := range m.visible() {
		if m.marks[i] && m.rows[i].result != "WORKING" {
			t = append(t, i)
		}
	}
	return t
}

// cursorTarget is the cursor row for an instant action; ok is false (with a message) when it is busy.
func (m *Model) cursorTarget() (int, bool) {
	i, ok := m.current()
	if !ok {
		return 0, false
	}
	if m.rows[i].result == "WORKING" {
		m.msg = "row busy"
		return 0, false
	}
	return i, true
}

// batchTargets returns the marked rows for an uppercase action, or sets the reason message.
func (m *Model) batchTargets() ([]int, bool) {
	if m.markedVisible() == 0 {
		m.msg = "no marked rows"
		return nil, false
	}
	t := m.markedTargets()
	if len(t) == 0 {
		m.msg = "row busy"
		return nil, false
	}
	return t, true
}

func (m *Model) askDecline(batch bool) {
	var t []int
	if batch {
		var ok bool
		if t, ok = m.batchTargets(); !ok {
			return
		}
	} else {
		i, ok := m.cursorTarget()
		if !ok {
			return
		}
		t = []int{i}
	}
	m.pending, m.pendingBatch, m.pendingSkipped, m.mode = t, batch, 0, modeConfirmDecline
}

func (m *Model) askComment(batch bool) {
	var t []int
	if batch {
		var ok bool
		if t, ok = m.batchTargets(); !ok {
			return
		}
	} else {
		i, ok := m.cursorTarget()
		if !ok {
			return
		}
		t = []int{i}
	}
	m.pending, m.pendingBatch, m.pendingSkipped = t, batch, 0
	m.startInput(inputComment)
}

func (m *Model) startInput(k inputKind) {
	m.mode, m.inputKind, m.input = modeInput, k, nil
}

func (m *Model) inputPrompt() string {
	if m.inputKind == inputDecline {
		return "comment (enter = none, esc = cancel): "
	}
	return fmt.Sprintf("comment on %d PR(s) (enter = send, esc = cancel): ", len(m.pending))
}

// inputKey is the minimal single-line editor: printable runes, backspace, ctrl+u, enter, esc.
// Every key is consumed here so no list action can fire while a prompt is active.
func (m *Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.mode, m.pending, m.input = modeList, nil, nil
		m.msg = "cancelled"
	case tea.KeyEnter:
		text := strings.TrimSpace(string(m.input))
		m.mode, m.input = modeList, nil
		if m.inputKind == inputComment {
			if text == "" {
				m.pending = nil
				m.msg = "refused: empty comment"
				return m, nil
			}
			return m, m.runComment(text)
		}
		return m, m.runDecline(text)
	case tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
	case tea.KeyCtrlU:
		m.input = nil
	case tea.KeySpace:
		m.input = append(m.input, ' ')
	case tea.KeyRunes:
		for _, r := range k.Runes {
			if r >= ' ' && r != 0x7f {
				m.input = append(m.input, r)
			}
		}
	}
	return m, nil
}

func (m *Model) runDecline(comment string) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.pending))
	for _, i := range m.pending {
		i, pr := i, m.rows[i].pr
		m.rows[i].result, m.rows[i].resCode, m.rows[i].resMsg = "WORKING", cYellow, ""
		m.unmark(i)
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			return declineDoneMsg{pr.URL, m.cfg.Backend.Decline(m.cfg.Ctx, pr, comment)}
		})
	}
	m.msg = fmt.Sprintf("declining %d PR(s)…", len(m.pending))
	m.pending = nil
	return tea.Batch(cmds...)
}

func (m *Model) runComment(body string) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.pending))
	for _, i := range m.pending {
		i, pr := i, m.rows[i].pr
		m.unmark(i) // the row's status word is left untouched
		ref := m.ref(i)
		m.cmtOutstanding++
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			return commentDoneMsg{pr.URL, ref, m.cfg.Backend.Comment(m.cfg.Ctx, pr, body)}
		})
	}
	m.msg = fmt.Sprintf("commenting on %d PR(s)…", len(m.pending))
	m.pending = nil
	return tea.Batch(cmds...)
}

// unmark drops the mark of a row a batch action just acted on; instant actions never touch marks.
func (m *Model) unmark(i int) {
	if m.pendingBatch {
		delete(m.marks, i)
	}
}

// approveCursor approves+merges the cursor row immediately (lowercase a): no prompt, marks untouched.
func (m *Model) approveCursor() tea.Cmd {
	i, ok := m.cursorTarget()
	if !ok {
		return nil
	}
	if w, _, why := m.status(m.rows[i]); w != "ELIGIBLE" {
		if why == "" {
			why = strings.ToLower(w)
		}
		m.msg = "refused: not eligible (" + why + ")"
		return nil
	}
	m.pending, m.pendingBatch, m.pendingSkipped = []int{i}, false, 0
	return m.runPending()
}

// askApproveMarked starts the A confirm for the eligible marked rows.
func (m *Model) askApproveMarked() {
	t, ok := m.batchTargets()
	if !ok {
		return
	}
	var targets []int
	for _, i := range t {
		if m.eligible(i) {
			targets = append(targets, i)
		}
	}
	if len(targets) == 0 {
		m.msg = "no marked PR is eligible"
		return
	}
	m.pending, m.pendingBatch, m.pendingSkipped = targets, true, m.markedVisible()-len(targets)
	m.mode = modeConfirm
}

func (m *Model) runPending() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.pending))
	for _, i := range m.pending {
		i, pr := i, m.rows[i].pr
		m.rows[i].result, m.rows[i].resCode, m.rows[i].resMsg = "WORKING", cYellow, ""
		m.unmark(i)
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			return doneMsg{pr.URL, m.cfg.Backend.ApproveAndMerge(m.cfg.Ctx, pr)}
		})
	}
	m.msg = fmt.Sprintf("working on %d PR(s)…", len(m.pending))
	if m.pendingSkipped > 0 {
		m.msg += fmt.Sprintf(" (skipped %d not eligible)", m.pendingSkipped)
	}
	m.pending, m.pendingSkipped = nil, 0
	return tea.Batch(cmds...)
}

func (m *Model) toggleIgnore() {
	i, ok := m.current()
	if !ok {
		return
	}
	pr := m.rows[i].pr
	if e, ign := ignore.Matches(m.ignored, pr.Title); ign {
		if err := ignore.Remove(m.cfg.IgnorePath, e); err != nil {
			m.msg = "un-ignore failed: " + oneLine(err.Error())
			return
		}
		m.msg = "un-ignored '" + e + "'"
	} else {
		dep, ok := ignore.DependencyFromTitle(pr.Title)
		if !ok {
			m.msg = "cannot determine dependency from title"
			return
		}
		if err := ignore.Add(m.cfg.IgnorePath, dep); err != nil {
			m.msg = "ignore failed: " + oneLine(err.Error())
			return
		}
		m.msg = "ignored '" + dep + "'"
	}
	m.reloadIgnored()
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
	st := app.Style{Color: true}
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

// statusLine is footer line 1: message / prompt / cursor URL on the left, dim version on the right.
// The left side is truncated so the version always stays visible.
func (m *Model) statusLine() string {
	right := ""
	if m.cfg.Version != "" {
		right = "bbapprove " + m.cfg.Version
	}
	rw := ansi.StringWidth(right)
	avail := m.width
	if m.width > 0 {
		if m.width-rw-1 >= 8 {
			avail = m.width - rw - 1
		} else {
			right, rw = "", 0
		}
	}
	var left string
	switch {
	case m.mode == modeConfirm:
		left = cBold + fmt.Sprintf("approve+merge %d PR(s)? y/N", len(m.pending)) + cReset
	case m.mode == modeConfirmDecline:
		left = cBold + fmt.Sprintf("decline %d PR(s)? y/N", len(m.pending)) + cReset
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
		if i, ok := m.current(); ok && m.rows[i].pr.URL != "" {
			left = cDim + m.rows[i].pr.URL + cReset
		}
	}
	if avail <= 0 {
		return left + "  " + cDim + right + cReset
	}
	if ansi.StringWidth(left) > avail {
		left = ansi.Truncate(left, avail, "…") + cReset
	}
	pad := avail - ansi.StringWidth(left)
	out := left + strings.Repeat(" ", pad)
	if right != "" {
		out += " " + cDim + right + cReset
	}
	return out
}

var hintItems = [][2]string{
	{"a", "approve"}, {"d", "decline"}, {"c", "comment"}, {"i", "ignore"},
	{"x", "mark"}, {"o", "open"}, {"?", "help"}, {"q", "quit"},
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
	first := hintItems[0][0] + " " + hintItems[0][1]
	if m.width <= 0 || ansi.StringWidth(counts)+2+ansi.StringWidth(first) <= m.width {
		add(counts, cDim+counts+cReset)
	}
	for _, h := range hintItems {
		if !add(h[0]+" "+h[1], cBold+h[0]+cReset+cDim+" "+h[1]+cReset) {
			break
		}
	}
	return b.String()
}

// View renders the whole screen.
func (m *Model) View() string {
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
	st := app.Style{Color: true}
	b.WriteString(m.fit("  "+st.Header()) + "\n")
	v := m.visible()
	h := m.bodyHeight()
	for n := 0; n < h; n++ {
		if m.top+n < len(v) {
			b.WriteString(m.renderRow(v[m.top+n], m.top+n == m.cursor, m.top+n == m.hover))
		} else if len(v) == 0 && n == 0 {
			b.WriteString(cDim + "no pull requests" + cReset)
		}
		b.WriteString("\n")
	}
	for _, l := range m.helpArea() {
		b.WriteString(m.fit(cDim+l+cReset) + "\n")
	}
	b.WriteString(m.statusLine() + "\n")
	// No trailing newline: a View of exactly m.height lines plus "\n" would make Bubble Tea
	// drop the first line (the header) in the alt screen.
	b.WriteString(m.hintLine(len(v)))
	return b.String()
}
