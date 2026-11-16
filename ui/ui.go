// Package ui is the terminal screen: the source where the program is, its
// stack and locals, the lldb log, and a prompt. It is a debugger's console,
// not an editor: the source pane is for looking and for placing breakpoints,
// and everything else is typed at the prompt or picked from one palette.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/Flicksell-com/tongs/agent"
	"github.com/Flicksell-com/tongs/lldb"
	"github.com/Flicksell-com/tongs/nav"
	"github.com/Flicksell-com/tongs/session"
	"github.com/Flicksell-com/tongs/store"
)

const (
	promptHint  = "lldb command   (enter repeats the last, tab completes, esc to the source, f1 menu)"
	sideAt      = 100 // columns from which stack and locals fit beside the source
	splitLogsAt = 90  // columns from which the two logs fit side by side
	partsShown  = 100 // parts of one variable listed when it is opened
	choicesMax  = 6
	historyKept = 300  // commands remembered for a target between runs
	logKept     = 3000 // lines of each log kept on screen
)

// What the menu offers: the name, the key, and what do() is given.
var actions = []struct{ name, key, action string }{
	{"Continue", "f5", "continue"},
	{"Step over", "f6", "next"},
	{"Step in", "f7", "step"},
	{"Step out", "f8", "finish"},
	{"Pause", "ctrl+c", "pause"},
	{"Rebuild and restart", "ctrl+r", "restart"},
	{"Choose what to debug", "ctrl+t", "targets"},
	{"Open a file", "ctrl+p", "files"},
	{"Find a function and break there", "ctrl+n", "functions"},
	{"List breakpoints", "ctrl+b", "breakpoints"},
	{"Watch an expression", "", "watch"},
	{"Watch what is under the cursor", "=", "watch_word"},
	{"Watches: stop watching all", "", "watch_clear"},
	{"Go to definition", "gd", "definition"},
	{"Find references", "gr", "references"},
	{"What is this: type and declaration", "K", "hover"},
	{"Breakpoint on the cursor line", "space", "break_here"},
	{"Breakpoint here: stop only when…", "C", "condition"},
	{"Breakpoint here: print and run on", "P", "log"},
	{"Run to the cursor line", "R", "run_to_here"},
	{"Next file", "gt", "tab_next"},
	{"Previous file", "gT", "tab_prev"},
	{"Close this file", "", "tab_close"},
	{"Show the variables", "", "side_vars"},
	{"Show the breakpoints", "", "side_breaks"},
	{"Show the threads", "", "side_threads"},
	{"Stack and variables: show or hide", "", "side"},
	{"Panes: back to their usual sizes", "", "sizes"},
	{"Colours: choose a theme", "", "themes"},
	{"Debugger: download it again", "", "lldb_fresh"},
	{"Debugger: choose a version", "", "lldb_versions"},
	{"Quit", "ctrl+q", "quit"},
}

var tools = []struct{ icon, name, key, action string }{
	{"▶", "go", "f5", "continue"}, {"↷", "next", "f6", "next"}, {"↓", "in", "f7", "step"},
	{"↑", "out", "f8", "finish"}, {"⏸", "pause", "^c", "pause"}, {"↻", "build", "^r", "restart"},
	{"☰", "menu", "f1", "menu"},
}

// What turns in the status line while the program is busy.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	frameEvery = 80 * time.Millisecond  // between two steps of what moves on screen
	flashFor   = 900 * time.Millisecond // a changed value takes this long to settle
)

const sourceKeys = "esc: the source, with vim's keys (h j k l w b e 0 $ gg G ^d ^u / ? n N * # % gd gr gt K ^o, counts)   space: breakpoint   R: run to   =: watch   i or : back to the prompt"

// What the prompt says while it is asking for something other than a command.
var asks = map[string]struct{ mark, hint string }{
	"find":  {"/ ", "search this file   (enter keeps it, esc gives it up)"},
	"watch": {"watch ❯ ", "an expression, worked out again at every stop   (enter adds it, esc gives it up)"},
	"cond":  {"stop when ❯ ", "an expression; the breakpoint stops only when it holds   (empty: every time)"},
	"log":   {"print ❯ ", "what to print instead of stopping, with {expressions} in braces   (empty: stop again)"},
}

type (
	changedMsg    struct{}
	frameMsg      struct{}
	candidatesMsg struct {
		text  string
		fill  bool
		found []session.Candidate
	}
	localsMsg struct {
		key  string
		rows []localRow
	}
	itemsMsg struct {
		kind  string
		seq   int
		items []item
	}
	placeMsg struct{ place session.Place }
	// placesMsg answers gd ("definition") or gr ("references").
	placesMsg struct {
		what   string
		places []nav.Place
		err    error
	}
)

// targets is the saved list, shared with the agent socket.
type targets struct {
	mu   sync.Mutex
	list []store.Entry
}

func (t *targets) get() []store.Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]store.Entry(nil), t.list...)
}

type model struct {
	s       *session.Session
	home    string
	targets *targets

	width, height int
	snap          session.Snapshot
	shownKey      string // the frame the panes were last drawn for

	input   textinput.Model
	history []string
	histAt  int
	last    string
	// What the prompt is taking instead of a command: "find" for a search,
	// "watch" for an expression, "cond" or "log" for breakpoint askID. Where
	// the keys were before, to give them back.
	asking  string
	askID   int
	askFrom bool
	// Where the cursor was when a search began, to search from as the
	// needle is typed and to go back to if it is given up.
	origin   [2]int
	colon    bool   // the prompt was opened with ":" from the source
	count    string // digits typed before a motion
	prefix   string // "g" or "z", waiting for the key that completes it
	jumps    []nav.Place
	nav      *nav.Nav
	navFor   string // the target nav was started for
	choices  []session.Candidate
	choiceAt int
	// The text left of the cursor the choices were worked out for; they
	// are open while it is non-empty.
	choicesFor string

	src        viewer
	inSource   bool // the source pane has the keys
	noSource   string
	console    viewport.Model
	program    viewport.Model
	consoleRaw []string
	programRaw []string
	consoleN   int
	programN   int
	locals     []localRow
	vals       map[string]*vals // what the variables were, by function
	stackTop   int              // first stack row shown
	stackAt    int              // the frame the stack was last scrolled to show
	sideTab    string           // which of sideTabs is in front
	sideTop    int              // its first row shown
	tabs       []*tab
	clock      int      // counts the files shown, to tell which tab is oldest
	zones      []string // what could be clicked in the last frame drawn
	hot        string   // the one of those the pointer is over
	tip        *tip
	tipSeq     int
	placeKey   string // the frame the source was last moved to show
	edge       string // the edge between panes the pointer is on
	theme      string // the colours in use, by name
	chosen     string // the same where they were chosen, "" where left to suit the terminal
	frame      int    // counts up while something on screen is moving
	framing    bool   // a frameMsg is on its way
	flashAt    time.Time
	// How far a download of the debugger has got, in percent plus one; 0
	// when none is going on.
	progress    atomic.Int32
	stateKey    string       // the target the screen's state belongs to
	stateFor    *store.Entry // the same, for saving it back
	expanded    map[string]bool
	showSide    bool // asked for where it does not fit beside the source
	pal         *palette
	form        *form
	chooser     *chooser
	srcFor      string // the file on screen when its edits were last counted
	srcEdits    int    // how many edits to it have been taken in
	quitting    bool
	first       *store.Entry
	sideW, logH int
	// What the panes were dragged to, 0 for the usual size: the side
	// column's width, the logs' height, the stack's height, the lldb log's
	// width. And as last drawn, for telling which edge a press is on.
	sideSet, logSet, stackSet, splitSet int
	mainH, stackH, logW                 int
	drag                                string // the edge being dragged
	dragFrom, dragSize                  int    // where it was taken, and the size then
}

// Run shows the screen until it is quit.
func Run(root, adapter string, entries []store.Entry, first *store.Entry) error {
	zone.NewGlobal()
	s := session.New(root, adapter)
	shared := &targets{list: entries}
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = promptHint
	input.PlaceholderStyle = styDim
	input.Focus()
	m := &model{s: s, home: root, targets: shared, input: input, expanded: map[string]bool{}, first: first,
		console: viewport.New(0, 0), program: viewport.New(0, 0), vals: map[string]*vals{}, sideTab: "vars"}
	sizes := store.LoadLayout()
	m.sideSet, m.logSet, m.stackSet, m.splitSet = sizes.Side, sizes.Logs, sizes.Stack, sizes.Split
	// The colours chosen; with none chosen, ones that suit the terminal.
	if sizes.Theme == "" && !lipgloss.HasDarkBackground() {
		m.setTheme("Catppuccin Latte")
	} else {
		m.setTheme(sizes.Theme)
	}
	m.chosen = sizes.Theme

	// All motion, not only drags: what the pointer is over is picked out.
	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	// Many changes in a burst are one redraw.
	wake := make(chan struct{}, 1)
	s.OnChange = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	go func() {
		for range wake {
			program.Send(changedMsg{})
		}
	}()
	server, err := agent.Listen(s, shared.get)
	if err == nil {
		defer server.Close()
		s.Note("note", "agents: echo help | nc -U "+agent.SocketPath())
	}
	_, err = program.Run()
	m.remember()
	store.SaveLayout(store.Layout{Side: m.sideSet, Logs: m.logSet, Stack: m.stackSet, Split: m.splitSet, Theme: m.chosen})
	s.Close()
	return err
}

func (m *model) Init() tea.Cmd {
	if m.first != nil {
		m.s.Start(*m.first, false)
		return textinput.Blink
	}
	m.openTargets()
	return textinput.Blink
}

// ---- update ----

// Update takes a message, and keeps the screen moving while something on it
// is: a spinner, a value settling, a download.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(frameMsg); ok {
		m.framing = false
		m.frame++
	}
	_, cmd := m.update(msg)
	if m.framing || !m.moving() {
		return m, cmd
	}
	m.framing = true
	return m, tea.Batch(cmd, tea.Tick(frameEvery, func(time.Time) tea.Msg { return frameMsg{} }))
}

// moving says whether anything on screen changes with time alone.
func (m *model) moving() bool {
	switch m.snap.State {
	case session.Building, session.Starting, session.Running:
		return true
	}
	return m.progress.Load() > 0 || time.Since(m.flashAt) < flashFor
}

// setTheme draws everything in the colours of a name; the first, for a name
// not known.
func (m *model) setTheme(name string) {
	chosen := themes[0]
	for _, t := range themes {
		if t.name == name {
			chosen = t
		}
	}
	chosen.use()
	m.theme = chosen.name
	m.input.PlaceholderStyle, m.input.TextStyle = styDim, styText
	if m.src.path != "" {
		m.src.read(m.src.path) // Its colours are worked out when it is read.
	}
	// The log is kept coloured: colour it again from the start.
	m.consoleN, m.consoleRaw = 0, nil
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case frameMsg:
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case changedMsg:
		return m, m.refresh()
	case candidatesMsg:
		m.offer(msg)
		return m, nil
	case localsMsg:
		if msg.key == m.shownKey {
			m.locals = msg.rows
			m.took(msg.rows)
		}
		return m, nil
	case tipDueMsg:
		return m, m.tipDue(msg)
	case tipMsg:
		m.tipped(msg)
		return m, nil
	case itemsMsg:
		if m.pal != nil && m.pal.kind == msg.kind && m.pal.seq == msg.seq {
			m.pal.all = msg.items
			m.pal.narrow()
		}
		return m, nil
	case placeMsg:
		m.openFile(msg.place.Path, msg.place.Line, 0)
		m.src.at(m.src.line, m.src.firstText())
		return m, nil
	case placesMsg:
		m.answered(msg)
		return m, nil
	case pickedMsg:
		return m, m.picked(msg)
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// refresh takes in what the session now says.
func (m *model) refresh() tea.Cmd {
	m.snap = m.s.Snapshot()
	// The file on screen was edited on disk: show it as it is now.
	if path := m.src.path; path != m.srcFor {
		m.srcFor, m.srcEdits = path, m.snap.Edits[path]
	} else if edits := m.snap.Edits[path]; edits != m.srcEdits {
		m.srcEdits = edits
		if m.src.reload() == nil {
			m.layout() // Its line numbers may have grown a digit.
		}
	}
	if e := m.snap.Entry; e != nil && e.Name+"\x00"+e.Target+"\x00"+m.snap.Root != m.stateKey {
		// A different target, whoever started it: the screen goes back to
		// how this one was left.
		m.remember()
		m.stateKey = e.Name + "\x00" + e.Target + "\x00" + m.snap.Root
		m.recall(*e)
	}
	if e := m.snap.Entry; e != nil && e.Name+"\x00"+e.Target+"\x00"+m.snap.Root != m.navFor {
		// A different target, whoever started it: the code questions go to
		// a clangd that knows how this one is compiled.
		m.navFor = e.Name + "\x00" + e.Target + "\x00" + m.snap.Root
		if m.nav != nil {
			m.nav.Close()
		}
		label := ""
		if store.IsLabel(e.Target) {
			label = e.Target
		}
		m.nav = nav.Start(m.snap.Root, label)
	}
	if fresh := m.s.Console(m.consoleN); len(fresh) > 0 {
		m.consoleN += len(fresh)
		for _, line := range fresh {
			m.consoleRaw = append(m.consoleRaw, consoleLine(line))
		}
		m.consoleRaw = tail(m.consoleRaw, logKept)
		m.fill(&m.console, m.consoleRaw)
	}
	if fresh := m.s.Program(m.programN); len(fresh) > 0 {
		m.programN += len(fresh)
		m.programRaw = tail(append(m.programRaw, fresh...), logKept)
		m.fill(&m.program, m.programRaw)
	}

	view := m.snap.View
	place, key := "", ""
	if view != nil {
		frame := view.Frames[view.Index]
		place = fmt.Sprintf("%d %d %s:%d %d %d", view.Index, frame.ID, frame.Path, frame.Line, len(view.Frames), view.Run)
		key = fmt.Sprintf("%s %d", place, view.Seq)
	}
	if key == m.shownKey {
		return nil
	}
	m.shownKey = key
	if view == nil {
		m.locals, m.placeKey, m.tip = nil, "", nil
		return nil
	}
	if place != m.placeKey {
		// The program is somewhere new: show it there. Where it has no
		// source, the file that was open stays, for want of anything better.
		m.placeKey, m.tip = place, nil
		frame := view.Frames[view.Index]
		m.noSource = ""
		if frame.Path == "" || m.show(frame.Path, frame.Line, 0) != nil {
			m.noSource = "no source for " + strings.SplitN(frame.Name, "(", 2)[0]
		} else {
			m.src.at(frame.Line, m.src.firstText())
			m.src.centre()
		}
	}
	return m.loadLocals()
}

// consoleLine is a line of the lldb log as it is shown: a command stands out
// at the margin, what it printed is set in under it.
func consoleLine(line session.Line) string {
	switch line.Kind {
	case "lldb":
		return styAccent.Bold(true).Render("❯ ") + styText.Bold(true).Render(line.Text)
	case "agent":
		return styAgent.Bold(true).Render("❯ "+line.Text) + styDim.Render("   agent")
	case "error":
		return styError.Render("▎ " + line.Text)
	case "note":
		return styNote.Render("· " + line.Text)
	}
	return styText.Render("  " + line.Text)
}

func tail(lines []string, keep int) []string {
	if len(lines) > keep {
		return lines[len(lines)-keep:]
	}
	return lines
}

// fill puts lines in a log pane, wrapped, staying at the bottom if it was.
func (m *model) fill(pane *viewport.Model, lines []string) {
	follow := pane.AtBottom()
	pane.SetContent(styText.Width(max(pane.Width, 1)).Render(strings.Join(lines, "\n")))
	if follow {
		pane.GotoBottom()
	}
}

// loadLocals lists the watched expressions and the frame's variables,
// opening again what was left open.
func (m *model) loadLocals() tea.Cmd {
	view, key, open := m.snap.View, m.shownKey, map[string]bool{}
	for path := range m.expanded {
		open[path] = true
	}
	if view == nil {
		return nil
	}
	return func() tea.Msg {
		var rows []localRow
		var walk func(vars []session.Var, depth int, above string)
		walk = func(vars []session.Var, depth int, above string) {
			for i, v := range vars {
				if i == partsShown {
					rows = append(rows, localRow{depth: depth, note: fmt.Sprintf("… %d more; print it to see all", len(vars)-partsShown)})
					break
				}
				row := localRow{v: v, depth: depth, path: above + "/" + v.Name}
				if above == watchPath {
					row.watch = i + 1
				}
				rows = append(rows, row)
				if v.Ref != 0 && open[row.path] {
					walk(m.s.Parts(v.Ref), depth+1, row.path)
				}
			}
		}
		if len(view.Watches) > 0 {
			rows = append(rows, localRow{note: "watched"})
			walk(view.Watches, 0, watchPath)
			rows = append(rows, localRow{note: "in this frame"})
		}
		walk(view.Locals, 0, "")
		return localsMsg{key, rows}
	}
}

// What stands before a watched expression in its path, to keep it apart from
// a variable of the same name.
const watchPath = "\x00watch"

// remember saves how the target's screen is being left.
func (m *model) remember() {
	if m.stateFor == nil {
		return
	}
	m.leave()
	state := store.State{Tab: max(m.tabOf(m.src.path), 0), Watches: m.s.Snapshot().Watches,
		History: tail(m.history, historyKept), Side: m.sideTab}
	for _, t := range m.tabs {
		state.Tabs = append(state.Tabs, store.Tab{Path: t.path, Line: t.line, Col: t.col})
	}
	for path := range m.expanded {
		state.Open = append(state.Open, path)
	}
	sort.Strings(state.Open)
	if err := m.stateFor.SaveState(m.home, state); err != nil {
		m.s.Note("error", "error: "+err.Error())
	}
}

// recall puts the screen back to how a target was left: its files, with the
// one in front shown, its watches, what was opened out, what was typed.
func (m *model) recall(entry store.Entry) {
	state := entry.LoadState(m.home)
	m.stateFor = &entry
	m.locals, m.vals, m.shownKey, m.placeKey, m.tip = nil, map[string]*vals{}, "", "", nil
	m.src, m.noSource, m.jumps, m.tabs = viewer{height: m.src.height, width: m.src.width}, "", nil, nil
	m.expanded = map[string]bool{}
	for _, path := range state.Open {
		m.expanded[path] = true
	}
	m.history, m.histAt, m.last = state.History, len(state.History), ""
	if len(state.History) > 0 {
		m.last = state.History[len(state.History)-1]
	}
	m.sideTab = "vars"
	for _, t := range sideTabs {
		if t.key == state.Side {
			m.sideTab = state.Side
		}
	}
	front := -1
	for i, t := range state.Tabs {
		if _, err := os.Stat(t.Path); err != nil {
			continue
		}
		if i == state.Tab {
			front = len(m.tabs)
		}
		m.tabs = append(m.tabs, &tab{path: t.Path, line: t.Line, col: t.Col, top: t.Line - m.src.height/2})
	}
	if front >= 0 {
		m.front(front)
	} else if len(m.tabs) > 0 {
		m.front(0)
	}
	// In the background: it waits its turn to talk to lldb.
	go m.s.SetWatches(state.Watches)
}

// run sends a command to lldb in the background; the session reports back.
func (m *model) run(command string) tea.Cmd {
	return func() tea.Msg {
		m.s.Execute(command, "lldb")
		return nil
	}
}

func (m *model) needTarget() bool {
	if m.snap.Entry == nil {
		m.s.Note("note", "ctrl+t picks a target to debug.")
		return false
	}
	return true
}

// debugger switches to a version of the debugger, downloading it if it isn't
// here, or again if fresh. The next start of the program uses it.
func (m *model) debugger(version string, fresh bool) tea.Cmd {
	if os.Getenv("TONGS_ADAPTER") != "" {
		m.s.Note("note", "TONGS_ADAPTER names the debugger to use; unset it to let tongs manage one.")
		return nil
	}
	if fresh {
		switch m.snap.State {
		case session.Building, session.Starting, session.Running, session.Stopped:
			m.s.Note("note", "The program is using this debugger. End it first (the lldb command `kill`), then download again.")
			return nil
		}
	}
	return func() tea.Msg {
		defer m.progress.Store(0)
		adapter, err := lldb.Use(version, fresh, func(line string) {
			// "  40%" moves the bar in the status line; the rest is said.
			if percent, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(line), "%")); err == nil {
				m.progress.Store(int32(percent) + 1)
				if m.s.OnChange != nil {
					m.s.OnChange() // The bar is drawn from here on.
				}
				return
			}
			if strings.HasPrefix(line, "downloading") {
				m.progress.Store(1)
			}
			m.s.Note("note", line)
		})
		if err != nil {
			m.s.Note("error", err.Error())
			return nil
		}
		m.s.SetAdapter(adapter)
		m.s.Note("note", "Using CodeLLDB "+version+". ctrl+r starts the program with it.")
		return nil
	}
}

// do carries out an action from a key, the toolbar or the menu.
func (m *model) do(action string) tea.Cmd {
	switch action {
	case "continue", "next", "step", "finish":
		if m.needTarget() {
			return m.run(action)
		}
	case "pause":
		go m.s.Pause()
	case "restart":
		if m.needTarget() {
			m.s.Restart()
		}
	case "menu":
		var items []item
		for _, a := range actions {
			items = append(items, item{label: a.name, detail: a.key, value: a.action})
		}
		m.pal = newPalette("menu", "Menu", sourceKeys, items)
	case "targets":
		m.openTargets()
	case "files":
		m.pal = newPalette("files", "Open a file of the program", "enter opens   esc closes", nil)
		return func() tea.Msg {
			var items []item
			for _, path := range m.s.Files() {
				items = append(items, item{label: path, value: path})
			}
			return itemsMsg{"files", 0, items}
		}
	case "functions":
		if m.needTarget() {
			m.pal = newPalette("functions", "Find a function", "type part of its name   enter sets a breakpoint there   esc closes", nil)
			m.pal.asks = true
		}
	case "breakpoints":
		var items []item
		for _, id := range m.breakIDs() {
			point := m.snap.Breaks[id]
			detail := fmt.Sprintf("#%d", id)
			if !point.Enabled {
				detail += "  off"
			}
			if point.Condition != "" {
				detail += "  if " + point.Condition
			}
			if point.Log != "" {
				detail += "  prints " + point.Log
			}
			items = append(items, item{label: fmt.Sprintf("%s:%d", agent.Rel(m.snap.Root, point.Path), point.Line), detail: detail, value: id})
		}
		m.pal = newPalette("breakpoints", "Breakpoints",
			"enter goes there   ctrl+e turns it on or off   ctrl+o condition   ctrl+l print and run on   ctrl+d deletes   esc closes", items)
	case "find", "find_back":
		if m.src.path != "" {
			m.src.back = action == "find_back"
			m.origin = [2]int{m.src.line, m.src.col}
			m.ask("find", 0, "")
		}
	case "watch":
		m.ask("watch", 0, "")
	case "watch_word":
		if expr := m.src.chain(m.src.line, m.src.col); m.src.path != "" && expr != "" {
			m.sideTab = "vars"
			return func() tea.Msg { m.s.AddWatch(expr); return nil }
		}
	case "watch_clear":
		return func() tea.Msg { m.s.SetWatches(nil); return nil }
	case "condition", "log":
		if m.src.path == "" {
			return nil
		}
		id, ok := m.s.BreakOn(m.src.path, m.src.line)
		if !ok {
			m.s.Note("note", "There is no breakpoint on this line. space sets one.")
			return nil
		}
		m.askBreak(map[string]string{"condition": "cond", "log": "log"}[action], id)
	case "tab_next":
		m.turn(1)
	case "tab_prev":
		m.turn(-1)
	case "tab_close":
		m.shut(m.tabOf(m.src.path))
	case "side_vars", "side_breaks", "side_threads":
		m.sideTab, m.sideTop = strings.TrimPrefix(action, "side_"), 0
		if m.sideW == 0 {
			m.showSide = true
		}
	case "source":
		m.inSource = m.src.path != ""
	case "prompt":
		m.inSource, m.count, m.prefix = false, "", ""
	case "break_here":
		if m.src.path != "" && m.needTarget() {
			path, line := m.src.path, m.src.line
			return func() tea.Msg { m.s.ToggleBreak(path, line); return nil }
		}
	case "run_to_here":
		if m.src.path != "" && m.needTarget() {
			path, line := m.src.path, m.src.line
			return func() tea.Msg { m.s.RunTo(path, line); return nil }
		}
	case "definition", "references", "hover":
		if m.src.path == "" || m.nav == nil {
			return nil
		}
		at, server := nav.Place{Path: m.src.path, Line: m.src.line, Col: m.src.col + 1}, m.nav
		return func() tea.Msg {
			switch action {
			case "definition":
				places, err := server.Definition(at)
				return placesMsg{action, places, err}
			case "references":
				places, err := server.References(at)
				return placesMsg{action, places, err}
			}
			text, err := server.Hover(at)
			if err == nil && text == "" {
				text = "nothing is known about what is under the cursor"
			}
			if err != nil {
				text = err.Error()
			}
			m.s.Note("out", text)
			return nil
		}
	case "side":
		m.showSide = !m.showSide
		m.layout()
	case "sizes":
		m.sideSet, m.logSet, m.stackSet, m.splitSet = 0, 0, 0, 0
		m.layout()
	case "themes":
		var items []item
		for _, t := range themes {
			detail := ""
			if t.name == m.theme {
				detail = "in use"
			}
			items = append(items, item{label: t.name, detail: detail, value: t.name})
		}
		m.pal = newPalette("themes", "Colours", "enter uses it   esc closes", items)
	case "lldb_fresh":
		return m.debugger(lldb.Version(), true)
	case "lldb_versions":
		m.pal = newPalette("versions", "Debugger version (CodeLLDB)", "enter uses it, downloading it if need be   esc closes", nil)
		return func() tea.Msg {
			versions, err := lldb.Versions()
			if err != nil {
				m.s.Note("error", "listing the debugger's versions: "+err.Error())
			}
			var items []item
			for _, version := range versions {
				detail := ""
				switch {
				case version == lldb.Version():
					detail = "in use"
				case version == lldb.Pinned:
					detail = "the one tongs is tested with"
				}
				items = append(items, item{label: version, detail: detail, value: version})
			}
			return itemsMsg{"versions", 0, items}
		}
	case "quit":
		m.quitting = true
		return tea.Quit
	}
	return nil
}

// ask has the prompt take something other than a command: see asks.
func (m *model) ask(kind string, id int, value string) {
	m.closeChoices()
	m.asking, m.askID, m.askFrom = kind, id, m.inSource
	m.inSource = false
	m.input.Placeholder = asks[kind].hint
	m.input.SetValue(value)
	m.input.CursorEnd()
}

// askBreak asks for a breakpoint's condition ("cond") or what it prints
// ("log"), starting from the one it has.
func (m *model) askBreak(kind string, id int) {
	point := m.snap.Breaks[id]
	m.ask(kind, id, map[string]string{"cond": point.Condition, "log": point.Log}[kind])
}

func (m *model) openTargets() {
	m.pal, m.chooser = nil, newChooser(m.home, m.targets.get())
}

// knownFolders are the projects the saved targets are in, this one first.
func (m *model) knownFolders() []string {
	folders, seen := []string{m.home}, map[string]bool{m.home: true}
	for _, e := range m.targets.get() {
		if dir := e.Project(m.home); !seen[dir] {
			seen[dir] = true
			folders = append(folders, dir)
		}
	}
	sort.Strings(folders[1:])
	return folders
}

// chooserDo carries out one of the targets dialog's actions.
func (m *model) chooserDo(action string) tea.Cmd {
	c := m.chooser
	row, ok := c.current()
	switch action {
	case "start":
		if ok {
			m.chooser = nil
			return m.take("targets", row.index)
		}
	case "new":
		m.chooser, m.form = nil, newForm(m.home, nil, -1, c.folder(), m.knownFolders())
	case "edit":
		if ok {
			m.chooser, m.form = nil, newForm(m.home, &row.entry, row.index, "", m.knownFolders())
		}
	case "delete":
		if ok {
			m.targets.mu.Lock()
			m.targets.list = append(m.targets.list[:row.index:row.index], m.targets.list[row.index+1:]...)
			err := store.Save(m.targets.list)
			m.targets.mu.Unlock()
			if err != nil {
				m.s.Note("error", "error: "+err.Error())
			}
			c.list(m.targets.get())
		}
	}
	return nil
}

func (m *model) chooserKey(msg tea.KeyMsg) tea.Cmd {
	c := m.chooser
	switch msg.String() {
	case "esc":
		m.chooser = nil
	case "up", "ctrl+k":
		c.move(-1)
	case "down", "ctrl+j":
		c.move(1)
	case "enter":
		return m.chooserDo("start")
	case "ctrl+n":
		return m.chooserDo("new")
	case "ctrl+e":
		return m.chooserDo("edit")
	case "ctrl+d":
		return m.chooserDo("delete")
	default:
		before := c.input.Value()
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(msg)
		if c.input.Value() != before {
			c.list(m.targets.get())
		}
		return cmd
	}
	return nil
}

// openFile shows a place in the source and gives the source the keys. Where
// the cursor was is remembered, for ctrl+o to go back to.
func (m *model) openFile(path string, line, col int) {
	from := nav.Place{Path: m.src.path, Line: m.src.line, Col: m.src.col}
	if m.show(path, line, col) != nil {
		return
	}
	if from.Path != "" && (from.Path != path || from.Line != line || from.Col != col) {
		m.jumps = append(m.jumps, from)
	}
	m.src.at(line, col)
	m.noSource, m.inSource = "", true
}

// jumpBack returns to where the cursor was before the last jump.
func (m *model) jumpBack() {
	if len(m.jumps) == 0 {
		return
	}
	last := m.jumps[len(m.jumps)-1]
	m.jumps = m.jumps[:len(m.jumps)-1]
	if m.show(last.Path, last.Line, last.Col) == nil {
		m.src.at(last.Line, last.Col)
	}
}

// mark remembers the cursor before a jump within the file.
func (m *model) mark() {
	m.jumps = append(m.jumps, nav.Place{Path: m.src.path, Line: m.src.line, Col: m.src.col})
}

func (m *model) answered(msg placesMsg) {
	switch {
	case msg.err != nil:
		m.s.Note("error", "error: "+msg.err.Error())
	case len(msg.places) == 0:
		m.s.Note("note", "clangd knows no "+msg.what+" for what is under the cursor")
	case len(msg.places) == 1 && msg.what == "definition":
		m.openFile(msg.places[0].Path, msg.places[0].Line, msg.places[0].Col-1)
		m.src.centre()
	default:
		var items []item
		for _, place := range msg.places {
			items = append(items, item{label: fmt.Sprintf("%s:%d", agent.Rel(m.snap.Root, place.Path), place.Line), value: place})
		}
		m.pal = newPalette("places", strings.ToUpper(msg.what[:1])+msg.what[1:], "enter goes there   esc closes", items)
	}
}

// ---- keys ----

func (m *model) key(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	m.tip = nil // What it pointed at may be about to move.
	if key == "ctrl+q" {
		return m.do("quit")
	}
	if m.form != nil {
		return m.formKey(msg)
	}
	if m.chooser != nil {
		return m.chooserKey(msg)
	}
	if m.pal != nil {
		return m.paletteKey(msg)
	}
	switch key {
	case "ctrl+c":
		return m.do("pause")
	case "f5":
		return m.do("continue")
	case "f6":
		return m.do("next")
	case "f7":
		return m.do("step")
	case "f8":
		return m.do("finish")
	case "ctrl+r":
		return m.do("restart")
	case "f1":
		return m.do("menu")
	case "ctrl+t":
		return m.do("targets")
	case "ctrl+p":
		return m.do("files")
	case "ctrl+n":
		return m.do("functions")
	case "ctrl+b":
		if !m.inSource { // In the source it is vim's page up.
			return m.do("breakpoints")
		}
	}
	if m.inSource {
		return m.sourceKey(key)
	}
	return m.promptKey(msg)
}

// sourceKey is vim's normal mode, for a file that can't be changed: motions,
// counts, searches and jumps. i, a or : go back to the prompt.
func (m *model) sourceKey(key string) tea.Cmd {
	v := &m.src
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && (key != "0" || m.count != "") {
		m.count += key
		return nil
	}
	n, counted := 1, m.count != ""
	if counted {
		n, _ = strconv.Atoi(m.count)
		n = max(n, 1)
	}
	prefix := m.prefix
	m.count, m.prefix = "", ""
	half, page := max(v.height/2, 1), max(v.height-2, 1)

	if prefix == "g" {
		switch key {
		case "g":
			m.mark()
			line := 1
			if counted {
				line = n
			}
			v.at(line, 0)
			v.at(v.line, v.firstText())
		case "d":
			return m.do("definition")
		case "r":
			return m.do("references")
		case "t":
			m.turn(1)
		case "T":
			m.turn(-1)
		}
		return nil
	}
	if prefix == "z" {
		switch key {
		case "z":
			v.centre()
		case "t":
			v.top = v.line
			v.clamp()
		case "b":
			v.top = v.line - v.height + 1
			v.clamp()
		}
		return nil
	}

	switch key {
	case "g", "z":
		m.prefix = key
		if counted {
			m.count = strconv.Itoa(n)
		}
	case "h", "left":
		v.at(v.line, v.col-n)
	case "l", "right":
		v.at(v.line, v.col+n)
	case "j", "down":
		v.to(v.line + n)
	case "k", "up":
		v.to(v.line - n)
	case "w", "b", "e":
		for ; n > 0; n-- {
			v.word(key)
		}
	case "0", "home":
		v.at(v.line, 0)
	case "^":
		v.at(v.line, v.firstText())
	case "$", "end":
		v.at(v.line, len(v.text()))
	case "G":
		m.mark()
		line := len(v.plain)
		if counted {
			line = n
		}
		v.at(line, 0)
		v.at(v.line, v.firstText())
	case "H":
		v.to(v.top)
	case "M":
		v.to(v.top + v.height/2)
	case "L":
		v.to(v.top + v.height - 1)
	case "ctrl+d":
		v.scroll(half)
		v.to(v.line + half)
	case "ctrl+u":
		v.scroll(-half)
		v.to(v.line - half)
	case "ctrl+f", "pgdown":
		v.scroll(page)
		v.to(v.line + page)
	case "ctrl+b", "pgup":
		v.scroll(-page)
		v.to(v.line - page)
	case "%":
		v.pair()
	case "/":
		return m.do("find")
	case "?":
		return m.do("find_back")
	case "n", "N":
		m.mark()
		for ; n > 0; n-- {
			m.next(key == "N")
		}
	case "*", "#":
		if word, start := v.under(); word != "" {
			m.mark()
			v.needle, v.back = word, key == "#"
			v.at(v.line, start)
			m.next(false)
		}
	case "K":
		return m.do("hover")
	case "ctrl+o":
		m.jumpBack()
	case " ", "B":
		return m.do("break_here")
	case "R":
		return m.do("run_to_here")
	case "=":
		return m.do("watch_word")
	case "C":
		return m.do("condition")
	case "P":
		return m.do("log")
	case ":":
		m.colon = true
		return m.do("prompt")
	case "i", "a", "tab":
		return m.do("prompt")
	case "esc":
		v.needle = "" // As :noh does.
	}
	return nil
}

// next goes to the next match of the last search, in its direction or against it.
func (m *model) next(against bool) {
	v := &m.src
	found, wrapped := v.find(v.line, v.col, v.back != against, false)
	switch {
	case !found:
		m.s.Note("note", "not found: "+v.needle)
	case wrapped:
		m.s.Note("note", "search wrapped round the file")
	}
}

func (m *model) left() string {
	runes := []rune(m.input.Value())
	return string(runes[:min(m.input.Position(), len(runes))])
}

func (m *model) promptKey(msg tea.KeyMsg) tea.Cmd {
	open := m.choicesFor != ""
	switch msg.String() {
	case "enter":
		if open {
			m.accept()
			return nil
		}
		return m.submit()
	case "tab":
		if open {
			m.accept()
			return nil
		}
		if m.asking == "" {
			return m.complete(true)
		}
		return nil
	case "up", "down":
		step := map[string]int{"up": -1, "down": 1}[msg.String()]
		if open {
			m.choiceAt = ((m.choiceAt+step)%len(m.choices) + len(m.choices)) % len(m.choices)
			return nil
		}
		if m.asking != "" {
			return nil // The history is of commands.
		}
		at := m.histAt + step
		if at < 0 || at > len(m.history) {
			return nil
		}
		m.histAt = at
		m.input.SetValue("")
		if at < len(m.history) {
			m.input.SetValue(m.history[at])
		}
		m.input.CursorEnd()
		return nil
	case "esc":
		// As in vim: esc leaves the line for the text. What was typed stays.
		switch {
		case open:
			m.closeChoices()
		case m.asking == "find":
			m.src.needle = ""
			m.src.at(m.origin[0], m.origin[1])
			m.endAsk()
			m.inSource = true
		case m.asking != "":
			m.endAsk()
			m.inSource = m.askFrom && m.src.path != ""
		case m.src.path != "":
			m.inSource, m.colon = true, false
		default:
			m.input.SetValue("")
		}
		return nil
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() == before {
		return cmd
	}
	if m.asking == "find" {
		// Search as it is typed, each time from where the cursor began.
		m.src.needle = m.input.Value()
		m.src.at(m.origin[0], m.origin[1])
		m.src.find(m.origin[0], m.origin[1], m.src.back, false)
		return cmd
	}
	if m.asking != "" {
		return cmd
	}
	if open {
		// Typing with the list open narrows it.
		return tea.Batch(cmd, m.complete(false))
	}
	return cmd
}

func (m *model) endAsk() {
	m.asking = ""
	m.input.Placeholder = promptHint
	m.input.SetValue("")
}

func (m *model) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if kind, id := m.asking, m.askID; kind != "" && kind != "find" {
		m.endAsk()
		m.inSource = m.askFrom && m.src.path != ""
		return func() tea.Msg {
			switch kind {
			case "watch":
				m.s.AddWatch(text)
			case "cond":
				m.s.SetCondition(id, text)
			case "log":
				m.s.SetLog(id, text)
			}
			return nil
		}
	}
	if m.asking != "" {
		// The cursor is already on the match: the search ran as it was typed.
		found := text == "" || m.src.has(m.src.line)
		m.endAsk()
		m.inSource = true
		if !found {
			m.s.Note("note", "not found: "+text)
			m.src.at(m.origin[0], m.origin[1])
		} else if text != "" {
			m.jumps = append(m.jumps, nav.Place{Path: m.src.path, Line: m.origin[0], Col: m.origin[1]})
		}
		return nil
	}
	if line, err := strconv.Atoi(text); err == nil && m.colon {
		// :123, as in vim.
		m.input.SetValue("")
		m.colon, m.inSource = false, true
		m.mark()
		m.src.at(line, 0)
		m.src.at(m.src.line, m.src.firstText())
		m.src.centre()
		return nil
	}
	back := m.colon
	m.colon = false
	if back && text != "" {
		m.inSource = true // A command given with : goes back to the source, as in vim.
	}
	m.input.SetValue("")
	if text == "" {
		text = m.last
	}
	if text == "" {
		return nil
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != text {
		m.history = append(m.history, text)
	}
	m.histAt, m.last = len(m.history), text
	switch text {
	// Not sent to lldb: quitting ends the tool, and a second process started
	// from the prompt would run outside the session this screen follows.
	case "q", "quit", "exit":
		return m.do("quit")
	case "r", "run", "process launch":
		return m.do("restart")
	}
	if !m.needTarget() {
		return nil
	}
	return m.run(text)
}

// ---- completion ----

func (m *model) complete(fill bool) tea.Cmd {
	text := m.left()
	if text == "" {
		m.closeChoices()
		return nil
	}
	return func() tea.Msg { return candidatesMsg{text, fill, m.s.Complete(text)} }
}

func (m *model) closeChoices() {
	m.choices, m.choicesFor, m.choiceAt = nil, "", 0
	m.layout()
}

// put replaces the line from start to the cursor.
func (m *model) put(start int, text string) {
	runes := []rune(m.input.Value())
	at := min(m.input.Position(), len(runes))
	start = min(start, at)
	m.input.SetValue(string(runes[:start]) + text + string(runes[at:]))
	m.input.SetCursor(start + len([]rune(text)))
}

func (m *model) accept() {
	choice := m.choices[m.choiceAt]
	m.closeChoices()
	m.put(choice.Start, choice.Put)
}

// offer takes the answer to a completion: one match is typed in; several are
// typed as far as they agree and listed.
func (m *model) offer(msg candidatesMsg) {
	if m.left() != msg.text || m.pal != nil || m.form != nil || m.chooser != nil {
		return // The line moved on while lldb thought.
	}
	found := msg.found
	if len(found) == 0 {
		m.closeChoices()
		return
	}
	text := msg.text
	if msg.fill {
		agreed, sameStart := found[0].Put, true
		for _, c := range found[1:] {
			sameStart = sameStart && c.Start == found[0].Start
			for !strings.HasPrefix(c.Put, agreed) {
				agreed = agreed[:len(agreed)-1]
			}
		}
		if sameStart && len(agreed) > len(text)-found[0].Start {
			m.put(found[0].Start, agreed)
			text = m.left()
		}
		if len(found) == 1 {
			m.closeChoices()
			return
		}
	}
	m.choices, m.choicesFor, m.choiceAt = found, text, 0
	m.layout()
}

// ---- palette and form ----

func (m *model) paletteKey(msg tea.KeyMsg) tea.Cmd {
	p := m.pal
	current, ok := p.current()
	switch msg.String() {
	case "esc":
		m.pal = nil
		return nil
	case "up", "ctrl+k":
		p.move(-1)
		return nil
	case "down", "ctrl+j":
		p.move(1)
		return nil
	case "enter":
		if ok {
			m.pal = nil
			return m.take(p.kind, current.value)
		}
		return nil
	case "ctrl+d":
		if ok && p.kind == "breakpoints" {
			m.pal = nil
			return m.run(fmt.Sprintf("breakpoint delete %d", current.value.(int)))
		}
		return nil
	case "ctrl+e", "ctrl+o", "ctrl+l":
		if !ok || p.kind != "breakpoints" {
			return nil
		}
		id := current.value.(int)
		m.pal = nil
		switch msg.String() {
		case "ctrl+o":
			m.askBreak("cond", id)
		case "ctrl+l":
			m.askBreak("log", id)
		default:
			on := !m.snap.Breaks[id].Enabled
			return func() tea.Msg { m.s.SetEnabled(id, on); return nil }
		}
		return nil
	}
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if query := strings.TrimSpace(p.input.Value()); p.input.Value() != before {
		if p.asks {
			p.seq++
			seq := p.seq
			return tea.Batch(cmd, func() tea.Msg {
				var items []item
				for _, f := range m.s.Functions(query) {
					detail := ""
					if f.File != "" {
						detail = fmt.Sprintf("%s:%d", f.File, f.Line)
					}
					items = append(items, item{label: f.Name, detail: detail, value: f.Name})
				}
				return itemsMsg{"functions", seq, items}
			})
		}
		p.narrow()
	}
	return cmd
}

// take acts on what was picked from a palette.
func (m *model) take(kind string, value any) tea.Cmd {
	switch kind {
	case "menu":
		return m.do(value.(string))
	case "files":
		m.openFile(filepath.Join(m.snap.Root, value.(string)), 1, 0)
	case "functions":
		name := value.(string)
		return func() tea.Msg {
			if place, ok := m.s.BreakAt(name); ok {
				return placeMsg{place}
			}
			return nil
		}
	case "breakpoints":
		if place, ok := m.snap.Breaks[value.(int)]; ok {
			m.openFile(place.Path, place.Line, 0)
		}
	case "versions":
		return m.debugger(value.(string), false)
	case "themes":
		m.setTheme(value.(string))
		m.chosen = m.theme
		m.layout()
		return m.refresh()
	case "places":
		place := value.(nav.Place)
		m.openFile(place.Path, place.Line, place.Col-1)
		m.src.centre()
	case "targets":
		// The screen is put back to how this target was left when the
		// session says it is the one loaded (see refresh).
		m.remember()
		m.stateFor, m.stateKey = nil, ""
		m.s.Start(m.targets.get()[value.(int)], false)
	}
	return nil
}

func (m *model) formKey(msg tea.KeyMsg) tea.Cmd {
	f := m.form
	switch msg.String() {
	case "esc":
		m.form = nil
		m.openTargets()
		return nil
	case "tab", "down":
		f.focus(f.at + 1)
		return nil
	case "shift+tab", "up":
		f.focus(f.at - 1)
		return nil
	case "enter":
		if f.at < len(fields)-1 {
			f.focus(f.at + 1)
			return nil
		}
		return m.saveForm()
	case "ctrl+s":
		return m.saveForm()
	case "f2":
		switch fields[f.at].key {
		case "dir":
			return pick("dir", f.folder(), false)
		case "target":
			return pick("target", f.folder(), true)
		}
		return nil
	case "f3":
		f.nextKnown()
		return nil
	}
	var cmd tea.Cmd
	f.inputs[f.at], cmd = f.inputs[f.at].Update(msg)
	return cmd
}

func (m *model) saveForm() tea.Cmd {
	f := m.form
	m.targets.mu.Lock()
	// A name is one target's within its folder; another folder may reuse it.
	taken := map[string]bool{}
	for i, e := range m.targets.list {
		if i != f.editing && e.Project(m.home) == f.folder() {
			taken[e.Name] = true
		}
	}
	entry, err := f.entry(taken)
	if err == nil {
		if f.editing >= 0 {
			m.targets.list[f.editing] = entry
		} else {
			m.targets.list = append(m.targets.list, entry)
		}
		err = store.Save(m.targets.list)
	}
	m.targets.mu.Unlock()
	if err != nil {
		f.message, f.bad = err.Error(), true
		return nil
	}
	m.form = nil
	m.openTargets()
	return nil
}

func (m *model) picked(msg pickedMsg) tea.Cmd {
	f := m.form
	if f == nil {
		return nil
	}
	if msg.err != nil {
		f.message, f.bad = msg.err.Error(), true
		return nil
	}
	if msg.path == "" {
		return nil
	}
	if msg.field == "dir" {
		f.set("dir", store.Tilde(msg.path))
		f.folderChanged()
	} else {
		f.set("target", shortPath(msg.path, f.folder()))
	}
	return nil
}

// shortPath is path as written in a field: relative to base when inside it.
func shortPath(path, base string) string {
	if path == base {
		return ""
	}
	return agent.Rel(base, path)
}

// ---- mouse ----

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	wheel := map[tea.MouseButton]int{tea.MouseButtonWheelUp: -3, tea.MouseButtonWheelDown: 3}[msg.Button]
	press := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft
	if m.form != nil {
		for i, field := range fields {
			if press && zone.Get("browse:"+field.key).InBounds(msg) {
				m.form.focus(i)
				return pick(field.key, m.form.folder(), field.key == "target")
			}
			if press && zone.Get(fmt.Sprintf("field:%d", i)).InBounds(msg) {
				m.form.focus(i)
			}
		}
		for at, dir := range m.form.known {
			if press && zone.Get(fmt.Sprintf("known:%d", at)).InBounds(msg) {
				m.form.set("dir", store.Tilde(dir))
				m.form.folderChanged()
			}
		}
		return nil
	}
	if c := m.chooser; c != nil {
		if wheel != 0 {
			c.move(wheel / 3)
			return nil
		}
		for _, button := range chooserButtons {
			if press && zone.Get("target:"+button.action).InBounds(msg) {
				return m.chooserDo(button.action)
			}
		}
		for i, row := range c.rows {
			if press && row.index >= 0 && zone.Get(fmt.Sprintf("target:%d", i)).InBounds(msg) {
				c.at = i
				return m.chooserDo("start")
			}
		}
		return nil
	}
	if m.pal == nil && m.drag == "" && press {
		// Before the edges: the tabs written over them are for clicking.
		if cmd, did := m.clicked(msg); did {
			return cmd
		}
	}
	if m.pal == nil && m.dragged(msg) {
		return nil
	}
	if p := m.pal; p != nil {
		if wheel != 0 {
			p.move(wheel / 3)
			return nil
		}
		for i := range p.shown {
			if press && zone.Get(fmt.Sprintf("pal:%d", i)).InBounds(msg) {
				m.pal = nil
				return m.take(p.kind, p.shown[i].value)
			}
		}
		return nil
	}
	if msg.Action == tea.MouseActionMotion {
		return m.pointed(msg)
	}
	if wheel != 0 {
		m.tip = nil
		switch {
		case zone.Get("source").InBounds(msg):
			m.src.scroll(wheel)
		case zone.Get("console").InBounds(msg):
			m.console.SetYOffset(m.console.YOffset + wheel)
		case zone.Get("program").InBounds(msg):
			m.program.SetYOffset(m.program.YOffset + wheel)
		case zone.Get("stack").InBounds(msg):
			m.stackTop += wheel // sidePanes keeps both within their lists.
		case zone.Get("locals").InBounds(msg):
			m.sideTop += wheel
		}
		return nil
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight {
		// On a line with a breakpoint: when it is to stop.
		if z := zone.Get("source"); z.InBounds(msg) && m.src.path != "" {
			_, y := z.Pos(msg)
			if id, ok := m.s.BreakOn(m.src.path, m.src.top+y); ok {
				m.askBreak("cond", id)
			}
		}
		return nil
	}
	if !press {
		return nil
	}
	for i := range m.choices {
		if zone.Get(fmt.Sprintf("choice:%d", i)).InBounds(msg) {
			m.choiceAt = i
			m.accept()
			return nil
		}
	}
	if z := zone.Get("source"); z.InBounds(msg) && m.src.path != "" {
		x, y := z.Pos(msg)
		if line := m.src.top + y; line <= len(m.src.plain) {
			m.inSource = true
			m.src.at(line, max(x-m.src.gutter(), 0)+m.src.left)
			if x < m.src.gutter()-1 {
				return m.do("break_here") // In the margin.
			}
		}
		return nil
	}
	if zone.Get("prompt").InBounds(msg) || zone.Get("console").InBounds(msg) || zone.Get("program").InBounds(msg) {
		m.inSource = false
	}
	return nil
}

// clicked carries out a press on one of the places marked for it, and says
// whether it was on one.
func (m *model) clicked(msg tea.MouseMsg) (tea.Cmd, bool) {
	hit := ""
	for _, id := range m.zones {
		if zone.Get(id).InBounds(msg) {
			hit = id
			break
		}
	}
	kind, rest, _ := strings.Cut(hit, ":")
	n, _ := strconv.Atoi(rest)
	switch kind {
	case "tool":
		return m.do(rest), true
	case "tab":
		m.front(n)
	case "tabx":
		m.shut(n)
	case "side":
		m.sideTab, m.sideTop = rest, 0
	case "frame":
		return m.run(fmt.Sprintf("frame select %d", n)), true
	case "thread":
		return m.run(fmt.Sprintf("thread select %d", n)), true
	case "local":
		if n < len(m.locals) {
			if path := m.locals[n].path; m.expanded[path] {
				delete(m.expanded, path) // Gone, not false: the set is of what is open.
			} else {
				m.expanded[path] = true
			}
			return m.loadLocals(), true
		}
	case "watch":
		m.ask("watch", 0, "")
	case "unwatch":
		return func() tea.Msg { m.s.RemoveWatch(n); return nil }, true
	case "bon":
		on := !m.snap.Breaks[n].Enabled
		return func() tea.Msg { m.s.SetEnabled(n, on); return nil }, true
	case "bgo":
		if point, ok := m.snap.Breaks[n]; ok {
			m.openFile(point.Path, point.Line, 0)
			m.src.centre()
		}
	case "bif":
		m.askBreak("cond", n)
	case "blog":
		m.askBreak("log", n)
	case "bdel":
		return m.run(fmt.Sprintf("breakpoint delete %d", n)), true
	default:
		return nil, false
	}
	return nil, true
}

// edgeAt is the edge between panes that a cell is on: "side" between the
// source and the side column, "logs" between those and the logs, "stack"
// between the stack and the locals, "split" between the two logs.
func (m *model) edgeAt(x, y int) string {
	const top = 1         // under the status line
	logs := top + m.mainH // the row the logs' strips are on
	sourceW := m.width
	if m.sideW > 0 {
		sourceW -= m.sideW + 1
	}
	inMain := y >= top && y < logs
	switch {
	case y == logs:
		return "logs"
	case m.sideW > 0 && inMain && (x == sourceW-1 || x == sourceW):
		return "side"
	case inMain && m.stackH > 0 && y == top+m.stackH && (m.sideW > 0 && x > sourceW || m.sideW == 0 && m.showSide):
		return "stack"
	case m.width >= splitLogsAt && y > logs && y < logs+m.logH && (x == m.logW-1 || x == m.logW):
		return "split"
	}
	return ""
}

// dragged resizes the panes: it takes a press on an edge between two, the
// movement that follows and the release, and says whether the event was one
// of those.
func (m *model) dragged(msg tea.MouseMsg) bool {
	if m.drag == "" {
		if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
			return false
		}
		switch m.drag = m.edgeAt(msg.X, msg.Y); m.drag {
		case "":
			return false
		case "side":
			m.dragFrom, m.dragSize = msg.X, m.sideW
		case "logs":
			m.dragFrom, m.dragSize = msg.Y, m.logH
		case "stack":
			m.dragFrom, m.dragSize = msg.Y, m.stackH
		case "split":
			m.dragFrom, m.dragSize = msg.X, m.logW
		}
		return true
	}
	if msg.Action == tea.MouseActionRelease {
		m.drag = ""
		return true
	}
	// The side column and the logs grow toward the top left; the others away.
	switch m.drag {
	case "side":
		m.sideSet = max(m.dragSize-(msg.X-m.dragFrom), 1)
	case "logs":
		m.logSet = max(m.dragSize-(msg.Y-m.dragFrom), 1)
	case "stack":
		m.stackSet = max(m.dragSize+(msg.Y-m.dragFrom), 1)
	case "split":
		m.splitSet = max(m.dragSize+(msg.X-m.dragFrom), 1)
	}
	m.layout()
	return true
}

// ---- view ----

// layout sizes the panes for the window.
func (m *model) layout() {
	if m.width == 0 {
		return
	}
	m.logH = min(max(m.height/4, 5), 12)
	if m.width < splitLogsAt {
		m.logH = min(max(m.height/3, 6), 14) // Two logs, one over the other.
	}
	if m.logSet > 0 {
		// A dragged size holds as far as the window leaves room for it.
		m.logH = max(min(m.logSet, m.height-2-4), 3)
	}
	m.sideW = 0
	if m.width >= sideAt {
		m.sideW = min(max(m.width/3, 32), 56)
		if m.sideSet > 0 {
			m.sideW = max(min(m.sideSet, m.width-30), 20)
		}
	}
	// A row each for the status line and the prompt; a pane's first row is
	// its strip, and its last column its scroll bar.
	mainH := max(m.height-2-m.logH-min(len(m.choices), choicesMax), 3)
	sourceW := m.width
	if m.sideW > 0 {
		sourceW -= m.sideW + 1 // The line between them.
	}
	m.src.resize(sourceW-m.src.gutter()-1, mainH-1)

	logW := m.width
	if m.width >= splitLogsAt {
		logW = m.width * 3 / 5
		if m.splitSet > 0 {
			logW = max(min(m.splitSet, m.width-20), 20)
		}
		m.console.Height, m.program.Height = m.logH-1, m.logH-1
		m.program.Width = m.width - logW - 2
	} else {
		m.console.Height = m.logH - m.logH/2 - 1
		m.program.Height = m.logH/2 - 1
		m.program.Width = m.width - 1
	}
	m.console.Width = logW - 1
	m.logW = logW
	m.fill(&m.console, m.consoleRaw)
	m.fill(&m.program, m.programRaw)
	m.input.Width = max(m.width-14, 10)
}

func (m *model) View() string {
	if m.quitting || m.width == 0 {
		return ""
	}
	// The palette, the form and the targets are dialogs over the screen.
	switch {
	case m.pal != nil:
		return zone.Scan(over(m.screen(), m.pal.view(m.width, m.height), m.width, m.height))
	case m.form != nil:
		return zone.Scan(over(m.screen(), m.form.view(min(m.width, max(m.width-8, 40), 100), m.height-2), m.width, m.height))
	case m.chooser != nil:
		return zone.Scan(over(m.screen(), m.chooser.view(m.width, m.height), m.width, m.height))
	}
	return zone.Scan(m.withTip(m.screen()))
}

// screen is the debugger itself: the status line, the panes, the prompt.
func (m *model) screen() string {
	m.zones = m.zones[:0]
	choices := m.choicesView()
	mainH := max(m.height-2-m.logH-len(choices), 3)
	m.mainH = mainH
	lit := func(edge string) bool { return m.drag == edge || m.drag == "" && m.edge == edge }

	rows := []string{m.statusBar()}
	switch {
	case m.sideW > 0:
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top,
			m.sourcePane(m.width-m.sideW-1, mainH), divider(mainH, lit("side")), m.sidePanes(m.sideW, mainH)))
	case m.showSide:
		rows = append(rows, m.sidePanes(m.width, mainH))
	default:
		rows = append(rows, m.sourcePane(m.width, mainH))
	}
	if m.width >= splitLogsAt {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top,
			m.logPane("console", "lldb", &m.console, m.logW, m.logH, lit("logs")), divider(m.logH, lit("split")),
			m.logPane("program", "program output", &m.program, m.width-m.logW-1, m.logH, lit("logs"))))
	} else {
		rows = append(rows,
			m.logPane("console", "lldb", &m.console, m.width, m.logH-m.logH/2, lit("logs")),
			m.logPane("program", "program output", &m.program, m.width, m.logH/2, false))
	}
	rows = append(rows, choices...)
	rows = append(rows, zone.Mark("prompt", m.promptView()))
	return joinLines(rows)
}

// logPane is one of the two logs.
func (m *model) logPane(id, title string, log *viewport.Model, width, height int, lit bool) string {
	strip, body := panel(heading(title, false), "", splitLines(log.View()), width, height, colMantle, false, lit,
		scroll{log.TotalLineCount(), log.YOffset})
	return zone.Mark(id, stack(strip, body))
}

// statusBar is the top line: at the left what has the keys, what is being
// debugged and how it stands; at the right the buttons, with their names and
// keys as far as there is room.
func (m *model) statusBar() string {
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
	turning := spinner[m.frame%len(spinner)]
	dot, colour := "●", colOverlay
	switch m.snap.State {
	case session.Stopped:
		colour = colYellow
	case session.Running:
		dot, colour = turning, colGreen
	case session.Building, session.Starting:
		dot, colour = turning, colBlue
	case session.Failed:
		dot, colour = "✖", colRed
	case session.Exited:
		dot = "■"
	}
	left := mode + styText.Bold(true).Render("  "+name) +
		lipgloss.NewStyle().Foreground(colour).Bold(true).Render("   "+dot+" "+m.snap.State)
	if m.snap.Detail != "" {
		left += stySub.Render("  " + m.snap.Detail)
	}
	if got := int(m.progress.Load()) - 1; got >= 0 {
		done := min(got/10, 10)
		left += styAccent.Render("   debugger "+repeat("▰", done)) + styDim.Render(repeat("▱", 10-done)) + stySub.Render(fmt.Sprintf(" %d%%", got))
	}
	if keys := m.count + m.prefix; keys != "" {
		left += styHit.Render("   " + keys)
	}
	// The fullest form of the buttons that leaves the left its room.
	right := ""
	for form := 2; form >= 0; form-- {
		right = ""
		for _, tool := range tools {
			label := " " + tool.icon + " "
			if form >= 1 {
				label = " " + tool.icon + " " + tool.name + " "
			}
			if form == 2 {
				label += styToolKey.Render(tool.key + " ")
			}
			right += " " + m.spot("tool:"+tool.action, styTool.Render(label))
		}
		if lipgloss.Width(left)+lipgloss.Width(right)+2 <= m.width {
			break
		}
	}
	if lipgloss.Width(left)+lipgloss.Width(right) > m.width {
		right = ""
	}
	return paint(fit(left, m.width-lipgloss.Width(right))+right, colCrust)
}

func (m *model) sourcePane(width, height int) string {
	if m.src.path == "" {
		message := m.noSource
		if message == "" {
			message = map[string]string{
				session.NoTarget: "ctrl+t picks a target to debug.",
				session.Building: "Building…",
				session.Starting: "Starting…",
				session.Stopped:  "ctrl+p opens a file. A breakpoint, then f5, and the program stops in one.",
				session.Running:  "Running. ctrl+c pauses it.",
				session.Exited:   "The program exited. ctrl+r rebuilds and starts it again.",
				session.Failed:   "It did not start. The lldb log below says why.",
			}[m.snap.State]
		} else {
			message = strings.ToUpper(message[:1]) + message[1:] + ". ctrl+p opens a file."
		}
		strip, body := panel(heading("source", m.inSource), "", []string{"", "   " + styDim.Render(message)},
			width, height, colBase, m.inSource, false, scroll{})
		return stack(strip, zone.Mark("source", body))
	}
	pc, marks := 0, map[int]string{}
	if view := m.snap.View; view != nil && view.Frames[view.Index].Path == m.src.path {
		pc = view.Frames[view.Index].Line
	}
	for _, id := range m.breakIDs() {
		if point := m.snap.Breaks[id]; point.Path == m.src.path && marks[point.Line] == "" {
			marks[point.Line] = breakMark(point)
		}
	}
	// In the strip: the files open, and where the cursor is in this one.
	where := fmt.Sprintf(" %d:%d ", m.src.line, m.src.col+1)
	switch {
	case m.noSource != "":
		where = " " + m.noSource + " ·" + where
	case m.snap.Newer[m.src.path]:
		where = " edited since the build ·" + where
	}
	where = stySub.Render(where)
	strip, body := panel(m.tabBar(width-2-lipgloss.Width(where)), where, m.src.render(pc, marks, m.notes(pc), m.inSource),
		width, height, colBase, m.inSource, false, scroll{len(m.src.plain), m.src.top - 1})
	return stack(strip, zone.Mark("source", body))
}

// window is the part of a list that fits a pane, scrolled to top, which it
// keeps within the list, and where in the list that is.
func window(lines []string, top *int, height int) ([]string, scroll) {
	height = max(height, 0)
	*top = max(0, min(*top, len(lines)-height))
	if len(lines) <= height {
		return lines, scroll{}
	}
	return lines[*top : *top+height], scroll{len(lines), *top}
}

func (m *model) choicesView() []string {
	if m.choicesFor == "" {
		return nil
	}
	first := max(0, min(m.choiceAt-choicesMax/2, len(m.choices)-choicesMax))
	var rows []string
	for i := first; i < len(m.choices) && i < first+choicesMax; i++ {
		c := m.choices[i]
		row := "   " + c.Name + "  " + styDim.Render(c.Type)
		if i == m.choiceAt {
			row = stySelect.Render(fit(" ❯ "+c.Name+"  "+c.Type, m.width))
		}
		rows = append(rows, zone.Mark(fmt.Sprintf("choice:%d", i), paint(fit(row, m.width), colCrust)))
	}
	return rows
}

// promptView is the bottom line, where commands are typed. It is lit while
// it has the keys.
func (m *model) promptView() string {
	mark := "lldb ❯ "
	if m.asking != "" {
		mark = asks[m.asking].mark
		if m.asking == "find" && m.src.back {
			mark = "? "
		}
	}
	bg := colSurface
	if m.inSource {
		m.input.Blur()
		bg = colMantle
	} else {
		m.input.Focus()
	}
	return paint(fit(" "+styAccent.Bold(true).Render(mark)+m.input.View(), m.width), bg)
}
