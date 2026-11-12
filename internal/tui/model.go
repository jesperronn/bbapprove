package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"bbapprove/internal/adapter"
	"bbapprove/internal/app"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
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
	// NoKitty stops View from requesting the kitty keyboard protocol enhancements (BBAPPROVE_KITTY=0);
	// the hint line then never follows the Shift key.
	NoKitty bool
	// Summary styles the exit summary: Color for ANSI colours, Width (0 = never truncate) for lines.
	// Run sets it from the stdout terminal and NO_COLOR; the zero value is plain and untruncated.
	Summary app.Style
}

// Row result words. Eligible and untouched rows have no result and a blank status cell.
const (
	wApproving  = "APPROVING…"
	wDeclining  = "DECLINING…"
	wCommenting = "COMMENTING…"
	wMerged     = "MERGED"
	wApproved   = "APPROVED"
	wNotMerged  = "NOT MERGED"
	wDeclined   = "DECLINED"
	wCommented  = "COMMENTED"
	wFail       = "FAIL"
	wWorking    = "WORKING…" // summary only: action still running at quit
	wIgnored    = "IGNORED"
	wUnignored  = "UN-IGNORED"
)

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
	result  string // non-empty after an action: APPROVING…, MERGED, NOT MERGED, DECLINED, COMMENTED, FAIL ...
	resCode string
	resMsg  string
	// locked is set by a merge or decline that went through: the row is no longer eligible until a
	// reload. A failed attempt leaves the row open so it can be retried; a comment never locks.
	locked bool
}

// failed reports a failed approve, merge or decline: the row is open for a retry and stays listed.
func (r row) failed() bool {
	return r.result == wFail || r.result == wNotMerged
}

func (r row) busy() bool {
	return r.result == wApproving || r.result == wDeclining || r.result == wCommenting
}

// logEntry is one completed (or still running) action of this session, kept for the exit summary.
// Entries survive reloads, which reset the rows. One entry per PR and verb; a repeat replaces it.
type logEntry struct {
	key  string
	pr   adapter.PR
	word string
	code string
	msg  string
}

type mode int

const (
	modeList    mode = iota
	modeConfirm      // approve / approve+merge confirm (y/N)
	modeInput        // single-line text prompt (decline comment or comment-only)
)

// action is what a confirmed approve key runs.
type action int

const (
	actApprove action = iota // a: approve only, any row
	actMerge                 // m: approve+merge, eligible rows only
	actForce                 // M: approve+merge, whatever the rules say
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
	url     string
	err     error
	warning string // adapter warning after a successful merge
}

type approveDoneMsg struct {
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
	repoLocks   adapter.RepoLocks // merges into one repository run one at a time
	rows        []row
	ignored     []string
	loading     bool
	loadErr     error
	cursor, top int
	marks       map[int]bool
	showIgnored bool
	showHelp    bool
	// kitty is set once the terminal answered the keyboard-enhancements query, i.e. speaks the kitty
	// keyboard protocol. shiftL/shiftR track the physical Shift keys from modifier-key press/release
	// events; the hint line follows them only when kitty is set.
	kitty   bool
	shiftL  bool
	shiftR  bool
	mode    mode
	pending []int
	act     action // what pending runs on y
	// pendingBatch is true when pending came from the marked set: only then are the acted rows
	// un-marked. pendingSkipped counts marked rows an action skipped (not eligible, or done already).
	pendingBatch   bool
	pendingSkipped int
	reloading      bool
	hover          int // visible index under the mouse, -1 for none
	input          []rune
	inputKind      inputKind
	// Comment results also show on the row (COMMENTED / FAIL) and are summarised in the status bar
	// once every outstanding comment finished.
	cmtOutstanding, cmtOK int
	cmtFails              []string
	msg                   string
	width, height         int
	log                   []logEntry
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

// kind classifies a row with the logic shared with the CLI (rules.Classify).
func (m *Model) kind(r row) (rules.Kind, string) {
	return rules.Classify(m.cfg.Check, m.ignored, r.pr)
}

// status computes the display status of a row; the word is blank for an eligible, untouched row.
func (m *Model) status(r row) (word, code, reason string) {
	kind, why := m.kind(r)
	if r.result != "" && !(r.result == wCommented && kind == rules.Ignored) {
		return r.result, r.resCode, r.resMsg
	}
	switch kind {
	case rules.Ignored:
		return wIgnored, cDim, why
	case rules.Skip:
		return "SKIP", cYellow, why
	}
	return "", "", ""
}

// eligible reports whether the row may be approved: classified eligible, no approve/decline attempt
// yet and nothing in flight. A COMMENTED row stays eligible.
func (m *Model) eligible(i int) bool {
	r := m.rows[i]
	k, _ := m.kind(r)
	return k == rules.Eligible && !r.locked && !r.busy()
}

// hiddenIgnored is true for a row the `.`/`h` toggle hides: classified Ignored and not part of an action.
func (m *Model) hiddenIgnored(r row) bool {
	k, _ := m.kind(r)
	return k == rules.Ignored && !r.locked && !r.busy() && !r.failed()
}

// visible returns indexes into m.rows currently shown.
func (m *Model) visible() []int {
	var v []int
	for i, r := range m.rows {
		if !m.showIgnored && m.hiddenIgnored(r) {
			continue
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

// helpParagraph is one block of the extended help; hang is the indent of its wrapped continuation lines.
type helpParagraph struct {
	text string
	hang int
}

var helpParagraphs = []helpParagraph{
	{"↑/↓ j/k move   x/space mark   ctrl+a mark all eligible   u clear marks   r reload   . or h ignored", 2},
	{"a approve only  m approve+merge (eligible only)  M force approve+merge  d decline (needs a comment)  c comment  i ignore  o open", 2},
	{"a m M d c act on the MARKED rows, or on the cursor row when nothing is marked; marks are cleared afterwards. On the cursor row a and m run at once; marked rows and M ask y/N first. A failed approval never stops the merge. Mouse: click row = cursor, left edge = mark, click the URL at the bottom = open, wheel = scroll, hover.", 0},
	{"Hints:        a approve  m merge  d decline  c comment  i ignore  x mark  o open  ? help  q quit", 2},
	{"Hints: Shift  M force merge  ctrl+a mark all eligible  x mark  o open", 2},
	{"The hint line shows the Shift set while Shift is held (terminals with the kitty keyboard protocol).", 0},
	{"Mouse capture disables native text selection and cmd-click on links; hold Shift to bypass it (Ghostty, iTerm2, kitty). Off with --no-mouse or BBAPPROVE_MOUSE=0.", 0},
}

// minListRows is how many list rows the help may never take away on a short terminal.
const minListRows = 4

// helpArea is the extended help shown above the two status lines while `?` is on, word-wrapped to
// the terminal width. On a short terminal its tail is clipped so the list keeps minListRows rows.
func (m *Model) helpArea() []string {
	if !m.showHelp {
		return nil
	}
	var out []string
	for _, p := range helpParagraphs {
		if m.width <= 0 {
			out = append(out, p.text)
			continue
		}
		hang := p.hang
		if hang >= m.width {
			hang = 0
		}
		for i, l := range strings.Split(ansi.Wrap(p.text, m.width-hang, ""), "\n") {
			l = strings.TrimRight(l, " ")
			if i > 0 {
				l = strings.Repeat(" ", hang) + l
			}
			out = append(out, l)
		}
	}
	if m.height > 0 {
		limit := m.height - 1 - 2 - minListRows
		if limit < 0 {
			limit = 0
		}
		if len(out) > limit {
			out = out[:limit]
		}
	}
	return out
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
		var pe *PartialError
		word, code, text := wMerged, cGreen+cBold, ""
		switch {
		case msg.err == nil:
			// Merged, with the adapter's note (e.g. branch not deleted) as the reason; the MERGED word already
			// says "merge ok", so the long row only keeps the part that is news.
			text = strings.TrimPrefix(oneLine(msg.warning), "merge ok, but ")
		case errors.As(msg.err, &pe):
			word, code, text = wNotMerged, cRed, oneLine(msg.err.Error())
		default:
			word, code, text = wFail, cRed, oneLine(msg.err.Error())
		}
		m.finish(msg.url, "approve", word, code, text, true)
	case approveDoneMsg:
		word, code, text := wApproved, cGreen, ""
		if msg.err != nil {
			word, code, text = wFail, cRed, oneLine(msg.err.Error())
		}
		m.finish(msg.url, "approve", word, code, text, false)
	case declineDoneMsg:
		word, code, text := wDeclined, cRed, ""
		if msg.err != nil {
			word, code, text = wFail, cRed, oneLine(msg.err.Error())
		}
		m.finish(msg.url, "decline", word, code, text, true)
	case commentDoneMsg:
		if msg.err == nil {
			m.cmtOK++
			m.finish(msg.url, "comment", wCommented, cGreen, "", false)
		} else {
			m.cmtFails = append(m.cmtFails, msg.ref+": "+oneLine(msg.err.Error()))
			m.finish(msg.url, "comment", wFail, cRed, oneLine(msg.err.Error()), false)
		}
		if m.cmtOutstanding--; m.cmtOutstanding <= 0 {
			m.msg = fmt.Sprintf("COMMENTED on %d PR(s)", m.cmtOK)
			if len(m.cmtFails) > 0 {
				m.msg += fmt.Sprintf(", FAIL %d: %s", len(m.cmtFails), strings.Join(m.cmtFails, "; "))
			}
			m.cmtOutstanding, m.cmtOK, m.cmtFails = 0, 0, nil
		}
	case tea.KeyboardEnhancementsMsg:
		m.kitty = true
	case tea.BlurMsg:
		m.shiftL, m.shiftR = false, false // a release while unfocused is never seen
	case tea.KeyReleaseMsg:
		switch msg.Key().Code {
		case tea.KeyLeftShift:
			m.shiftL = false
		case tea.KeyRightShift:
			m.shiftR = false
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// finish records the outcome of an approve/decline/comment: always in the session log (the row may be
// gone after a reload), and on the row when it is still listed. lock marks a merge or decline that went
// through; a failure (FAIL, NOT MERGED) reopens the row for another try.
func (m *Model) finish(url, verb, word, code, text string, lock bool) {
	m.logSet(url, verb, word, code, text)
	r := m.rowByURL(url)
	if r == nil {
		return
	}
	r.result, r.resCode, r.resMsg = word, code, text
	if lock {
		r.locked = word == wMerged || word == wDeclined
		m.msg = r.pr.Project + "/" + r.pr.Repo + "#" + fmt.Sprint(r.pr.Number) + " " + word
	}
	m.clamp()
}

// logStart records an action as running; finish replaces it with the outcome.
func (m *Model) logStart(pr adapter.PR, verb string) {
	m.logPut(logEntry{key: pr.URL + "|" + verb, pr: pr, word: wWorking, code: cYellow})
}

func (m *Model) logSet(url, verb, word, code, text string) {
	key := url + "|" + verb
	for i := range m.log {
		if m.log[i].key == key {
			m.log[i].word, m.log[i].code, m.log[i].msg = word, code, text
			return
		}
	}
}

// logPut replaces the entry with the same key in place, or appends.
func (m *Model) logPut(e logEntry) {
	for i := range m.log {
		if m.log[i].key == e.key {
			m.log[i] = e
			return
		}
	}
	m.log = append(m.log, e)
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

// isModifierKey reports a press of a bare modifier or lock key (only reported by the kitty protocol).
func isModifierKey(c rune) bool {
	return c >= tea.KeyLeftShift && c <= tea.KeyIsoLevel5Shift ||
		c == tea.KeyCapsLock || c == tea.KeyScrollLock || c == tea.KeyNumLock
}

// keyName is the key's string with a lone Shift+letter normalised to the uppercase letter, so a
// terminal that reports "A" as shift+a still triggers the same action as one that sends "A".
func keyName(k tea.KeyPressMsg) string {
	if k.Text == "" && k.Mod == tea.ModShift && k.Code >= 'a' && k.Code <= 'z' {
		return strings.ToUpper(string(k.Code))
	}
	return k.String()
}

// shiftHints reports whether the hint line shows the batch set: Shift is held and the terminal
// reports modifier keys (kitty keyboard protocol). Anywhere else the instant set is always shown.
func (m *Model) shiftHints() bool {
	return m.kitty && !m.cfg.NoKitty && (m.shiftL || m.shiftR)
}

func (m *Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyLeftShift:
		m.shiftL = true
		return m, nil
	case tea.KeyRightShift:
		m.shiftR = true
		return m, nil
	}
	if !k.Mod.Contains(tea.ModShift) {
		// Safety net for a lost release: a key press without Shift means Shift is not held.
		m.shiftL, m.shiftR = false, false
	}
	if isModifierKey(k.Code) {
		return m, nil // a bare modifier never cancels a prompt or clears the message
	}
	s := keyName(k)
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		return m.inputKey(k)
	case modeConfirm:
		m.mode = modeList
		if s == "y" || s == "Y" {
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
	case "x", "space":
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
		cmd = m.approve(actApprove)
	case "m":
		cmd = m.approve(actMerge)
	case "M":
		cmd = m.approve(actForce)
	case "d":
		m.askDecline()
	case "c":
		m.askComment()
	case "r":
		cmd = m.reload()
	case "i":
		m.toggleIgnore()
	case ".", "h":
		m.showIgnored = !m.showIgnored
	case "o":
		m.openCurrent()
	case "?":
		m.showHelp = !m.showHelp
	}
	m.pruneMarks()
	m.clamp()
	return m, cmd
}

// openCurrent opens the cursor row's URL through the Opener (the `o` key and a click on the footer URL).
func (m *Model) openCurrent() {
	if i, ok := m.current(); ok {
		if err := m.cfg.Opener(m.rows[i].pr.URL); err != nil {
			m.msg = "open failed: " + oneLine(err.Error())
		} else {
			m.msg = "opened " + m.rows[i].pr.URL
		}
	}
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
	switch e := e.(type) {
	case tea.MouseMotionMsg:
		m.hover = m.rowAt(e.Y)
	case tea.MouseWheelMsg:
		switch e.Button {
		case tea.MouseWheelUp:
			m.cursor -= 3
			m.clamp()
		case tea.MouseWheelDown:
			m.cursor += 3
			m.clamp()
		}
	case tea.MouseClickMsg:
		if e.Button != tea.MouseLeft {
			return
		}
		if e.Y == m.height-2 { // footer line 1: the URL and its hint are a link
			if w := m.urlHitWidth(); w > 0 && e.X >= 0 && e.X < w {
				m.openCurrent()
			}
			return
		}
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
	// tea.MouseReleaseMsg changes nothing.
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
		if m.marks[i] && !m.rows[i].busy() {
			t = append(t, i)
		}
	}
	return t
}

// targets returns the rows an action works on: the marked rows when any are marked (batch), else the
// cursor row. ok is false (with a message) when there is nothing to act on.
func (m *Model) targets() (t []int, batch, ok bool) {
	if m.markedVisible() > 0 {
		if t = m.markedTargets(); len(t) == 0 {
			m.msg = "row busy"
			return nil, true, false
		}
		return t, true, true
	}
	i, ok := m.current()
	if !ok {
		return nil, false, false
	}
	if m.rows[i].busy() {
		m.msg = "row busy"
		return nil, false, false
	}
	return []int{i}, false, true
}

// askDecline prompts for the comment a decline needs; the PRs are declined on Enter.
func (m *Model) askDecline() {
	t, batch, ok := m.targets()
	if !ok {
		return
	}
	m.pending, m.pendingBatch, m.pendingSkipped = t, batch, 0
	m.startInput(inputDecline)
}

func (m *Model) askComment() {
	t, batch, ok := m.targets()
	if !ok {
		return
	}
	m.pending, m.pendingBatch, m.pendingSkipped = t, batch, 0
	m.startInput(inputComment)
}

func (m *Model) startInput(k inputKind) {
	m.mode, m.inputKind, m.input = modeInput, k, nil
}

func (m *Model) inputPrompt() string {
	if m.inputKind == inputDecline {
		return fmt.Sprintf("decline %d PR(s), reason (enter = decline, esc = cancel): ", len(m.pending))
	}
	return fmt.Sprintf("comment on %d PR(s) (enter = send, esc = cancel): ", len(m.pending))
}

// inputKey is the minimal single-line editor: printable runes, backspace, ctrl+u, enter, esc.
// Every key is consumed here so no list action can fire while a prompt is active.
func (m *Model) inputKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.Code == tea.KeyEscape:
		m.mode, m.pending, m.input = modeList, nil, nil
		m.msg = "cancelled"
	case k.Code == tea.KeyEnter:
		text := strings.TrimSpace(string(m.input))
		m.mode, m.input = modeList, nil
		if text == "" {
			m.pending = nil
			m.msg = "refused: empty comment"
			if m.inputKind == inputDecline {
				m.msg = "refused: a decline needs a comment"
			}
			return m, nil
		}
		if m.inputKind == inputComment {
			return m, m.runComment(text)
		}
		return m, m.runDecline(text)
	case k.Code == tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
	case k.String() == "ctrl+u":
		m.input = nil
	case k.Code == tea.KeySpace:
		m.input = append(m.input, ' ')
	default:
		for _, r := range k.Text {
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
		m.rows[i].result, m.rows[i].resCode, m.rows[i].resMsg = wDeclining, cYellow, ""
		m.logStart(pr, "decline")
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
		m.rows[i].result, m.rows[i].resCode, m.rows[i].resMsg = wCommenting, cYellow, ""
		m.logStart(pr, "comment")
		m.unmark(i)
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

// approve starts a (approve only), m (approve+merge, eligible rows) or M (approve+merge, forced) on the
// marked rows or the cursor row. a and m on the cursor row run at once; a batch and every M ask first.
func (m *Model) approve(act action) tea.Cmd {
	t, batch, ok := m.targets()
	if !ok {
		return nil
	}
	var run []int
	for _, i := range t {
		if m.allowed(act, i) {
			run = append(run, i)
		}
	}
	if len(run) == 0 {
		switch {
		case batch:
			m.msg = "no marked PR can be " + map[action]string{actApprove: "approved", actMerge: "merged (M forces)", actForce: "merged"}[act]
		case m.rows[t[0]].locked:
			m.msg = "refused: already " + strings.ToLower(m.rows[t[0]].result)
		case act == actMerge:
			w, _, why := m.status(m.rows[t[0]])
			if why == "" {
				why = strings.ToLower(w)
			}
			m.msg = "refused: not eligible (" + why + "); M merges anyway"
		default:
			m.msg = "refused: ignored (i un-ignores)"
		}
		return nil
	}
	m.pending, m.pendingBatch, m.pendingSkipped, m.act = run, batch, len(t)-len(run), act
	if batch || act == actForce {
		m.mode = modeConfirm
		return nil
	}
	return m.runPending()
}

// allowed reports whether act may run on row i: never on a row already merged or declined or on an
// IGNORED row (un-ignore it first); m only on eligible rows.
func (m *Model) allowed(act action, i int) bool {
	r := m.rows[i]
	if k, _ := m.kind(r); r.locked || r.busy() || k == rules.Ignored {
		return false
	}
	return act != actMerge || m.eligible(i)
}

// confirmPrompt is the y/N question for the pending approve action; a forced merge names what it overrides.
func (m *Model) confirmPrompt() string {
	n := len(m.pending)
	switch m.act {
	case actApprove:
		return fmt.Sprintf("approve %d PR(s) (no merge)? y/N", n)
	case actForce:
		var why []string
		for _, i := range m.pending {
			if !m.eligible(i) {
				w, _, r := m.status(m.rows[i])
				if r == "" {
					r = strings.ToLower(w)
				}
				why = append(why, m.ref(i)+": "+r)
			}
		}
		q := fmt.Sprintf("FORCE approve+merge %d PR(s)", n)
		if len(why) > 0 {
			q += " overriding " + strings.Join(why, ", ")
		}
		return q + "? y/N"
	}
	return fmt.Sprintf("approve+merge %d PR(s)? y/N", n)
}

func (m *Model) runPending() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.pending))
	for _, i := range m.pending {
		i, pr := i, m.rows[i].pr
		m.rows[i].result, m.rows[i].resCode, m.rows[i].resMsg = wApproving, cYellow, ""
		m.logStart(pr, "approve")
		m.unmark(i)
		if m.act == actApprove {
			cmds = append(cmds, func() tea.Msg {
				m.sem <- struct{}{}
				defer func() { <-m.sem }()
				return approveDoneMsg{pr.URL, m.cfg.Backend.Approve(m.cfg.Ctx, pr)}
			})
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			defer m.repoLocks.Lock(pr)() // before the slot, so a PR waiting on its repository holds none
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			warn, err := m.cfg.Backend.ApproveAndMerge(m.cfg.Ctx, pr)
			return doneMsg{pr.URL, err, warn}
		})
	}
	m.msg = fmt.Sprintf("working on %d PR(s)…", len(m.pending))
	if m.pendingSkipped > 0 {
		m.msg += fmt.Sprintf(" (skipped %d)", m.pendingSkipped)
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
		m.logIgnore(pr, wUnignored, e)
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
		m.logIgnore(pr, wIgnored, dep)
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

// logIgnore records an ignore / un-ignore action; every press is its own entry.
func (m *Model) logIgnore(pr adapter.PR, word, dep string) {
	m.log = append(m.log, logEntry{key: fmt.Sprintf("ignore:%d", len(m.log)), pr: pr, word: word, code: cDim, msg: "dependency: " + dep})
}

// Summary is what is printed to stdout after the TUI quits: the session's actions as a table in the
// CLI's fixed-width format followed by a tally line, or "No changes made." The text comes from the
// session log, not from the row states (a reload resets those). It ends with a newline.
func (m *Model) Summary() string {
	if len(m.log) == 0 {
		return "No changes made.\n"
	}
	st := m.cfg.Summary
	var b strings.Builder
	b.WriteString(st.Header() + "\n")
	counts := map[string]int{}
	for _, e := range m.log {
		b.WriteString(st.Row(e.word, e.code, e.pr, e.msg) + "\n")
		counts[e.word]++
	}
	var parts []string
	for _, t := range []struct{ word, label string }{
		{wMerged, "merged"}, {wApproved, "approved"}, {wNotMerged, "not merged"}, {wDeclined, "declined"}, {wCommented, "commented"},
		{wFail, "failed"}, {wIgnored, "ignored"}, {wUnignored, "un-ignored"}, {wWorking, "working"},
	} {
		if n := counts[t.word]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, t.label))
		}
	}
	tally := strings.Join(parts, ", ")
	if counts[wWorking] > 0 {
		tally += " (still running)"
	}
	b.WriteString(tally + "\n")
	return b.String()
}
