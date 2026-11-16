package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"

	"github.com/Flicksell-com/tongs/session"
)

// The head of the screen: a menu bar, as an application has one, and under
// it the buttons that start, stop and step the program.

// menus are the menu bar's menus: for each, the actions it offers, by the
// names do() knows them by, with "-" for a line between two groups.
var menus = []struct {
	name  string
	items []string
}{
	{"Target", []string{"targets", "restart", "stop", "-", "quit"}},
	{"Run", []string{"continue", "next", "step", "finish", "pause", "-", "run_to_here"}},
	{"Breakpoints", []string{"break_here", "condition", "log", "-", "breakpoints", "functions", "side_breaks"}},
	{"Watch", []string{"watch", "watch_word", "watch_clear"}},
	{"View", []string{"files", "tab_next", "tab_prev", "tab_close", "-", "definition", "references", "hover", "-",
		"side_vars", "side_threads", "side", "sizes", "themes"}},
	{"Debugger", []string{"lldb_fresh", "lldb_versions"}},
}

// tall says whether the window can spare the head its full height: three
// rows for the menu bar and three for the buttons, where otherwise each has
// one.
func (m *model) tall() bool { return m.height >= 30 }

// menuH is how many rows the menu bar takes.
func (m *model) menuH() int {
	if m.tall() {
		return 3
	}
	return 1
}

// headH is how many rows the head takes: the menu bar and the buttons, the
// same height each.
func (m *model) headH() int { return 2 * m.menuH() }

// pill is a label as a button rows tall, filled with a colour: with three
// rows it has half a row of the colour over and under the label, which is as
// near to padding as a terminal comes. With no colour it is the label with
// room left over and under it, so that all stand on one line.
func pill(label string, fill lipgloss.Color, rows int) string {
	if rows != 3 {
		return label
	}
	width := lipgloss.Width(label)
	if fill == "" {
		return repeat(" ", width) + "\n" + label + "\n" + repeat(" ", width)
	}
	edge := lipgloss.NewStyle().Foreground(fill)
	return edge.Render(repeat("▄", width)) + "\n" + label + "\n" + edge.Render(repeat("▀", width))
}

// can says whether an action would do anything as the program now stands.
// What can't be done is shown, but not offered.
func (m *model) can(action string) bool {
	switch state := m.snap.State; action {
	case "continue", "next", "step", "finish", "run_to_here":
		return state == session.Stopped
	case "pause":
		return state == session.Running
	case "stop":
		return state == session.Running || state == session.Stopped
	}
	return true
}

// named is an action's name and key, as the menus show them.
func named(action string) (name, key string) {
	for _, a := range actions {
		if a.action == action {
			return a.name, a.key
		}
	}
	return action, ""
}

// menuBar is the top of the screen: the menus at the left, the search at the
// right. A menu's name is a button as tall as the bar, lit while its menu is
// open or the pointer is on it. It says, too, where each name begins, for its
// list to open under.
func (m *model) menuBar() (rows []string, starts []int) {
	height := m.menuH()
	blocks := []string{pill(styAccent.Bold(true).Render("  tongs  "), "", height)}
	width := lipgloss.Width(blocks[0])
	for i, menu := range menus {
		id := fmt.Sprintf("menu:%d", i)
		m.zones = append(m.zones, id)
		label, fill := "  "+menu.name+"  ", lipgloss.Color("")
		switch {
		case m.menuOn && i == m.menu:
			fill = colSurface
		case m.hot == id:
			fill = colRaised
		}
		face := stySub
		if fill != "" {
			face = lipgloss.NewStyle().Background(fill).Foreground(colText).Bold(true)
		}
		starts = append(starts, width)
		width += lipgloss.Width(label)
		blocks = append(blocks, zone.Mark(id, pill(face.Render(label), fill, height)))
	}
	left := lipgloss.JoinHorizontal(lipgloss.Top, blocks...)

	// The search is a button too, at the far end.
	fill := colSurface
	if m.hot == "tool:menu" {
		fill = colRaised
	}
	face := lipgloss.NewStyle().Background(fill)
	search := face.Foreground(colText).Render("   ⌕ Search  ") + face.Foreground(colOverlay).Render("f1   ")
	gap := m.width - width - lipgloss.Width(search) - 1
	row := left
	if gap >= 1 {
		m.zones = append(m.zones, "tool:menu")
		row = lipgloss.JoinHorizontal(lipgloss.Top, left, repeat(" ", gap), zone.Mark("tool:menu", pill(search, fill, height)), " ")
	}
	for _, line := range splitLines(row) {
		rows = append(rows, paint(fit(line, m.width), colCrust))
	}
	return rows, starts
}

// button is one of the buttons under the menu bar.
type button struct {
	action          string
	icon, name, key string
	colour          lipgloss.Color // filled with it, for the ones that start and stop
	on              bool
}

// buttons are the ones to show as the program now stands: what starts it or
// starts it again, what stops it, and what moves it, each lit only while it
// would do something.
func (m *model) buttons() (run, move []button) {
	state := m.snap.State
	switch state {
	case session.Building, session.Starting:
		run = append(run, button{"", spinner[m.frame%len(spinner)], strings.ToUpper(state[:1]) + state[1:], "", "", false})
	case session.Running, session.Stopped:
		run = append(run, button{"restart", "↻", "Restart", "^r", colBlue, true})
	default:
		// Nothing of it is running: this builds it and starts it.
		run = append(run, button{"restart", "▶", "Run", "^r", colGreen, true})
	}
	run = append(run, button{"stop", "■", "Stop", "", colRed, m.can("stop")})
	for _, b := range []button{
		{"continue", "▷", "Continue", "f5", "", false}, {"next", "↷", "Step over", "f6", "", false},
		{"step", "↓", "Step into", "f7", "", false}, {"finish", "↑", "Step out", "f8", "", false},
		{"pause", "⏸", "Pause", "^c", "", false},
	} {
		b.on = m.can(b.action)
		move = append(move, b)
	}
	return run, move
}

// draw is a button rows tall (see pill). form is how much of it is written:
// 2 for its name and key, 1 for its name, 0 for its sign alone.
func (m *model) draw(b button, form, rows int) string {
	id := "tool:" + b.action
	bg, fg, keyFg := colSurface, colText, colOverlay
	switch {
	case !b.on:
		bg, fg, keyFg = mix(colMantle, colSurface, 0.5), colOverlay, mix(colMantle, colOverlay, 0.6)
	case b.colour != "":
		bg, fg, keyFg = b.colour, colCrust, mix(b.colour, colCrust, 0.6)
	}
	if b.on {
		m.zones = append(m.zones, id)
		if m.hot == id {
			bg = mix(bg, colText, 0.25)
		}
	}
	face := lipgloss.NewStyle().Background(bg).Foreground(fg).Bold(b.on)
	label := face.Render(" " + b.icon + " ")
	if form >= 1 {
		label = face.Render("  " + b.icon + " " + b.name + "  ")
	}
	if form == 2 && b.key != "" {
		label = face.Render("  "+b.icon+" "+b.name+" ") + lipgloss.NewStyle().Background(bg).Foreground(keyFg).Render(b.key+"  ")
	}
	label = pill(label, bg, rows)
	if !b.on {
		return label
	}
	return zone.Mark(id, label)
}

// toolBar is the rows under the menu bar: the buttons at the left; at the
// right what is being debugged, how it stands, and what has the keys.
func (m *model) toolBar(rows int) []string {
	mode := styModeAlt.Render(" PROMPT ")
	if m.inSource {
		mode = styMode.Render(" SOURCE ")
	}
	switch m.asking {
	case "":
	case "find":
		mode = styModeAlt.Render(" SEARCH ")
	default:
		mode = styModeAlt.Render(" ASKING ")
	}
	name := "no target"
	if m.snap.Entry != nil {
		name = m.snap.Entry.Name
	}
	dot, colour := "●", colOverlay
	switch m.snap.State {
	case session.Stopped:
		colour = colYellow
	case session.Running:
		dot, colour = spinner[m.frame%len(spinner)], colGreen
	case session.Building, session.Starting:
		dot, colour = spinner[m.frame%len(spinner)], colBlue
	case session.Failed:
		dot, colour = "✖", colRed
	case session.Exited:
		dot = "■"
	}
	info := styText.Bold(true).Render(name) + lipgloss.NewStyle().Foreground(colour).Bold(true).Render("   "+dot+" "+m.snap.State)
	if m.snap.Detail != "" {
		info += stySub.Render("  " + m.snap.Detail)
	}
	if got := int(m.progress.Load()) - 1; got >= 0 {
		done := min(got/10, 10)
		info += styAccent.Render("   debugger "+repeat("▰", done)) + styDim.Render(repeat("▱", 10-done)) + stySub.Render(fmt.Sprintf(" %d%%", got))
	}
	if keys := m.count + m.prefix; keys != "" {
		info += styHit.Render("   " + keys)
	}
	info += "   " + mode + " "

	// The fullest form of the buttons that leaves the rest its room.
	run, move := m.buttons()
	var left string
	zones := len(m.zones)
	for form := 2; form >= 0; form-- {
		m.zones = m.zones[:zones]
		blocks := []string{" "}
		for _, b := range run {
			blocks = append(blocks, m.draw(b, form, rows), " ")
		}
		blocks = append(blocks, "  ")
		for _, b := range move {
			blocks = append(blocks, m.draw(b, form, rows), " ")
		}
		left = lipgloss.JoinHorizontal(lipgloss.Center, blocks...)
		if lipgloss.Width(left)+lipgloss.Width(info)+2 <= m.width {
			break
		}
	}
	if lipgloss.Width(left)+lipgloss.Width(info) > m.width {
		info = "" // The buttons before the words.
	}
	out := splitLines(left)
	for i := range out {
		gap := m.width - lipgloss.Width(out[i])
		if i == len(out)/2 {
			out[i] += repeat(" ", gap-lipgloss.Width(info)) + info
		}
		out[i] = paint(fit(out[i], m.width), colMantle)
	}
	return out
}

// dropdown is the open menu's list, and the cell its corner goes on.
func (m *model) dropdown(starts []int) (box string, x int) {
	if !m.menuOn || m.menu >= len(menus) {
		return "", 0
	}
	items := menus[m.menu].items
	nameW, keyW := 0, 0
	for _, action := range items {
		name, key := named(action)
		nameW, keyW = max(nameW, len([]rune(name))), max(keyW, len([]rune(key)))
	}
	inner := nameW + keyW + 6
	var rows []string
	for i, action := range items {
		if action == "-" {
			rows = append(rows, lipgloss.NewStyle().Foreground(colRaised).Render(repeat("─", inner)))
			continue
		}
		name, key := named(action)
		plain := "  " + name + repeat(" ", inner-4-len([]rune(name))-len([]rune(key))) + key + "  "
		switch {
		case !m.can(action):
			rows = append(rows, styDim.Render(plain))
		case i == m.menuAt:
			m.zones = append(m.zones, fmt.Sprintf("menuitem:%d", i))
			rows = append(rows, zone.Mark(fmt.Sprintf("menuitem:%d", i), stySelect.Render(plain)))
		default:
			rows = append(rows, m.spot(fmt.Sprintf("menuitem:%d", i),
				styText.Render("  "+name)+styDim.Render(repeat(" ", inner-4-len([]rune(name))-len([]rune(key)))+key+"  ")))
		}
	}
	x = max(min(starts[m.menu], m.width-inner-2), 0)
	return framed("", "", joinLines(rows), inner+2, len(rows)+2, false), x
}

// menuStep moves the open menu's cursor to the next thing that can be done,
// down or up, going round.
func (m *model) menuStep(by int) {
	items := menus[m.menu].items
	for at, tries := m.menuAt, 0; tries < len(items); tries++ {
		at = ((at+by)%len(items) + len(items)) % len(items)
		if items[at] != "-" && m.can(items[at]) {
			m.menuAt = at
			return
		}
	}
}

// menuKey is the keys while a menu is open.
func (m *model) menuKey(key string) tea.Cmd {
	switch key {
	case "left", "h", "right", "l":
		by := map[string]int{"left": -1, "h": -1, "right": 1, "l": 1}[key]
		m.menu, m.menuAt = ((m.menu+by)%len(menus)+len(menus))%len(menus), -1
		m.menuStep(1)
	case "down", "j", "tab":
		m.menuStep(1)
	case "up", "k", "shift+tab":
		m.menuStep(-1)
	case "enter", " ":
		items, at := menus[m.menu].items, m.menuAt
		m.menuOn = false
		if at >= 0 && at < len(items) {
			return m.do(items[at])
		}
	default:
		m.menuOn = false
	}
	return nil
}

// menuMouse is the mouse while a menu is open: its list and the bar are live,
// and a press anywhere else closes it.
func (m *model) menuMouse(msg tea.MouseMsg) tea.Cmd {
	kind, n := "", 0
	for _, id := range m.zones {
		if strings.HasPrefix(id, "menu") && zone.Get(id).InBounds(msg) {
			var rest string
			kind, rest, _ = strings.Cut(id, ":")
			n, _ = strconv.Atoi(rest)
			break
		}
	}
	press := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft
	switch {
	case msg.Action == tea.MouseActionMotion && kind == "menu":
		m.menu, m.menuAt = n, -1 // The pointer along the bar opens each in turn.
	case msg.Action == tea.MouseActionMotion && kind == "menuitem":
		m.menuAt = n
	case press && kind == "menuitem":
		items := menus[m.menu].items
		m.menuOn = false
		return m.do(items[n])
	case press && kind == "menu" && n != m.menu:
		m.menu, m.menuAt = n, -1
	case press:
		m.menuOn = false
	}
	return nil
}

// overlay draws a box over the screen with its corner on a cell.
func overlay(screen, box string, x, y, width int) string {
	lines := splitLines(screen)
	for i, row := range splitLines(box) {
		if y+i < 0 || y+i >= len(lines) {
			continue
		}
		row = ansi.Truncate(row, width-x, "") // What the window has no room for is cut.
		lines[y+i] = ansi.Truncate(lines[y+i], x, "") + reset + row + reset +
			ansi.Cut(lines[y+i], x+ansi.StringWidth(row), width)
	}
	return joinLines(lines)
}

// pointer is the escape code that sets the mouse pointer's shape to suit what
// it is over: a hand on what can be clicked, arrows on an edge that can be
// dragged. A terminal that knows nothing of it lets it pass.
func (m *model) pointer() string {
	shape := "default"
	switch edge := m.drag + m.edge; {
	case m.drag == "side", m.drag == "split", m.drag == "" && (edge == "side" || edge == "split"):
		shape = "ew-resize"
	case m.drag == "logs", m.drag == "stack", m.drag == "" && (edge == "logs" || edge == "stack"):
		shape = "ns-resize"
	case m.hot != "":
		shape = "pointer"
	}
	return "\x1b]22;" + shape + "\x1b\\"
}
