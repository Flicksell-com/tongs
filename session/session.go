// Package session is one debugging session, with no screen attached: it
// builds a target, runs it under lldb, and keeps track of where it is. The
// terminal UI and the agent socket are both views onto it.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Flicksell-com/tongs/dap"
	"github.com/Flicksell-com/tongs/store"
)

// The states a session is in.
const (
	NoTarget = "no target"
	Ready    = "ready" // a target is open; nothing of it is running
	Building = "building"
	Starting = "starting"
	Running  = "running"
	Stopped  = "stopped"
	Exited   = "exited"
	Failed   = "failed"
)

// Frame is one frame of the stack. Path is empty where there is no source.
type Frame struct {
	ID   int
	Name string
	Path string
	Line int
}

// Var is a variable. Ref is nonzero when it has parts to list with Parts.
// Err is set for an expression that could not be worked out; Value says why.
type Var struct {
	Name, Value, Type string
	Ref               int
	Err               bool
}

// Thread is one thread of the stopped program.
type Thread struct {
	Index    int    // as lldb numbers it, for `thread select`
	Where    string // the function it is in, and the line where that is known
	Reason   string // why it stopped, if it was the one that did
	Selected bool
}

// View is what the program looks like where it is stopped.
type View struct {
	// Seq goes up each time the program is looked at again, whether or not
	// it moved: a command may have changed a value where it stands.
	Seq int
	// Run goes up each time the program is set running: two looks with the
	// same Run are of the program standing in the same place.
	Run     int
	Frames  []Frame
	Index   int // the frame selected
	Locals  []Var
	Watches []Var // the watched expressions, in this frame; Name is the expression
	Threads []Thread
	// Decl is the line each of the frame's variables is declared on, in the
	// frame's file as the program was built.
	Decl map[string]int
}

// Place is a line in a source file.
type Place struct {
	Path string
	Line int
}

// Break is a breakpoint: where it is and how it is set. Its place is the line
// it was asked for on. Lands is the line the program stops on for it, where
// that is another: the next with code, for one asked for on a line with none.
type Break struct {
	Place
	Lands     int
	Function  string // for one set on a function by its name
	Unplaced  bool   // the running program has no code it could be put on
	Enabled   bool
	Condition string // stops only when this holds; "" for always
	Log       string // printed instead of stopping; "" for one that stops
	Hits      int
}

// Line is one line of the lldb log. Kind is "note", "lldb" or "agent" for a
// command typed by that party, "out" for what lldb printed, "error".
type Line struct {
	Kind, Text string
}

// Snapshot is the session's state at one moment, safe to keep and read.
type Snapshot struct {
	State, Detail string
	Entry         *store.Entry
	Root          string
	View          *View
	Breaks        map[int]Break
	Watches       []string // the expressions watched, stopped or not
	// The watched files that have been edited: how many times each has
	// changed on disk, and which are newer than the program. The lines in
	// View and Breaks are the files' lines as they are on disk.
	Edits map[string]int
	Newer map[string]bool
}

type where struct {
	thread, frame int
	info          string
}

// Session is safe for use from several goroutines.
type Session struct {
	Home    string // where the targets are kept
	adapter string
	// OnChange is called, from any goroutine, whenever there is something
	// new to show. It must not block.
	OnChange func()

	// One conversation with lldb at a time: a command and the look at where
	// it left things must not interleave with the next.
	talk sync.Mutex
	// One start at a time; a second waits for the first to settle.
	starting sync.Mutex

	mu     sync.Mutex
	client *dap.Client
	gen    int // which client's events still count
	state  string
	detail string
	entry  *store.Entry
	live   *store.Entry // the target the running client was started for
	root   string
	binary string
	view   *View
	shown  *where
	breaks map[int]Place
	// The target's breakpoints (see points.go). breaks is where the
	// debugger put each of its own, by its number for it; bound says a
	// debugger has been given them, and changes are to go through it.
	points    []*point
	nextPoint int
	bound     bool
	watches   []string
	seq       int
	console   []Line
	program   []string
	files     []string
	// Counts the program being set running, to tell a command that ran it
	// from one that only printed.
	resumes     int
	stoppedOnce bool
	runOn       bool // a restart runs on from the entry stop by itself
	initialized chan struct{}
	entryStop   chan struct{}
	// What commands have printed that the adapter has yet to say again as
	// events (see repl), and the events kept back while one is awaited.
	echo     string
	held     []string
	awaiting bool
	// The source files watched for edits (see drift.go), and when the
	// program was built.
	tracked map[string]*tracked
	builtAt time.Time
	done    chan struct{}
}

var (
	frameRe = regexp.MustCompile(`frame #(\d+):`)
	tidRe   = regexp.MustCompile(`tid = (0x[0-9a-fA-F]+|\d+)`)
)

// New makes a session with nothing loaded.
func New(home, adapter string) *Session {
	s := &Session{Home: home, adapter: adapter, root: home, state: NoTarget, breaks: map[int]Place{},
		tracked: map[string]*tracked{}, done: make(chan struct{})}
	go s.watch()
	return s
}

// SetAdapter changes the debugger the next start uses.
func (s *Session) SetAdapter(adapter string) {
	s.mu.Lock()
	s.adapter = adapter
	s.mu.Unlock()
}

func (s *Session) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// Snapshot copies out the state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{State: s.state, Detail: s.detail, Entry: s.entry, Root: s.root, Breaks: map[int]Break{},
		Watches: append([]string(nil), s.watches...), Edits: map[string]int{}, Newer: map[string]bool{}}
	for path, t := range s.tracked {
		if t.edits > 0 {
			snap.Edits[path] = t.edits
		}
		if t.newer {
			snap.Newer[path] = true
		}
	}
	snap.Breaks = s.describe()
	if s.view != nil && s.state == Stopped {
		view := *s.view
		view.Frames = append([]Frame(nil), view.Frames...)
		for i, frame := range view.Frames {
			view.Frames[i].Line = s.tracked[frame.Path].now(frame.Line)
		}
		// The declarations are in the selected frame's file.
		file := s.tracked[view.Frames[view.Index].Path]
		view.Decl = map[string]int{}
		for name, line := range s.view.Decl {
			view.Decl[name] = file.now(line)
		}
		snap.View = &view
	}
	return snap
}

// Console is the lldb log from line `from` on, and Program the program's
// output likewise.
func (s *Session) Console(from int) []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Line(nil), s.console[min(from, len(s.console)):]...)
}

func (s *Session) Program(from int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.program[min(from, len(s.program)):]...)
}

// Note adds a line to the lldb log.
func (s *Session) Note(kind, text string) {
	s.mu.Lock()
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		s.console = append(s.console, Line{kind, line})
	}
	s.mu.Unlock()
	s.changed()
}

func (s *Session) setState(state, detail string) {
	s.mu.Lock()
	s.state, s.detail = state, detail
	s.mu.Unlock()
	s.changed()
}

// Load opens a target without running it: its breakpoints can be seen and
// changed, and Restart builds and starts it. Whatever was running is ended.
func (s *Session) Load(entry store.Entry) {
	s.mu.Lock()
	s.entry = &entry
	s.root = entry.Project(s.Home)
	s.state, s.detail = Ready, ""
	s.view, s.shown, s.files, s.bound = nil, nil, nil, false
	s.mu.Unlock()
	s.loadPoints(entry)
	s.changed()
	go func() {
		// Behind a start that is under way, which must come to rest first.
		s.starting.Lock()
		defer s.starting.Unlock()
		s.mu.Lock()
		old := s.client
		s.client, s.live = nil, nil
		s.gen++
		if s.entry != nil && s.entry.PointsFile(s.Home) == entry.PointsFile(s.Home) && s.state != Building {
			s.state, s.detail = Ready, ""
		}
		s.mu.Unlock()
		if old != nil {
			old.Close()
		}
		s.changed()
	}()
}

// Start builds the target and starts it under the debugger, in the
// background. With runOn it runs until something stops it; without, it stops
// before its first instruction.
func (s *Session) Start(entry store.Entry, runOn bool) {
	s.mu.Lock()
	other := s.entry == nil || s.entry.PointsFile(s.Home) != entry.PointsFile(s.Home)
	s.entry = &entry
	s.root = entry.Project(s.Home)
	s.state, s.detail = Building, "" // Now, so anyone waiting sees it begin.
	s.mu.Unlock()
	if other {
		s.loadPoints(entry)
	}
	s.changed()
	go s.start(entry, runOn)
}

// Restart builds the open target and starts it, or starts it again.
func (s *Session) Restart() {
	s.mu.Lock()
	entry := s.entry
	s.mu.Unlock()
	if entry != nil {
		s.Start(*entry, true)
	}
}

func (s *Session) start(entry store.Entry, restart bool) {
	s.starting.Lock()
	defer s.starting.Unlock()

	// Let go of the old client before ending it, so that its last words (the
	// program was killed, the adapter exited) are not taken for news.
	s.mu.Lock()
	old := s.client
	s.client, s.live = nil, nil
	s.gen++
	gen := s.gen
	s.view, s.shown, s.files = nil, nil, nil
	s.breaks, s.bound = map[int]Place{}, false
	for _, p := range s.points {
		p.lldb, p.lands, p.hits, p.unplaced = 0, 0, 0, false
	}
	s.stoppedOnce, s.runOn = false, restart
	s.echo, s.held = "", nil
	s.initialized, s.entryStop = make(chan struct{}), make(chan struct{})
	initialized, entryStop := s.initialized, s.entryStop
	root := s.root
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	s.setState(Building, "")

	binary, err := resolve(root, entry, func(line string) { s.Note("note", line) })
	if err != nil {
		s.setState(Failed, err.Error())
		return
	}
	s.rebase(entry, binary)
	s.setState(Starting, "")

	// At the root, so the workspace-relative paths in Bazel's debug info resolve.
	client, err := dap.Start(s.adapter, root, cleanEnv(), func(name string, body map[string]any) {
		s.event(gen, name, body)
	})
	if err != nil {
		s.setState(Failed, err.Error())
		return
	}
	s.mu.Lock()
	s.client, s.live, s.binary = client, &entry, binary
	s.mu.Unlock()

	fail := func(err error) { s.setState(Failed, err.Error()) }
	if _, err := client.Request("initialize", map[string]any{
		"adapterID": "tongs", "clientID": "tongs",
		"linesStartAt1": true, "columnsStartAt1": true, "pathFormat": "path",
	}); err != nil {
		fail(err)
		return
	}
	// `bazelisk run` starts a program in its runfiles; do the same.
	cwd := root
	if info, err := os.Stat(binary + ".runfiles/_main"); err == nil && info.IsDir() {
		cwd = binary + ".runfiles/_main"
	}
	args := entry.Args
	if args == nil {
		args = []string{}
	}
	launched := make(chan error, 1)
	go func() {
		_, err := client.Request("launch", map[string]any{
			"program": binary, "args": args, "env": entry.Env, "cwd": cwd,
			"stopOnEntry": true,
			// Send the program's output here rather than asking for a terminal.
			"terminal": "console",
			// macOS records "./pkg/file.cpp"; Linux records the sandbox's /proc/self/cwd.
			"relativePathBase": root,
			"sourceMap":        map[string]string{".": root, "/proc/self/cwd": root},
		})
		launched <- err
	}()
	select {
	case <-initialized:
		if _, err = client.Request("configurationDone", nil); err == nil {
			err = <-launched
		}
	case err = <-launched:
	}
	if err != nil {
		fail(err)
		return
	}
	<-entryStop
	if !client.Alive() {
		return
	}

	// Under the lock: the entry stop has the view asking lldb where it is.
	s.talk.Lock()
	s.apply()
	// Breakpoints kept the old way, in a file lldb wrote: read once, taken
	// in as the target's own, and the file done with.
	if old := entry.BreakpointsFile(s.Home); len(s.Snapshot().Breaks) == 0 {
		if _, err := os.Stat(old); err == nil {
			s.repl(fmt.Sprintf(`breakpoint read -f "%s"`, old))
			s.mirror()
		}
		os.Remove(old)
	} else {
		os.Remove(old)
	}
	if restart {
		s.repl("process continue")
	}
	s.talk.Unlock()
	if !restart {
		had := ""
		if n := len(s.Snapshot().Breaks); n > 0 {
			had = fmt.Sprintf("%d breakpoints set; ", n)
		}
		s.Note("note", "stopped at entry: "+had+"c runs it")
	}
}

func (s *Session) event(gen int, name string, body map[string]any) {
	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return // A client already replaced.
	}
	switch name {
	case "initialized":
		closeOnce(s.initialized)
	case "breakpoint":
		// However it was set: the prompt, a key, an agent, a restart.
		point, _ := body["breakpoint"].(map[string]any)
		id := num(point["id"])
		source, _ := point["source"].(map[string]any)
		path, _ := source["path"].(string)
		if body["reason"] == "removed" {
			delete(s.breaks, id)
		} else if path != "" && num(point["line"]) > 0 {
			if !filepath.IsAbs(path) {
				path = filepath.Join(s.root, path)
			}
			s.breaks[id] = Place{path, num(point["line"])}
			go s.Watch(path) // Its lines are to be followed if it is edited.
		}
	case "output":
		if !s.stoppedOnce {
			break // The program hasn't run yet; this is the adapter starting up.
		}
		text, _ := body["output"].(string)
		text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		switch category, _ := body["category"].(string); {
		case category == "stdout" || category == "stderr":
			s.program = append(s.program, strings.Split(text, "\n")...)
		case s.awaiting:
			s.held = append(s.held, text)
		default:
			s.said(text)
		}
	case "stopped":
		entry := !s.stoppedOnce
		s.stoppedOnce = true
		closeOnce(s.entryStop)
		if entry && s.runOn {
			break
		}
		s.state = Stopped
		s.detail, _ = body["description"].(string)
		if entry {
			s.detail = "entry"
		} else if s.detail == "" {
			s.detail, _ = body["reason"].(string)
		}
		go s.Sync()
	case "continued":
		s.resumes++
		if s.state == Stopped || s.state == Starting {
			s.state, s.detail = Running, ""
		}
	case "exited":
		s.state, s.detail = Exited, fmt.Sprintf("status %d", num(body["exitCode"]))
		s.view, s.shown = nil, nil
	case "adapter-exited":
		s.bound = false // Nothing left to tell of a change.
		closeOnce(s.entryStop)
		if s.state == Starting || s.state == Running || s.state == Stopped {
			s.state, s.detail = Failed, "the debugger exited"
		}
	}
	s.mu.Unlock()
	s.changed()
}

func closeOnce(c chan struct{}) {
	select {
	case <-c:
	default:
		close(c)
	}
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func (s *Session) current() *dap.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// repl runs one lldb command and returns what it printed. The adapter gives
// the output in its response, or a failure as the response's error. It also
// says the output again as console events, and those come when they come:
// before the response or after it, even after the next command's. So they are
// told from what lldb says of its own accord by their text, and dropped.
// Callers hold talk.
func (s *Session) repl(command string) string {
	client := s.current()
	if client == nil {
		return "error: the program isn't running. Run starts it (ctrl+r)."
	}
	s.mu.Lock()
	s.awaiting = true
	s.mu.Unlock()
	body, err := client.Request("evaluate", map[string]any{"expression": command, "context": "_command"})
	result, _ := body["result"].(string)
	s.mu.Lock()
	s.awaiting = false
	s.echo += spaceless(result)
	held := s.held
	s.held = nil
	for _, text := range held {
		s.said(text)
	}
	s.mu.Unlock()
	if err != nil {
		if text := err.Error(); strings.HasPrefix(text, "error:") {
			return text
		}
		return "error: " + err.Error()
	}
	return strings.TrimRight(result, "\n")
}

func spaceless(text string) string { return strings.Join(strings.Fields(text), "") }

// said takes console output from the adapter: nothing, if it repeats what a
// command's response already gave; a line in the log otherwise. The two differ
// in where they break lines, so they are compared without their spaces.
// Callers hold mu.
func (s *Session) said(text string) {
	bare := spaceless(text)
	if bare == "" {
		return
	}
	if at := strings.Index(s.echo, bare); at >= 0 {
		// Events come in the order they were printed: what is ahead of this
		// one in the echo is not coming.
		s.echo = s.echo[at+len(bare):]
		return
	}
	for _, line := range strings.Split(text, "\n") {
		s.console = append(s.console, Line{"out", line})
	}
}

// where is lldb's selected thread and frame, or nil when nothing is stopped.
func (s *Session) where() *where {
	thread := tidRe.FindStringSubmatch(s.repl("thread info"))
	info := s.repl("frame info")
	frame := frameRe.FindStringSubmatch(info)
	if thread == nil || frame == nil {
		return nil
	}
	tid, _ := strconv.ParseInt(thread[1], 0, 64)
	index, _ := strconv.Atoi(frame[1])
	return &where{int(tid), index, info}
}

func same(a, b *where) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// sync brings the view to where lldb is. Callers hold talk.
func (s *Session) sync() {
	at := s.where()
	s.mu.Lock()
	moved := !same(at, s.shown)
	s.shown = at
	s.mu.Unlock()
	if moved && at != nil {
		s.look(at)
	}
	s.mirror()
}

// look reads the stopped program where lldb has it selected: the stack, the
// frame's variables, the watches, the threads. Callers hold talk.
func (s *Session) look(at *where) {
	client := s.current()
	if client == nil {
		return
	}
	stack, err := client.Request("stackTrace", map[string]any{"threadId": at.thread})
	if err != nil {
		return // It ran on before we could look; the next stop draws again.
	}
	var frames []Frame
	list, _ := stack["stackFrames"].([]any)
	for _, item := range list {
		f, _ := item.(map[string]any)
		frame := Frame{ID: num(f["id"]), Line: num(f["line"])}
		frame.Name, _ = f["name"].(string)
		if source, _ := f["source"].(map[string]any); source != nil && num(source["sourceReference"]) == 0 {
			if path, _ := source["path"].(string); path != "" {
				if _, err := os.Stat(path); err == nil {
					frame.Path = path
				}
			}
		}
		frames = append(frames, frame)
	}
	if at.frame >= len(frames) {
		return
	}
	id := frames[at.frame].ID
	view := &View{Frames: frames, Index: at.frame, Locals: s.locals(client, id),
		Watches: s.watched(client, id), Threads: s.threads(), Decl: s.declared()}
	for _, frame := range frames {
		s.Watch(frame.Path)
	}
	s.mu.Lock()
	s.seq++
	view.Seq, view.Run = s.seq, s.resumes
	s.view = view
	s.mu.Unlock()
	s.changed()
}

func (s *Session) locals(client *dap.Client, frame int) []Var {
	scopes, err := client.Request("scopes", map[string]any{"frameId": frame})
	if err != nil {
		return nil
	}
	list, _ := scopes["scopes"].([]any)
	for _, item := range list {
		scope, _ := item.(map[string]any)
		if name, _ := scope["name"].(string); strings.HasPrefix(strings.ToLower(name), "local") {
			return variables(client, num(scope["variablesReference"]))
		}
	}
	return nil
}

func variables(client *dap.Client, ref int) []Var {
	body, err := client.Request("variables", map[string]any{"variablesReference": ref})
	if err != nil {
		return nil
	}
	var vars []Var
	list, _ := body["variables"].([]any)
	for _, item := range list {
		v, _ := item.(map[string]any)
		one := Var{Ref: num(v["variablesReference"])}
		one.Name, _ = v["name"].(string)
		one.Value, _ = v["value"].(string)
		one.Type, _ = v["type"].(string)
		vars = append(vars, one)
	}
	return vars
}

// Parts lists what a variable is made of.
func (s *Session) Parts(ref int) []Var {
	if client := s.current(); client != nil {
		return variables(client, ref)
	}
	return nil
}

// Sync brings the view up to date with lldb.
func (s *Session) Sync() {
	s.talk.Lock()
	s.sync()
	s.talk.Unlock()
}

// Execute sends a command to lldb and returns what it printed and whether it
// moved the program or the frame. One that moved has said enough on screen;
// anything else is logged with its output, if it had any, so that a command
// which prints nothing is still seen to have run. An agent's commands are
// always logged, so the person watching can follow.
func (s *Session) Execute(command, who string) (string, bool) {
	s.talk.Lock()
	before := s.where()
	s.mu.Lock()
	resumes := s.resumes
	s.mu.Unlock()
	output := s.repl(command)
	s.sync()
	s.mu.Lock()
	moved := !same(before, s.shown) || s.resumes != resumes
	at := s.shown
	s.mu.Unlock()
	if !moved && at != nil {
		s.look(at) // It may have changed a value where the program stands.
	}
	s.talk.Unlock()

	if who != "lldb" || !moved {
		s.mu.Lock()
		s.console = append(s.console, Line{who, command})
		s.mu.Unlock()
		if !moved && output != "" {
			kind := "out"
			if strings.HasPrefix(output, "error:") {
				kind = "error"
			}
			s.Note(kind, output)
		}
		s.changed()
	}
	return output, moved
}

// Settle waits for a build, a start or a running program to come to rest,
// then brings the view up to date.
func (s *Session) Settle(wait time.Duration) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if state := s.Snapshot().State; state != Building && state != Starting && state != Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Sync()
}

// Pause stops a running program.
func (s *Session) Pause() {
	client := s.current()
	if client == nil || s.Snapshot().State != Running {
		return
	}
	threads, err := client.Request("threads", nil)
	if err != nil {
		return
	}
	if list, _ := threads["threads"].([]any); len(list) > 0 {
		first, _ := list[0].(map[string]any)
		client.Request("pause", map[string]any{"threadId": num(first["id"])})
	}
}

// Close ends the session and the program with it.
func (s *Session) Close() {
	closeOnce(s.done)
	s.mu.Lock()
	client := s.client
	s.client = nil
	s.gen++
	s.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

// built is the line the program knows for a line of a file as it is on disk.
// It has none for a line written since the program was built, and says so.
func (s *Session) built(path string, line int) (int, bool) {
	s.mu.Lock()
	line, ok := s.tracked[path].then(line)
	s.mu.Unlock()
	if !ok {
		s.Note("note", "That line was written after the program was built, so the program has no such line. ctrl+r rebuilds.")
	}
	return line, ok
}

// RunTo runs until a line is reached.
func (s *Session) RunTo(path string, line int) {
	line, ok := s.built(path, line)
	if !ok {
		return
	}
	s.talk.Lock()
	s.repl(fmt.Sprintf(`breakpoint set --one-shot true --file "%s" --line %d`, path, line))
	s.talk.Unlock()
	s.Execute("continue", "lldb")
}
