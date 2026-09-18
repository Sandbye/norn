package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/plan"
)

// Reading a plan before accepting it.
//
// One line per strand, because the question on this screen is "is this the
// right split", not "what does every brief say". Enter opens the one brief you
// doubt, and `e` hands the file to $EDITOR, which already scrolls and searches
// better than anything drawn here.

// planEditedMsg carries the plan back after $EDITOR closed on it.
type planEditedMsg struct {
	taskID string
	plan   *plan.Plan
	err    error
}

// editPlanCmd opens the plan file in $EDITOR and re-reads it afterwards, so an
// edit made there is what L then acts on.
func editPlanCmd(taskID string) tea.Cmd {
	cmd := config.EditorCommand(plan.Path(taskID))
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return planEditedMsg{taskID: taskID, err: err}
		}
		p, rerr := plan.Read(taskID)
		return planEditedMsg{taskID: taskID, plan: p, err: rerr}
	})
}

// renderPlanView draws the plan of the strand under the cursor.
func (d Dashboard) renderPlanView() string {
	p := d.planRow.Plan
	if p == nil {
		return popover(dimStyle.Render("this strand has no plan"), 46, d.width, d.height)
	}
	width := min(max(d.width-8, 48), 96)
	inner := max(width-10, 30)
	nameW := planNameWidth(*p)
	flagW := planFlagWidth(*p)

	var lines []string
	for i, st := range p.Strands {
		marker, name := "  ", branchStyle.Render(fitCell(st.Role, nameW))
		if i == d.planCursor {
			marker, name = cursorStyle.Render("▸ "), cursorStyle.Render(fitCell(st.Role, nameW))
		}
		lines = append(lines, marker+name+dimStyle.Render(fitCell(truncate(planFlags(st), flagW-2), flagW))+
			truncate(planOpener(st.Brief), max(inner-nameW-flagW-4, 12)))
	}

	// The brief of the one strand you are on, capped. A brief runs to forty
	// lines, which is a document, and a document is what $EDITOR is for.
	if d.planExpand && d.planCursor < len(p.Strands) {
		st := p.Strands[d.planCursor]
		body := wrapText(strings.TrimSpace(st.Brief), inner-4)
		peek := min(planPeek, max(d.height-len(lines)-11, 2))
		lines = append(lines, "", dimStyle.Render("  "+st.Role+" owns"))
		for _, line := range body[:min(peek, len(body))] {
			lines = append(lines, dimStyle.Render("    "+line))
		}
		if len(body) > peek {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("    +%d lines · e opens it in $EDITOR", len(body)-peek)))
		}
	}

	// Last guard: a plan with more strands than the terminal has rows.
	if budget := max(d.height-8, 3); len(lines) > budget {
		lines = append(lines[:budget-1], dimStyle.Render(fmt.Sprintf("  +%d lines · e opens it in $EDITOR", len(lines)-budget+1)))
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("plan · "+d.planRow.Role) +
		dimStyle.Render(fmt.Sprintf("   %d strands", len(p.Strands))) + "\n\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\n" + dimStyle.Render(planHelp(d.planExpand)))
	return popover(b.String(), width, d.width, d.height)
}

// planPeek is how much of a brief the list will show: enough to judge the
// split, never enough to become the document.
const planPeek = 8

// planNameWidth sizes the role column to the longest role, so a plan whose
// roles are named after services does not read as "enqueue-t…".
func planNameWidth(p plan.Plan) int {
	w := 4
	for _, st := range p.Strands {
		w = max(w, len(st.Role))
	}
	return min(w, 22) + 1
}

func planFlagWidth(p plan.Plan) int {
	w := 0
	for _, st := range p.Strands {
		w = max(w, len(planFlags(st)))
	}
	if w == 0 {
		return 0
	}
	return min(w, 26) + 2
}

// planOpener is the first sentence of a brief: what this strand owns, before
// the file list and the decisions.
func planOpener(brief string) string {
	s := oneLine(strings.TrimSpace(brief))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// planFlags is the ordering and proof a strand carries, in one column.
func planFlags(s plan.Strand) string {
	var parts []string
	if s.After != "" {
		parts = append(parts, "after "+s.After)
	}
	if s.Expect != "" {
		parts = append(parts, s.Expect)
	}
	return strings.Join(parts, " · ")
}

func planHelp(expanded bool) string {
	open := "⏎ read one"
	if expanded {
		open = "⏎ collapse"
	}
	return open + " · e $EDITOR · L creates these · esc"
}

// wrapText breaks a brief on word boundaries: prose, and no horizontal scroll.
func wrapText(s string, width int) []string {
	width = max(width, 10)
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}
