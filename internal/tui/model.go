package tui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"bbapprove/internal/adapter"
	"bbapprove/internal/app"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
	"bbapprove/internal/style"
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

type row struct {
	pr      adapter.PR
	result  string // non-empty after an action: APPROVING…, MERGED, NOT MERGED, DECLINED, COMMENTED, FAIL ...
	resCode string
	resMsg  string
	// locked is set by a merge or decline that went through: the row is no longer eligible until a
	// reload. A failed attempt leaves the row open so it can be retried; a comment never locks.
	locked bool
	// gone is set when a refresh found the PR merged or declined elsewhere: the row stays, dimmed, locked
	// and unmarkable, so the list does not shift under the cursor and the change is visible.
	gone bool
}

// failed reports a failed approve, merge or decline: the row is open for a retry and stays listed.
func (r row) failed() bool {
	return r.result == wFail || r.result == wNotMerged
}

func (r row) busy() bool {
	return r.result == wApproving || r.result == wDeclining || r.result == wCommenting
}

// logEntry is one completed (or still running) action of this session, kept for the exit summary.
// Entries survive reloads. One entry per PR and verb; a repeat replaces it.
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

// reloadedMsg is the answer to R: the open PRs, plus a View of each listed row the list no longer has
// (gone holds the answers, goneErr the failures).
type reloadedMsg struct {
	prs     []adapter.PR
	err     error
	gone    map[string]adapter.PR
	goneErr map[string]error
}

// refreshedMsg is one PR re-read by r.
type refreshedMsg struct {
	url string
	pr  adapter.PR
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
	// refreshing counts the r re-reads still out; refreshFails collects their errors for the status bar.
	refreshing   int
	refreshOK    int
	refreshFails []string
	hover        int // visible index under the mouse, -1 for none
	input        []rune
	inputKind    inputKind
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
	case refreshedMsg:
		m.applyRefresh(msg)
	case tea.MouseMsg:
		m.mouse(msg)
	case doneMsg:
		var pe *PartialError
		word, code, text := wMerged, cGreen+cBold, ""
		switch {
		case msg.err == nil:
			// Merged, with the adapter's note (e.g. branch not deleted) as the reason; the MERGED word already
			// says "merge ok", so the long row only keeps the part that is news.
			text = strings.TrimPrefix(style.OneLine(msg.warning), "merge ok, but ")
		case errors.As(msg.err, &pe):
			word, code, text = wNotMerged, cRed, style.OneLine(msg.err.Error())
		default:
			word, code, text = wFail, cRed, style.OneLine(msg.err.Error())
		}
		m.finish(msg.url, "approve", word, code, text, true)
	case approveDoneMsg:
		word, code, text := wApproved, cGreen, ""
		if msg.err != nil {
			word, code, text = wFail, cRed, style.OneLine(msg.err.Error())
		}
		m.finish(msg.url, "approve", word, code, text, false)
	case declineDoneMsg:
		word, code, text := wDeclined, cRed, ""
		if msg.err != nil {
			word, code, text = wFail, cRed, style.OneLine(msg.err.Error())
		}
		m.finish(msg.url, "decline", word, code, text, true)
	case commentDoneMsg:
		if msg.err == nil {
			m.cmtOK++
			m.finish(msg.url, "comment", wCommented, cGreen, "", false)
		} else {
			m.cmtFails = append(m.cmtFails, msg.ref+": "+style.OneLine(msg.err.Error()))
			m.finish(msg.url, "comment", wFail, cRed, style.OneLine(msg.err.Error()), false)
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

func (m *Model) rowByURL(url string) *row {
	for i := range m.rows {
		if m.rows[i].pr.URL == url {
			return &m.rows[i]
		}
	}
	return nil
}

// logIgnore records an ignore / un-ignore action; every press is its own entry.
func (m *Model) logIgnore(pr adapter.PR, word, dep string) {
	m.log = append(m.log, logEntry{key: fmt.Sprintf("ignore:%d", len(m.log)), pr: pr, word: word, code: cDim, msg: "dependency: " + dep})
}

// Summary is what is printed to stdout after the TUI quits: the session's actions as a table in the
// CLI's fixed-width format followed by a tally line, or "No changes made." The text comes from the
// session log, not from the row states. It ends with a newline.
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
