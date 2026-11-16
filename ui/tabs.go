package ui

import (
	"fmt"
	"path/filepath"
)

// tab is a source file kept open: the files the program stopped in or that
// were asked for, most lately shown. The viewer holds the one in front.
type tab struct {
	path           string
	line, col, top int // where it was left, for coming back to
	used           int // goes up each time it is shown; the least is the first dropped
}

const tabsKept = 12

func (m *model) tabOf(path string) int {
	for i, t := range m.tabs {
		if t.path == path {
			return i
		}
	}
	return -1
}

// leave remembers where the cursor is in the file in front.
func (m *model) leave() {
	if at := m.tabOf(m.src.path); at >= 0 {
		t := m.tabs[at]
		t.line, t.col, t.top = m.src.line, m.src.col, m.src.top
	}
}

// show puts a file in the source pane with the cursor on a place in it, as
// the tab in front. Nothing changes if the file can't be read.
func (m *model) show(path string, line, col int) error {
	m.leave()
	if err := m.src.open(path, line, col); err != nil {
		return err
	}
	m.clock++
	if at := m.tabOf(path); at >= 0 {
		m.tabs[at].used = m.clock
	} else {
		m.tabs = append(m.tabs, &tab{path: path, used: m.clock})
		if len(m.tabs) > tabsKept {
			oldest := 0
			for i, t := range m.tabs {
				if t.used < m.tabs[oldest].used {
					oldest = i
				}
			}
			m.tabs = append(m.tabs[:oldest], m.tabs[oldest+1:]...)
		}
	}
	m.s.Watch(path)
	m.layout() // Its line numbers may be wider or narrower than the last file's.
	return nil
}

// front brings a tab to the front, with the cursor where it was left.
func (m *model) front(at int) {
	if at < 0 || at >= len(m.tabs) || m.tabs[at].path == m.src.path {
		return
	}
	t := m.tabs[at]
	if m.show(t.path, t.line, t.col) != nil {
		m.shut(at) // The file is gone.
		return
	}
	m.src.top = t.top
	m.src.clamp()
	m.src.settle()
	m.noSource = ""
}

// shut closes a tab. With the one in front closed, its neighbour comes forward.
func (m *model) shut(at int) {
	if at < 0 || at >= len(m.tabs) {
		return
	}
	inFront := m.tabs[at].path == m.src.path
	m.tabs = append(m.tabs[:at], m.tabs[at+1:]...)
	if !inFront {
		return
	}
	m.src = viewer{height: m.src.height, width: m.src.width}
	if len(m.tabs) == 0 {
		m.inSource = false
		return
	}
	m.front(min(at, len(m.tabs)-1))
}

// turn brings the next tab to the front, or the one before.
func (m *model) turn(by int) {
	if at := m.tabOf(m.src.path); at >= 0 && len(m.tabs) > 1 {
		m.front(((at+by)%len(m.tabs) + len(m.tabs)) % len(m.tabs))
	}
}

// tabNames are the tabs' files as their tabs are labelled: the name alone,
// with the folder where two files share a name.
func (m *model) tabNames() []string {
	count := map[string]int{}
	for _, t := range m.tabs {
		count[filepath.Base(t.path)]++
	}
	names := make([]string, len(m.tabs))
	for i, t := range m.tabs {
		names[i] = filepath.Base(t.path)
		if count[names[i]] > 1 {
			names[i] = filepath.Join(filepath.Base(filepath.Dir(t.path)), names[i])
		}
	}
	return names
}

// tabBar is the tabs as written in the source pane's strip, in room cells:
// the one in front and as many of its neighbours as fit. The one in front is
// the pane's own shade, as if part of it.
func (m *model) tabBar(room int) string {
	names, at := m.tabNames(), m.tabOf(m.src.path)
	if at < 0 {
		return ""
	}
	// A tab is its name with a space each side; the one in front has a
	// cross to close it by as well.
	width := func(i int) int {
		w := len([]rune(names[i])) + 2
		if i == at {
			w += 2
		}
		return w
	}
	first, last, used := at, at, width(at)+4 // Room kept for the marks that say there are more.
	for grew := true; grew; {
		grew = false
		if last+1 < len(names) && used+width(last+1) <= room {
			last++
			used += width(last)
			grew = true
		}
		if first > 0 && used+width(first-1) <= room {
			first--
			used += width(first)
			grew = true
		}
	}
	bar := ""
	if first > 0 {
		bar += styDim.Render("‹ ")
	}
	for i := first; i <= last; i++ {
		if i == at {
			name := styText.Bold(true)
			if m.inSource {
				name = styAccent.Bold(true)
			}
			bar += paint(name.Render(" "+names[i]+" ")+m.spot(fmt.Sprintf("tabx:%d", i), styDim.Render("× ")), colBase)
		} else {
			bar += m.spot(fmt.Sprintf("tab:%d", i), stySub.Render(" "+names[i]+" "))
		}
	}
	if last < len(names)-1 {
		bar += styDim.Render(" ›")
	}
	return bar
}
