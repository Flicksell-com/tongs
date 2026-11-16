package session

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Flicksell-com/tongs/store"
)

// A source file can be edited while the program built from it is still
// running. lldb goes on speaking of the lines the program was built from;
// the screen shows the file as it now is. The session watches the files that
// matter - the ones shown, in the stack, or with breakpoints - and keeps, for
// each that changes, which line became which. Everything it hands out is in
// the file's lines as they are on disk, and breakpoints are put where their
// code went when the program is next built.

// tracked is one watched file.
type tracked struct {
	built []string // its lines as the program was built from; nil if not known
	lines []string // its lines now
	mod   time.Time
	size  int64
	to    []int // where each built line is now, 0 if it is gone; nil while the same
	from  []int // the built line each line now was, 0 if it is new
	newer bool  // edited since the program was built
	edits int   // how many times it has been seen to change
}

// now is where a line the program knows is in the file on disk.
func (t *tracked) now(line int) int {
	if t == nil || t.to == nil {
		return line
	}
	return Follow(t.to, len(t.lines), line)
}

// then is the line the program knows for a line of the file on disk. It has
// none for a line added or rewritten since the build.
func (t *tracked) then(line int) (int, bool) {
	if t == nil || t.from == nil {
		return line, true
	}
	if line < 1 || line > len(t.from) || t.from[line-1] == 0 {
		return 0, false
	}
	return t.from[line-1], true
}

func readLines(path string) ([]string, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n"), info, nil
}

// Watch has a file looked out for. Watching one already watched does nothing.
func (s *Session) Watch(path string) {
	s.mu.Lock()
	_, has := s.tracked[path]
	s.mu.Unlock()
	if has || path == "" {
		return
	}
	lines, info, err := readLines(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, has := s.tracked[path]; has {
		return
	}
	t := &tracked{built: lines, lines: lines, mod: info.ModTime(), size: info.Size()}
	if !s.builtAt.IsZero() && info.ModTime().After(s.builtAt) {
		// Already edited when first looked at: what the program was built
		// from is gone, so its lines can't be matched to these.
		t.built, t.newer = nil, true
	}
	s.tracked[path] = t
}

// watch looks at the watched files every second until the session closes.
func (s *Session) watch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
			s.poll()
		}
	}
}

// poll takes in the watched files that have changed on disk.
func (s *Session) poll() {
	type seen struct {
		mod  time.Time
		size int64
	}
	s.mu.Lock()
	was := map[string]seen{}
	for path, t := range s.tracked {
		was[path] = seen{t.mod, t.size}
	}
	s.mu.Unlock()

	changed, moved := false, false
	for path, before := range was {
		info, err := os.Stat(path)
		if err != nil || (info.ModTime().Equal(before.mod) && info.Size() == before.size) {
			continue
		}
		lines, info, err := readLines(path)
		if err != nil {
			continue
		}
		s.mu.Lock()
		t := s.tracked[path]
		if t == nil {
			s.mu.Unlock()
			continue
		}
		// The breakpoints in it go with their lines.
		went := Match(t.lines, lines)
		for _, p := range s.points {
			if p.Path == path {
				p.Line, moved = Follow(went, len(lines), p.Line), true
				if p.lands != 0 {
					p.lands = Follow(went, len(lines), p.lands)
				}
			}
		}
		t.lines, t.mod, t.size = lines, info.ModTime(), info.Size()
		t.edits++
		if s.builtAt.IsZero() {
			// No program has been built yet to be out of step with.
			t.built, t.to, t.from, t.newer = lines, nil, nil, false
		} else {
			first := !t.newer
			t.newer = true
			if t.built != nil {
				t.to = Match(t.built, lines)
				t.from = make([]int, len(lines))
				for built, now := range t.to {
					if now > 0 {
						t.from[now-1] = built + 1
					}
				}
			}
			if first && s.client != nil {
				s.console = append(s.console, Line{"note", filepath.Base(path) +
					" was edited; the program is older than it. ctrl+r rebuilds, and breakpoints go with their lines."})
			}
		}
		s.mu.Unlock()
		changed = true
	}
	if moved {
		s.savePoints()
	}
	if changed {
		s.changed()
	}
}

// rebase is called when a program has been built and is about to start: the
// files it was built from are the ones on disk now, so the lines on disk
// become the ones to measure from.
func (s *Session) rebase(entry store.Entry, binary string) {
	// A target with a build step was just built from what is on disk. A bare
	// binary is as old as its file says, and may predate an edit.
	built := time.Now()
	fresh := store.IsLabel(entry.Target) || entry.Build != ""
	if info, err := os.Stat(binary); err == nil && !fresh {
		built = info.ModTime()
	}
	s.mu.Lock()
	s.builtAt = built
	for _, t := range s.tracked {
		if !t.mod.After(built) { // One still newer than the program stays so.
			t.built, t.to, t.from, t.newer = t.lines, nil, nil, false
		}
	}
	s.mu.Unlock()
}

// Match pairs the lines of two versions of a file: for each line of old, the
// line of new it is now (from 1), or 0 if it was rewritten or removed.
func Match(old, new []string) []int {
	to := make([]int, len(old))
	// What is the same at both ends is most of any edit.
	head := 0
	for head < len(old) && head < len(new) && old[head] == new[head] {
		to[head] = head + 1
		head++
	}
	tail := 0
	for tail < len(old)-head && tail < len(new)-head && old[len(old)-1-tail] == new[len(new)-1-tail] {
		to[len(old)-1-tail] = len(new) - tail
		tail++
	}
	a, b := old[head:len(old)-tail], new[head:len(new)-tail]
	// Between them, the longest run of lines in common, where the two
	// middles are small enough to compare line by line.
	if len(a) == 0 || len(b) == 0 || len(a)*len(b) > 4_000_000 {
		return to
	}
	width := len(b) + 1
	longest := make([]int32, (len(a)+1)*width)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				longest[i*width+j] = longest[(i+1)*width+j+1] + 1
			case longest[(i+1)*width+j] >= longest[i*width+j+1]:
				longest[i*width+j] = longest[(i+1)*width+j]
			default:
				longest[i*width+j] = longest[i*width+j+1]
			}
		}
	}
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			to[head+i] = head + j + 1
			i, j = i+1, j+1
		case longest[(i+1)*width+j] >= longest[i*width+j+1]:
			i++
		default:
			j++
		}
	}
	return to
}

// Follow is where a line went, by Match's pairing, in a file now count lines
// long. A line that was itself rewritten keeps its place between its
// neighbours that were not; one that was removed gives way to the next.
func Follow(to []int, count, line int) int {
	if line < 1 || line > len(to) {
		return line
	}
	if to[line-1] != 0 {
		return to[line-1]
	}
	before, beforeNow := 0, 0
	for i := line - 1; i >= 1; i-- {
		if to[i-1] != 0 {
			before, beforeNow = i, to[i-1]
			break
		}
	}
	afterNow := count + 1
	for i := line + 1; i <= len(to); i++ {
		if to[i-1] != 0 {
			afterNow = to[i-1]
			break
		}
	}
	at := beforeNow + (line - before)
	if at >= afterNow {
		at = afterNow - 1
	}
	if at <= beforeNow {
		at = min(afterNow, count)
	}
	return max(at, 1)
}
