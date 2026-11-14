package tui

import (
	"fmt"
)

func (m *Model) toggleMark(i int) {
	if !m.markable(i) {
		m.msg = "refused: " + m.rows[i].resMsg
		return
	}
	if m.marks[i] {
		delete(m.marks, i)
	} else {
		m.marks[i] = true
	}
}

// markable reports whether row i may carry a mark: not a PR merged or declined elsewhere.
func (m *Model) markable(i int) bool { return !m.rows[i].gone }

// pruneMarks drops marks on rows that are not visible (rows that just became hidden IGNORED rows).
func (m *Model) pruneMarks() {
	vis := map[int]bool{}
	for _, i := range m.visible() {
		vis[i] = m.markable(i)
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
	if m.rows[i].gone {
		m.msg = "refused: " + m.rows[i].resMsg
		return nil, false, false
	}
	return []int{i}, false, true
}

// unmark drops the mark of a row a batch action just acted on; instant actions never touch marks.
func (m *Model) unmark(i int) {
	if m.pendingBatch {
		delete(m.marks, i)
	}
}
