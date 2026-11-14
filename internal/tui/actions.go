package tui

import (
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"bbapprove/internal/adapter"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
)

// refresh (r) re-reads the rows the next action would hit with one View each; the rows keep their place,
// the cursor and the marks.
func (m *Model) refresh() tea.Cmd {
	t, _, ok := m.targets()
	if !ok {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(t))
	for _, i := range t {
		pr := m.rows[i].pr
		if m.rows[i].gone {
			continue
		}
		m.refreshing++
		cmds = append(cmds, func() tea.Msg {
			m.sem <- struct{}{}
			defer func() { <-m.sem }()
			v, err := m.cfg.Backend.View(m.cfg.Ctx, pr)
			return refreshedMsg{pr.URL, v, err}
		})
	}
	if len(cmds) == 0 {
		m.msg = "nothing to refresh"
		return nil
	}
	m.msg = fmt.Sprintf("refreshing %d PR(s)…", m.refreshing)
	return tea.Batch(cmds...)
}

func (m *Model) applyRefresh(msg refreshedMsg) {
	r := m.rowByURL(msg.url)
	switch {
	case msg.err != nil:
		ref := msg.url
		if r != nil {
			ref = r.pr.Project + "/" + r.pr.Repo + "#" + fmt.Sprint(r.pr.Number)
		}
		m.refreshFails = append(m.refreshFails, ref+": "+oneLine(msg.err.Error()))
	case r != nil:
		m.update(r, msg.pr)
		m.refreshOK++
	}
	if m.refreshing--; m.refreshing <= 0 {
		m.msg = fmt.Sprintf("refreshed %d PR(s)", m.refreshOK)
		if len(m.refreshFails) > 0 {
			m.msg += fmt.Sprintf(", FAIL %d: %s", len(m.refreshFails), strings.Join(m.refreshFails, "; "))
		}
		m.refreshing, m.refreshOK, m.refreshFails = 0, 0, nil
	}
	m.pruneMarks()
	m.clamp()
}

// update puts a re-read PR on its row. A PR merged or declined elsewhere makes the row gone. Otherwise
// the columns follow the forge, and a stale FAIL, NOT MERGED or APPROVED is cleared only when the PR
// changed, so the outcome of the last action stays readable until there is news. A row with an action
// in flight, or one this session merged or declined, is left alone.
func (m *Model) update(r *row, pr adapter.PR) {
	if r.busy() || r.locked {
		return
	}
	if pr.URL == "" {
		pr.URL = r.pr.URL
	}
	switch pr.State {
	case "MERGED", "DECLINED":
		r.pr, r.gone, r.locked = pr, true, true
		r.result, r.resCode, r.resMsg = pr.State, cDim, strings.ToLower(pr.State)+" elsewhere"
		return
	}
	old := r.pr
	old.State, pr.State = "", ""
	if old == pr {
		return
	}
	r.pr = pr
	switch r.result {
	case wFail, wNotMerged, wApproved:
		r.result, r.resCode, r.resMsg = "", "", ""
	}
}

// reload (R) asks the Backend for the list again. Rows stay in place and are updated from it; PRs that
// are new are appended, so marks (held by row index) stay put; a row the list no longer has is re-read
// with View to tell merged or declined elsewhere from an error.
func (m *Model) reload() tea.Cmd {
	if m.reloading {
		m.msg = "already reloading…"
		return nil
	}
	m.reloading = true
	m.msg = "reloading…"
	var known []adapter.PR
	for _, r := range m.rows {
		if !r.locked {
			known = append(known, r.pr)
		}
	}
	return func() tea.Msg {
		prs, err := m.cfg.Backend.List(m.cfg.Ctx)
		if err != nil {
			return reloadedMsg{err: err}
		}
		listed := map[string]bool{}
		for _, pr := range prs {
			listed[pr.URL] = true
		}
		var missing []adapter.PR
		for _, pr := range known {
			if !listed[pr.URL] {
				missing = append(missing, pr)
			}
		}
		gone, goneErr := map[string]adapter.PR{}, map[string]error{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, pr := range missing {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.sem <- struct{}{}
				defer func() { <-m.sem }()
				v, err := m.cfg.Backend.View(m.cfg.Ctx, pr)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					goneErr[pr.URL] = err
				} else {
					gone[pr.URL] = v
				}
			}()
		}
		wg.Wait()
		return reloadedMsg{prs: prs, gone: gone, goneErr: goneErr}
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
	byURL := map[string]int{}
	for i, r := range m.rows {
		byURL[r.pr.URL] = i
	}
	added := 0
	for _, pr := range msg.prs {
		if i, ok := byURL[pr.URL]; ok {
			m.update(&m.rows[i], pr)
			continue
		}
		m.rows = append(m.rows, row{pr: pr})
		added++
	}
	gone, failed := 0, 0
	for url, pr := range msg.gone {
		if i, ok := byURL[url]; ok {
			m.update(&m.rows[i], pr)
			if m.rows[i].gone {
				gone++
			}
		}
	}
	failed = len(msg.goneErr)
	m.reloadIgnored()
	for n, i := range m.visible() {
		if m.rows[i].pr.URL == curURL {
			m.cursor = n
			break
		}
	}
	m.hover = -1
	if m.mode != modeList { // a prompt's pending rows may have changed under it
		m.mode, m.pending, m.input = modeList, nil, nil
	}
	m.pruneMarks()
	m.clamp()
	m.msg = fmt.Sprintf("reloaded %d PRs", len(msg.prs))
	if added > 0 {
		m.msg += fmt.Sprintf(", %d new", added)
	}
	if gone > 0 {
		m.msg += fmt.Sprintf(", %d closed elsewhere", gone)
	}
	if failed > 0 {
		m.msg += fmt.Sprintf(", %d not re-read", failed)
	}
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
