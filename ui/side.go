package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"

	"github.com/Flicksell-com/tongs/agent"
	"github.com/Flicksell-com/tongs/session"
)

// The side column: the stack, and under it one pane that is the variables,
// the breakpoints or the threads, by its tabs.

var sideTabs = []struct{ key, name string }{{"vars", "variables"}, {"breaks", "breakpoints"}, {"threads", "threads"}}

// spot makes text a place to click, by the name the click is looked up by.
// What the pointer is over is picked out.
func (m *model) spot(id, text string) string {
	m.zones = append(m.zones, id)
	if id == m.hot {
		text = styHover.Render(ansi.Strip(text))
	}
	return zone.Mark(id, text)
}

func (m *model) sidePanes(width, height int) string {
	stackH := min(max(height/3, 4), 9)
	if m.stackSet > 0 {
		stackH = max(min(m.stackSet, height-3), 3)
	}
	if height < 8 {
		stackH = 0 // No room for both: the pane with the tabs has it all.
	}
	m.stackH = stackH
	var stack []string
	stackTitle := "stack"
	if view := m.snap.View; view != nil {
		for i, f := range view.Frames {
			row := fmt.Sprintf("#%d %s", i, strings.SplitN(f.Name, "(", 2)[0])
			if f.Path != "" {
				row += styDim.Render(fmt.Sprintf("  %s:%d", filepath.Base(f.Path), f.Line))
			}
			if i == view.Index {
				row = stySelect.Render(fit("❯ "+ansi.Strip(row), width-2))
			} else {
				row = "  " + row
			}
			stack = append(stack, m.spot(fmt.Sprintf("frame:%d", i), fit(row, width-2)))
		}
		if view.Index != m.stackAt {
			// A newly chosen frame is brought into view; after that the wheel is free.
			m.stackAt = view.Index
			m.stackTop = min(max(m.stackTop, view.Index-(stackH-2)+1), view.Index)
		}
		for _, thread := range view.Threads {
			if thread.Selected && len(view.Threads) > 1 {
				stackTitle = fmt.Sprintf("stack of thread %d", thread.Index)
			}
		}
	}
	stackTitle, stack = window(stackTitle, stack, &m.stackTop, stackH-2)

	var rows []string
	right := ""
	switch m.sideTab {
	case "breaks":
		rows = m.breakRows(width - 2)
	case "threads":
		rows = m.threadRows(width - 2)
	default:
		rows = m.varRows(width - 2)
		right = m.spot("watch:add", styDim.Render(" + watch "))
	}
	label := ""
	for _, t := range sideTabs {
		name := t.name
		switch {
		case t.key == "breaks" && len(m.snap.Breaks) > 0:
			name += fmt.Sprintf(" %d", len(m.snap.Breaks))
		case t.key == "threads" && m.snap.View != nil && len(m.snap.View.Threads) > 1:
			name += fmt.Sprintf(" %d", len(m.snap.View.Threads))
		}
		if width < 46 && t.key != m.sideTab {
			name = strings.Fields(strings.NewReplacer("variables", "vars", "breakpoints", "breaks").Replace(name))[0]
		}
		if t.key == m.sideTab {
			label += styText.Bold(true).Render(" " + name + " ")
		} else {
			label += m.spot("side:"+t.key, styDim.Render(" "+name+" "))
		}
	}
	lowerH := height - stackH
	title, rows := window("", rows, &m.sideTop, lowerH-2)
	if title != "" && right == "" {
		right = styDim.Render(title + " ")
	}
	lower := zone.Mark("locals", framed(label, right, block(rows, width-2, lowerH-2), width, lowerH, false))
	if stackH == 0 {
		return lower
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		zone.Mark("stack", pane(stackTitle, block(stack, width-2, stackH-2), width, stackH, false)), lower)
}

// ---- variables ----

// localRow is one line of the variables: a heading, a watched expression, a
// local, or a part of one opened out.
type localRow struct {
	v     session.Var
	depth int
	path  string // the names leading to it, for remembering what is open
	note  string // a line that is a remark, not a variable
	watch int    // for a watched expression, which one, from 1
}

func (m *model) varRows(width int) []string {
	var rows []string
	if len(m.locals) == 0 {
		// Watches are worth seeing even with nothing stopped to work them out in.
		for i, expression := range m.snap.Watches {
			text := "  " + styAccent.Render(expression) + styDim.Render(" = …")
			rows = append(rows, fit(text, width-2)+m.spot(fmt.Sprintf("unwatch:%d", i), styDim.Render(" ×")))
		}
		return rows
	}
	for i, row := range m.locals {
		indent := repeat("  ", row.depth)
		if row.note != "" {
			rows = append(rows, indent+styDim.Render(row.note))
			continue
		}
		mark := "  "
		if row.v.Ref != 0 {
			mark = "▸ "
			if m.expanded[row.path] {
				mark = "▾ "
			}
		}
		value := styText.Render(row.v.Value)
		switch {
		case row.v.Err:
			value = styError.Render(row.v.Value)
		case m.changed(row.path):
			value = styChanged.Render(row.v.Value)
		}
		text := indent + styDim.Render(mark) + styAccent.Render(row.v.Name) + styDim.Render(" = ") + value
		if row.watch == 0 {
			if row.v.Ref != 0 {
				text = m.spot(fmt.Sprintf("local:%d", i), fit(text, width))
			}
			rows = append(rows, text)
			continue
		}
		text = fit(text, width-2)
		if row.v.Ref != 0 {
			text = m.spot(fmt.Sprintf("local:%d", i), text)
		}
		rows = append(rows, text+m.spot(fmt.Sprintf("unwatch:%d", row.watch-1), styDim.Render(" ×")))
	}
	return rows
}

// vals is what the variables of one function were at the stop before the
// last one it was seen at, and at the last.
type vals struct {
	run       int
	base, now map[string]string
}

// scope names the frame shown: its function, and how deep in the stack.
func scope(view *session.View) string {
	return fmt.Sprintf("%s/%d", view.Frames[view.Index].Name, len(view.Frames)-view.Index)
}

// took notes the values just listed, to say at the next stop which changed.
func (m *model) took(rows []localRow) {
	view := m.snap.View
	if view == nil {
		return
	}
	key := scope(view)
	seen := m.vals[key]
	if seen == nil {
		seen = &vals{run: view.Run, now: map[string]string{}}
		m.vals[key] = seen
	}
	if seen.run != view.Run {
		// The program has run since: what was, is what to compare with.
		seen.base, seen.now, seen.run = seen.now, map[string]string{}, view.Run
	}
	for _, row := range rows {
		if row.note == "" {
			seen.now[row.path] = row.v.Value
		}
	}
}

// changed says whether a variable is not what it was when the program last
// stopped in this function.
func (m *model) changed(path string) bool {
	view := m.snap.View
	if view == nil {
		return false
	}
	seen := m.vals[scope(view)]
	if seen == nil || seen.base == nil {
		return false
	}
	was, had := seen.base[path]
	now, has := seen.now[path]
	return had && has && was != now
}

// notes are the values to write beside the source: for each line on screen
// that the program has come past in this frame, the variables it mentions.
func (m *model) notes(pc int) map[int]string {
	view := m.snap.View
	if view == nil || pc == 0 {
		return nil
	}
	notes := map[int]string{}
	v := &m.src
	for line := v.top; line < v.top+v.height && line <= len(v.plain) && line <= pc; line++ {
		var parts []string
		for _, local := range view.Locals {
			// From where it is declared on: an earlier line that says the
			// same name means something else by it.
			if decl := view.Decl[local.Name]; decl == 0 || line < decl || !mentions(v.plain[line-1], local.Name) {
				continue
			}
			parts = append(parts, local.Name+" = "+truncate(local.Value, 40))
		}
		if len(parts) > 0 {
			notes[line] = strings.Join(parts, "   ")
		}
	}
	return notes
}

// mentions says whether a line has name in it as a whole word.
func mentions(line []rune, name string) bool {
	want := []rune(name)
	for at := 0; at+len(want) <= len(line); at++ {
		if string(line[at:at+len(want)]) != name {
			continue
		}
		if (at == 0 || !wordy(line[at-1])) && (at+len(want) == len(line) || !wordy(line[at+len(want)])) {
			return true
		}
	}
	return false
}

// ---- breakpoints ----

// breakIDs are the breakpoints in the order they are listed.
func (m *model) breakIDs() []int {
	var ids []int
	for id := range m.snap.Breaks {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// breakMark is a breakpoint's mark, in the margin and in the list: round for
// one that stops, a diamond for one with a condition, an arrow for one that
// prints and runs on, hollow for one turned off.
func breakMark(point session.Break) string {
	switch {
	case !point.Enabled:
		return styDim.Render("○")
	case point.Log != "":
		return lipgloss.NewStyle().Foreground(colMauve).Render("◆")
	case point.Condition != "":
		return lipgloss.NewStyle().Foreground(colYellow).Render("◆")
	}
	return styError.Render("●")
}

func (m *model) breakRows(width int) []string {
	ids := m.breakIDs()
	if len(ids) == 0 {
		return []string{"", styDim.Render("  none: space on a line sets one,"), styDim.Render("  or a click in its margin")}
	}
	var rows []string
	for _, id := range ids {
		point := m.snap.Breaks[id]
		text := fmt.Sprintf("%s:%d", agent.Rel(m.snap.Root, point.Path), point.Line)
		if width < 60 {
			text = fmt.Sprintf("%s:%d", filepath.Base(point.Path), point.Line)
		}
		if !point.Enabled {
			text = styDim.Render(text)
		}
		if point.Hits > 0 {
			text += styDim.Render(fmt.Sprintf("  ×%d", point.Hits))
		}
		if point.Condition != "" {
			text += styDim.Render("  if ") + lipgloss.NewStyle().Foreground(colYellow).Render(point.Condition)
		}
		if point.Log != "" {
			text += styDim.Render("  prints ") + lipgloss.NewStyle().Foreground(colMauve).Render(point.Log)
		}
		buttons := m.spot(fmt.Sprintf("bif:%d", id), styDim.Render(" if")) +
			m.spot(fmt.Sprintf("blog:%d", id), styDim.Render(" print")) +
			m.spot(fmt.Sprintf("bdel:%d", id), styDim.Render(" × "))
		rows = append(rows, m.spot(fmt.Sprintf("bon:%d", id), breakMark(point)+" ")+
			m.spot(fmt.Sprintf("bgo:%d", id), fit(text, width-2-12))+buttons)
	}
	return rows
}

// ---- threads ----

func (m *model) threadRows(width int) []string {
	view := m.snap.View
	if view == nil {
		return []string{"", styDim.Render("  listed when the program is stopped")}
	}
	var rows []string
	for _, thread := range view.Threads {
		row := fmt.Sprintf("#%d %s", thread.Index, thread.Where)
		if thread.Reason != "" {
			row += "  " + thread.Reason
		}
		if thread.Selected {
			row = stySelect.Render(fit("❯ "+row, width))
		} else {
			row = "  " + fmt.Sprintf("#%d %s", thread.Index, thread.Where) + styDim.Render("  "+thread.Reason)
		}
		rows = append(rows, m.spot(fmt.Sprintf("thread:%d", thread.Index), fit(row, width)))
	}
	return rows
}

// ---- the pointer ----

// tip is the value shown by the pointer for the name it rests on.
type tip struct {
	x, y int    // the pointer's cell when it came to rest on the name
	line int    // of the source
	expr string // what is asked about
	text string // what to show, once known; "" before, and where there is nothing
	seq  int
}

type (
	tipDueMsg struct{ seq int }
	tipMsg    struct {
		seq   int
		value session.Var
		ok    bool
	}
)

// pointed takes the pointer moving with no button down: what it is over is
// picked out, and a name in the source it rests on is asked the value of.
func (m *model) pointed(msg tea.MouseMsg) tea.Cmd {
	m.hot = ""
	for _, id := range m.zones {
		if zone.Get(id).InBounds(msg) {
			m.hot = id
			break
		}
	}
	view, z := m.snap.View, zone.Get("source")
	if view == nil || m.src.path == "" || view.Frames[view.Index].Path != m.src.path || !z.InBounds(msg) {
		m.tip = nil
		return nil
	}
	x, y := z.Pos(msg)
	line, col := m.src.top+y-1, x-1-m.src.gutter()+m.src.left
	expr := ""
	if y >= 1 {
		expr = m.src.chain(line, col)
	}
	if expr == "" {
		m.tip = nil
		return nil
	}
	if m.tip != nil && m.tip.line == line && m.tip.expr == expr {
		return nil
	}
	m.tipSeq++
	seq := m.tipSeq
	m.tip = &tip{x: msg.X, y: msg.Y, line: line, expr: expr, seq: seq}
	// Asked only once the pointer has rested: one crossing the code names a
	// great many things on its way.
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return tipDueMsg{seq} })
}

func (m *model) tipDue(msg tipDueMsg) tea.Cmd {
	if m.tip == nil || m.tip.seq != msg.seq {
		return nil
	}
	expr := m.tip.expr
	return func() tea.Msg {
		value, ok := m.s.Value(expr)
		return tipMsg{msg.seq, value, ok}
	}
}

func (m *model) tipped(msg tipMsg) {
	if m.tip == nil || m.tip.seq != msg.seq || !msg.ok {
		return
	}
	m.tip.text = m.tip.expr + " = " + msg.value.Value
	if msg.value.Type != "" {
		m.tip.text += "   " + msg.value.Type
	}
}

// withTip draws the tip over the screen, under the pointer or, at the foot
// of the screen, over it.
func (m *model) withTip(screen string) string {
	if m.tip == nil || m.tip.text == "" {
		return screen
	}
	text := styTip.Render(" " + truncate(m.tip.text, min(m.width-4, 76)) + " ")
	y := m.tip.y + 1
	if y >= m.height {
		y = m.tip.y - 1
	}
	x := max(min(m.tip.x, m.width-lipgloss.Width(text)), 0)
	lines := splitLines(screen)
	if y < 0 || y >= len(lines) {
		return screen
	}
	const reset = "\x1b[0m"
	lines[y] = ansi.Truncate(lines[y], x, "") + reset + text + reset + ansi.Cut(lines[y], x+lipgloss.Width(text), m.width)
	return joinLines(lines)
}
