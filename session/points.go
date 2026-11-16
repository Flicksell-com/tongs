package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Flicksell-com/tongs/store"
)

// A target's breakpoints are its own. tongs keeps them, shows them and
// changes them whether or not a program is running; a debugger is given them
// when the program starts. While one has them, what it has is what there is:
// a breakpoint set or removed at the prompt is taken in here (see mirror).

// point is one breakpoint.
type point struct {
	ID        int    `json:"-"`
	Path      string `json:"path"`               // the file it is in
	Line      int    `json:"line"`               // the line asked for, in the file as it is on disk
	Function  string `json:"function,omitempty"` // for one set on a function by its name
	Enabled   bool   `json:"enabled"`
	Condition string `json:"condition,omitempty"`
	Log       string `json:"log,omitempty"`

	// What the debugger says of it, while one has it.
	lldb     int  // its number there, 0 for none
	lands    int  // the line it stops on, in the file as it is on disk
	hits     int  // how often it was hit
	unplaced bool // it found no code for it
}

var setRe = regexp.MustCompile(`^Breakpoint (\d+):`)

// loadPoints reads a target's breakpoints in.
func (s *Session) loadPoints(entry store.Entry) {
	var points []*point
	if raw, err := os.ReadFile(entry.PointsFile(s.Home)); err == nil {
		json.Unmarshal(raw, &points)
	}
	s.mu.Lock()
	s.points = nil
	for _, p := range points {
		if p != nil && (p.Path != "" || p.Function != "") {
			s.nextPoint++
			p.ID = s.nextPoint
			s.points = append(s.points, p)
		}
	}
	points = s.points
	s.mu.Unlock()
	for _, p := range points {
		s.Watch(p.Path) // Its line is to follow the file's edits.
	}
}

// savePoints writes the target's breakpoints out. It is done at every
// change: they are few, and nothing is then lost to a crash.
func (s *Session) savePoints() {
	s.mu.Lock()
	entry := s.entry
	raw, err := json.MarshalIndent(s.points, "", "  ")
	s.mu.Unlock()
	if entry == nil || err != nil {
		return
	}
	path := entry.PointsFile(s.Home)
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		os.WriteFile(path, append(raw, '\n'), 0o600)
	}
}

// find is the breakpoint with an id, or nil. Callers hold mu.
func (s *Session) find(id int) *point {
	for _, p := range s.points {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// drop forgets a breakpoint. Callers hold mu.
func (s *Session) drop(id int) {
	for i, p := range s.points {
		if p.ID == id {
			s.points = append(s.points[:i], s.points[i+1:]...)
			return
		}
	}
}

// edit changes a breakpoint where there is no debugger to tell.
func (s *Session) edit(id int, change func(*point)) {
	s.mu.Lock()
	if p := s.find(id); p != nil {
		change(p)
	}
	s.mu.Unlock()
	s.savePoints()
	s.changed()
}

// heldBy is a breakpoint's number in the debugger, if one has it.
func (s *Session) heldBy(id int) (lldb int, held bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.find(id); p != nil && s.bound && p.lldb != 0 {
		return p.lldb, true
	}
	return 0, false
}

// bind gives the debugger a breakpoint. Callers hold talk.
func (s *Session) bind(p *point) {
	s.mu.Lock()
	path, line, function := p.Path, p.Line, p.Function
	enabled, condition, log := p.Enabled, p.Condition, p.Log
	if function == "" {
		// The program knows the file as it was built from.
		built, known := s.tracked[path].then(line)
		if !known {
			p.unplaced = true
			s.mu.Unlock()
			return
		}
		line = built
	}
	s.mu.Unlock()

	command := fmt.Sprintf("breakpoint set --file %s --line %d", quote(path), line)
	if function != "" {
		command = "breakpoint set --name " + quote(function)
	}
	found := setRe.FindStringSubmatch(s.repl(command))
	if found == nil {
		return
	}
	id, _ := strconv.Atoi(found[1])
	if condition != "" {
		s.repl(fmt.Sprintf("breakpoint modify --condition %s %d", quote(condition), id))
	}
	if !enabled {
		s.repl(fmt.Sprintf("breakpoint disable %d", id))
	}
	if log != "" {
		s.repl(fmt.Sprintf(`breakpoint command add --script-type python %d --one-liner "%s"`, id, logScript(log)))
	}
	s.mu.Lock()
	p.lldb = id
	s.mu.Unlock()
}

// apply gives a debugger just started every breakpoint. Callers hold talk.
func (s *Session) apply() {
	for {
		s.mu.Lock()
		var next *point
		for _, p := range s.points {
			if p.lldb == 0 && !p.unplaced {
				next = p
				break
			}
		}
		if next == nil {
			s.bound = true // From here on a change goes through the debugger.
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		s.bind(next)
		s.mu.Lock()
		if next.lldb == 0 {
			next.unplaced = true // The debugger would not have it.
		}
		s.mu.Unlock()
	}
	s.mirror()
}

// Asks lldb's own scripting for what `breakpoint list` only prints as prose:
// for each breakpoint its number, whether it is on, how often it was hit, its
// condition, the line it was asked for on (0 if it was not asked for by line),
// whether it is to be hit only once, and the function it was asked for by.
// The last three are only in a breakpoint's description of itself.
const breakScript = `script import json; print(json.dumps([` +
	`(lambda o: [b.GetID(), b.IsEnabled(), b.GetHitCount(), b.GetCondition() or "", o.get("LineNumber", 0), b.IsOneShot(), (o.get("SymbolNames") or [""])[0]])` +
	`((lambda s: (b.SerializeToStructuredData().GetAsJSON(s), json.loads(s.GetData() or "{}"))[1])(lldb.SBStream())` +
	`.get("Breakpoint", {}).get("BKPTResolver", {}).get("Options", {})) for b in lldb.target.breakpoint_iter()]))`

// mirror brings the breakpoints in line with what the debugger has: how each
// is set and where it landed, any set at the prompt taken in, any deleted
// there dropped. Callers hold talk.
func (s *Session) mirror() {
	var rows [][]any
	if json.Unmarshal([]byte(s.repl(breakScript)), &rows) != nil {
		return
	}
	s.mu.Lock()
	if !s.bound {
		s.mu.Unlock()
		return // Still being given them: nothing to compare with yet.
	}
	before, _ := json.Marshal(s.describe())
	held := map[int]*point{}
	for _, p := range s.points {
		if p.lldb != 0 {
			held[p.lldb] = p
		}
	}
	seen := map[int]bool{}
	for _, row := range rows {
		if once, _ := row[5].(bool); len(row) != 7 || once {
			continue // One of the session's own, for running to a line.
		}
		id, asked := num(row[0]), num(row[4])
		function, _ := row[6].(string)
		seen[id] = true
		// Where the debugger put it, in the file as the program was built.
		place, placed := s.breaks[id]
		p := held[id]
		if p == nil {
			// Set at the prompt, or by an agent: taken in.
			p = &point{lldb: id}
			switch {
			case asked > 0 && placed:
				p.Path, p.Line = place.Path, s.tracked[place.Path].now(asked)
			case function != "":
				p.Function = function
			case placed:
				p.Path, p.Line = place.Path, s.tracked[place.Path].now(place.Line)
			default:
				continue // Nothing to show it by.
			}
			s.nextPoint++
			p.ID = s.nextPoint
			s.points = append(s.points, p)
		}
		p.Enabled, _ = row[1].(bool)
		p.hits = num(row[2])
		p.Condition, _ = row[3].(string)
		p.unplaced, p.lands = !placed, 0
		if placed {
			at := s.tracked[place.Path].now(place.Line)
			if p.Function != "" {
				p.Path, p.Line = place.Path, at // Shown where its function is.
			} else if at != p.Line {
				p.lands = at
			}
		}
	}
	kept := s.points[:0]
	for _, p := range s.points {
		if p.lldb == 0 || seen[p.lldb] {
			kept = append(kept, p)
		}
	}
	s.points = kept
	after, _ := json.Marshal(s.describe())
	s.mu.Unlock()
	if string(before) != string(after) {
		s.savePoints()
		s.changed()
	}
}

// describe is the breakpoints as a snapshot gives them. Callers hold mu.
func (s *Session) describe() map[int]Break {
	out := map[int]Break{}
	for _, p := range s.points {
		b := Break{Place: Place{p.Path, p.Line}, Function: p.Function, Enabled: p.Enabled,
			Condition: p.Condition, Log: p.Log, Hits: p.hits, Unplaced: s.bound && p.unplaced}
		if p.lands != p.Line {
			b.Lands = p.lands
		}
		out[p.ID] = b
	}
	return out
}

// BreakOn is the breakpoint on a line of a file as it is on disk, if any.
func (s *Session) BreakOn(path string, line int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.points {
		if p.Path == path && p.Line == line {
			return p.ID, true
		}
	}
	return 0, false
}

// ToggleBreak sets a breakpoint on a line, or removes the one there. Any line
// can be asked for: on one with no code the breakpoint stays marked where it
// was put, and stops the program on the next line that has some.
func (s *Session) ToggleBreak(path string, line int) {
	if id, ok := s.BreakOn(path, line); ok {
		s.RemoveBreak(id)
		return
	}
	s.mu.Lock()
	s.nextPoint++
	p := &point{ID: s.nextPoint, Path: path, Line: line, Enabled: true}
	s.points = append(s.points, p)
	bound := s.bound
	s.mu.Unlock()
	s.Watch(path)
	if bound {
		s.talk.Lock()
		s.bind(p)
		s.sync()
		s.talk.Unlock()
	}
	s.savePoints()
	s.changed()
}

// RemoveBreak deletes a breakpoint.
func (s *Session) RemoveBreak(id int) {
	if lldb, held := s.heldBy(id); held {
		s.quiet(fmt.Sprintf("breakpoint delete %d", lldb)) // mirror drops it.
		return
	}
	s.mu.Lock()
	s.drop(id)
	s.mu.Unlock()
	s.savePoints()
	s.changed()
}

// SetEnabled turns a breakpoint on or off, leaving it where it is.
func (s *Session) SetEnabled(id int, on bool) {
	if lldb, held := s.heldBy(id); held {
		s.quiet(fmt.Sprintf("breakpoint %s %d", map[bool]string{true: "enable", false: "disable"}[on], lldb))
		return
	}
	s.edit(id, func(p *point) { p.Enabled = on })
}

// SetCondition has a breakpoint stop only when an expression holds; "" has it
// stop every time.
func (s *Session) SetCondition(id int, condition string) {
	condition = strings.TrimSpace(condition)
	if lldb, held := s.heldBy(id); held {
		s.quiet(fmt.Sprintf("breakpoint modify --condition %s %d", quote(condition), lldb))
		return
	}
	s.edit(id, func(p *point) { p.Condition = condition })
}

// SetLog has a breakpoint print a message and run on rather than stop; ""
// has it stop again. An expression in {braces} is printed as its value.
func (s *Session) SetLog(id int, message string) {
	message = strings.TrimSpace(message)
	if lldb, held := s.heldBy(id); held {
		command := fmt.Sprintf("breakpoint command delete %d", lldb)
		if message != "" {
			command = fmt.Sprintf(`breakpoint command add --script-type python %d --one-liner "%s"`, lldb, logScript(message))
		}
		s.talk.Lock()
		s.repl(command)
		s.talk.Unlock()
	}
	s.edit(id, func(p *point) { p.Log = message })
}

// BreakAt sets a breakpoint on a function of the running program, by its
// name, and says where that is.
func (s *Session) BreakAt(function string) (Place, bool) {
	s.mu.Lock()
	bound := s.bound
	s.mu.Unlock()
	if !bound {
		s.Note("note", "A function is found in the running program. Run it first (ctrl+r).")
		return Place{}, false
	}
	before := s.Snapshot().Breaks
	s.quiet("breakpoint set --name " + quote(function))
	for id, point := range s.Snapshot().Breaks {
		if _, had := before[id]; !had && point.Path != "" {
			return point.Place, true
		}
	}
	return Place{}, false
}
