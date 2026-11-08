package tui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
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
}

const (
	cReset  = "\033[0m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBold   = "\033[1m"
)

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

type doneMsg struct {
	idx int
	err error
}

type declineDoneMsg struct {
	idx int
	err error
}

type commentDoneMsg struct {
	idx int
	err error
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
	input       []rune
	inputKind   inputKind
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
	m := &Model{cfg: cfg, sem: make(chan struct{}, cfg.Concurrency), marks: map[int]bool{}, loading: true, height: 24}
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

func (m *Model) helpLines() []string {
	if m.showHelp {
		return []string{
			"↑/↓ j/k move   x/space mark   X mark all eligible   u/ctrl+d clear marks",
			"a approve+merge (marked, else current)   i ignore/un-ignore dependency",
			"d decline (confirm, then optional comment)   c comment (marked, else current)",
			". show/hide ignored   o open in browser   ? less help   q quit",
		}
	}
	return []string{
		"↑↓ move  x mark  X all  u clear  a approve+merge  d decline  c comment",
		"i ignore  . ignored  o open  ? help  q quit",
	}
}

func (m *Model) bodyHeight() int {
	h := m.height - 2 - len(m.helpLines())
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
	case doneMsg:
		r := &m.rows[msg.idx]
		var pe *PartialError
		switch {
		case msg.err == nil:
			r.result, r.resCode, r.resMsg = "MERGED", cGreen+cBold, ""
		case errors.As(msg.err, &pe):
			r.result, r.resCode, r.resMsg = "APPROVED, NOT MERGED", cRed, oneLine(msg.err.Error())
		default:
			r.result, r.resCode, r.resMsg = "FAIL", cRed, oneLine(msg.err.Error())
		}
		m.clamp()
	case declineDoneMsg:
		r := &m.rows[msg.idx]
		if msg.err == nil {
			r.result, r.resCode, r.resMsg = "DECLINED", cRed, ""
		} else {
			r.result, r.resCode, r.resMsg = "FAIL", cRed, oneLine(msg.err.Error())
		}
		m.clamp()
	case commentDoneMsg:
		if msg.err == nil {
			m.cmtOK++
		} else {
			m.cmtFails = append(m.cmtFails, m.ref(msg.idx)+": "+oneLine(msg.err.Error()))
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
			if m.marks[i] {
				delete(m.marks, i)
			} else {
				m.marks[i] = true
			}
		}
	case "X":
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
		m.askApprove()
	case "d":
		m.askDecline()
	case "c":
		m.askComment()
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
	return m, nil
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

// anyStatusTargets returns the marked visible rows (any status), else the current row. Rows with an
// action in flight are skipped.
func (m *Model) anyStatusTargets() []int {
	var t []int
	for _, i := range m.visible() {
		if m.marks[i] && m.rows[i].result != "WORKING" {
			t = append(t, i)
		}
	}
	if len(t) > 0 {
		return t
	}
	if m.markedVisible() > 0 {
		return nil
	}
	if i, ok := m.current(); ok && m.rows[i].result != "WORKING" {
		return []int{i}
	}
	return nil
}

func (m *Model) askDecline() {
	t := m.anyStatusTargets()
	if len(t) == 0 {
		m.msg = "nothing to decline"
		return
	}
	m.pending, m.mode = t, modeConfirmDecline
}

func (m *Model) askComment() {
	t := m.anyStatusTargets()
	if len(t) == 0 {
		m.msg = "nothing to comment on"
		return
	}
	m.pending = t
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
		delete(m.marks, i)
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			return declineDoneMsg{i, m.cfg.Backend.Decline(m.cfg.Ctx, pr, comment)}
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
		delete(m.marks, i) // avoid double-sending; the row's status word is left untouched
		m.cmtOutstanding++
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			return commentDoneMsg{i, m.cfg.Backend.Comment(m.cfg.Ctx, pr, body)}
		})
	}
	m.msg = fmt.Sprintf("commenting on %d PR(s)…", len(m.pending))
	m.pending = nil
	return tea.Batch(cmds...)
}

func (m *Model) askApprove() {
	var targets []int
	if len(m.marks) > 0 {
		for i := range m.marks {
			if m.eligible(i) {
				targets = append(targets, i)
			}
		}
		sort.Ints(targets)
		if len(targets) == 0 {
			m.msg = "no marked PR is eligible"
			return
		}
	} else {
		i, ok := m.current()
		if !ok {
			return
		}
		if w, _, why := m.status(m.rows[i]); w != "ELIGIBLE" {
			if why == "" {
				why = strings.ToLower(w)
			}
			m.msg = "refused: not eligible (" + why + ")"
			return
		}
		targets = []int{i}
	}
	m.pending = targets
	m.mode = modeConfirm
}

func (m *Model) runPending() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.pending))
	for _, i := range m.pending {
		i, pr := i, m.rows[i].pr
		m.rows[i].result, m.rows[i].resCode, m.rows[i].resMsg = "WORKING", cYellow, ""
		delete(m.marks, i)
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			return doneMsg{i, m.cfg.Backend.ApproveAndMerge(m.cfg.Ctx, pr)}
		})
	}
	m.msg = fmt.Sprintf("working on %d PR(s)…", len(m.pending))
	m.pending = nil
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

func (m *Model) renderRow(idx int, selected bool) string {
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
		mark = "▌"
	}
	return m.fit(cur + mark + line)
}

// visibleInput returns the tail of the input that fits next to the prompt so the cursor stays visible.
func (m *Model) visibleInput(prompt string) string {
	in := string(m.input)
	if m.width <= 0 {
		return in
	}
	room := m.width - ansi.StringWidth(prompt) - 1
	if room < 1 {
		room = 1
	}
	r := []rune(in)
	for len(r) > 0 && ansi.StringWidth(string(r)) > room {
		r = r[1:]
	}
	return string(r)
}

// View renders the whole screen.
func (m *Model) View() string {
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
			b.WriteString(m.renderRow(v[m.top+n], m.top+n == m.cursor))
		} else if len(v) == 0 && n == 0 {
			b.WriteString(cDim + "no pull requests" + cReset)
		}
		b.WriteString("\n")
	}
	status := m.msg
	switch m.mode {
	case modeConfirm:
		status = fmt.Sprintf("approve+merge %d PR(s)? y/N", len(m.pending))
	case modeConfirmDecline:
		status = fmt.Sprintf("decline %d PR(s)? y/N", len(m.pending))
	case modeInput:
		status = m.inputPrompt() + m.visibleInput(m.inputPrompt()) + "█"
	}
	if status == "" {
		status = fmt.Sprintf("%d PRs, %d marked", len(v), m.markedVisible())
	}
	b.WriteString(m.fit(cBold+status+cReset) + "\n")
	// No trailing newline: a View of exactly m.height lines plus "\n" would make Bubble Tea
	// drop the first line (the header) in the alt screen.
	for n, l := range m.helpLines() {
		if n > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.fit(cDim + l + cReset))
	}
	return b.String()
}
