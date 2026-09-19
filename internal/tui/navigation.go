package tui

// Moving through two columns.
//
// The rail used to be one flat list, so one index was the whole truth. With
// tasks on the left and their strands on the right, a single index means `j`
// walks strands nobody can see while the left column sits still, and then jumps
// a whole task at once. Navigation has to move what the eye is on.
//
// The cursor is still a row index, because every action key acts on a row.
// These helpers are what keep that index and the two columns saying the same
// thing.

type paneFocus int

const (
	focusTasks paneFocus = iota // the left column: which task
	focusStrands
)

// leftEntry is one line of the left column: a task, or a worktree that belongs
// to no task.
type leftEntry struct {
	taskID string  // "" for a loose worktree
	row    dashRow // the loose worktree, or the task's first row
	rows   []dashRow
}

// leftEntries is the left column in the order it is drawn.
func leftEntries(vis []dashRow) []leftEntry {
	var out []leftEntry
	at := map[string]int{}
	for _, r := range vis {
		if r.TaskID == "" {
			out = append(out, leftEntry{row: r})
			continue
		}
		if i, seen := at[r.TaskID]; seen {
			out[i].rows = append(out[i].rows, r)
			continue
		}
		at[r.TaskID] = len(out)
		out = append(out, leftEntry{taskID: r.TaskID, row: r, rows: []dashRow{r}})
	}
	// Tasks first, then loose worktrees, each keeping the order it arrived in.
	// A task is what you came to work on; a loose worktree is one you left.
	var tasks, loose []leftEntry
	for _, e := range out {
		if e.taskID == "" {
			loose = append(loose, e)
			continue
		}
		tasks = append(tasks, e)
	}
	return append(tasks, loose...)
}

// entryOf is the left-column entry the cursor's row belongs to.
func entryOf(entries []leftEntry, row dashRow) int {
	for i, e := range entries {
		if e.taskID != "" && e.taskID == row.TaskID {
			return i
		}
		if e.taskID == "" && e.row.Path == row.Path {
			return i
		}
	}
	return 0
}

// moveLeftColumn walks tasks and loose worktrees, and lands on the row that
// entry stands for: the strand you were last on in that task, or its first.
func (d Dashboard) moveLeftColumn(vis []dashRow, delta int) Dashboard {
	entries := leftEntries(vis)
	if len(entries) == 0 {
		return d
	}
	at := 0
	if d.cursor >= 0 && d.cursor < len(vis) {
		at = entryOf(entries, vis[d.cursor])
	}
	next := clampIndex(at+delta, len(entries)-1)
	d.cursor = rowIndex(vis, d.rowFor(entries[next]))
	return d
}

// rowFor is the row an entry stands for: the strand last selected in that task
// if it is still there, otherwise the task's first row.
func (d Dashboard) rowFor(e leftEntry) dashRow {
	if e.taskID == "" {
		return e.row
	}
	if path := d.strandSel[e.taskID]; path != "" {
		for _, r := range e.rows {
			if r.Path == path {
				return r
			}
		}
	}
	return e.rows[0]
}

// moveStrand walks the strands of the task the cursor is in, and stops at
// either end rather than sliding into the next task: crossing a task boundary
// is what the left column is for.
func (d Dashboard) moveStrand(vis []dashRow, delta int) Dashboard {
	if d.cursor < 0 || d.cursor >= len(vis) {
		return d
	}
	cur := vis[d.cursor]
	if cur.TaskID == "" {
		// A loose worktree has no strands, so movement is the left column's.
		return d.moveLeftColumn(vis, delta)
	}
	entries := leftEntries(vis)
	rows := entries[entryOf(entries, cur)].rows
	at := 0
	for i, r := range rows {
		if r.Path == cur.Path {
			at = i
		}
	}
	next := clampIndex(at+delta, len(rows)-1)
	d.cursor = rowIndex(vis, rows[next])
	d.rememberStrand(rows[next])
	return d
}

// rememberStrand keeps a task's selected strand, so leaving a task and coming
// back does not send you to the top of it.
func (d *Dashboard) rememberStrand(r dashRow) {
	if r.TaskID == "" {
		return
	}
	if d.strandSel == nil {
		d.strandSel = map[string]string{}
	}
	d.strandSel[r.TaskID] = r.Path
}

// clampIndex keeps a move inside the column it is moving in.
func clampIndex(v, hi int) int {
	switch {
	case v < 0:
		return 0
	case v > hi:
		return hi
	}
	return v
}

func rowIndex(vis []dashRow, row dashRow) int {
	for i, r := range vis {
		if r.Path == row.Path {
			return i
		}
	}
	return 0
}
