package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"
	"github.com/sahilm/fuzzy"

	"github.com/Flicksell-com/tongs/store"
)

// chooser is the targets dialog: every saved target, under the folder of the
// project it belongs to, with the project tongs was started in first.
type chooser struct {
	here  string // the project tongs was started in
	input textinput.Model
	rows  []chooserRow
	at    int // the row the cursor is on, always a target's; -1 with none
}

// chooserRow is a folder's heading, or one target under it.
type chooserRow struct {
	dir   string
	index int // of the target in the saved list; -1 for a heading
	entry store.Entry
}

func newChooser(here string, entries []store.Entry) *chooser {
	input := textinput.New()
	input.Prompt = "❯ "
	input.PromptStyle = styAccent
	input.Placeholder = "type to narrow"
	input.PlaceholderStyle = styDim
	input.Focus()
	c := &chooser{here: here, input: input}
	c.list(entries)
	return c
}

// list fills the rows with the targets the query matches. The cursor goes to
// the first one of this project, where there is one.
func (c *chooser) list(entries []store.Entry) {
	shown := map[int]bool{}
	query := strings.TrimSpace(c.input.Value())
	if query != "" {
		texts := make([]string, len(entries))
		for i, e := range entries {
			texts[i] = e.Name + " " + e.Target
		}
		for _, match := range fuzzy.Find(query, texts) {
			shown[match.Index] = true
		}
	}
	under := map[string][]int{}
	for i, e := range entries {
		if query == "" || shown[i] {
			under[e.Dir] = append(under[e.Dir], i)
		}
	}
	var dirs []string
	for dir := range under {
		if dir != c.here {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	// This project leads, and is listed even with nothing in it yet, so
	// that it is plain where a new target would go.
	if query == "" || len(under[c.here]) > 0 {
		dirs = append([]string{c.here}, dirs...)
	}
	c.rows, c.at = nil, -1
	for _, dir := range dirs {
		c.rows = append(c.rows, chooserRow{dir: dir, index: -1})
		for _, i := range under[dir] {
			if c.at < 0 {
				c.at = len(c.rows)
			}
			c.rows = append(c.rows, chooserRow{dir: dir, index: i, entry: entries[i]})
		}
	}
}

// move steps the cursor to the next target, past the headings.
func (c *chooser) move(step int) {
	for at := c.at + step; at >= 0 && at < len(c.rows); at += step {
		if c.rows[at].index >= 0 {
			c.at = at
			return
		}
	}
}

func (c *chooser) current() (chooserRow, bool) {
	if c.at >= 0 && c.at < len(c.rows) {
		return c.rows[c.at], true
	}
	return chooserRow{}, false
}

// folder is where a new target would go: beside the one the cursor is on.
func (c *chooser) folder() string {
	if row, ok := c.current(); ok {
		return row.dir
	}
	return c.here
}

// The buttons along the foot of the dialog, and the keys that do the same.
var chooserButtons = []struct{ name, key, action string }{
	{"new", "^n", "new"}, {"edit", "^e", "edit"}, {"delete", "^d", "delete"},
}

func (c *chooser) view(width, height int) string {
	boxW := min(width, max(width-8, 40), 96)
	inner := boxW - 4
	room := max(height-2-7, 1) // The rows left for the list in the tallest box.
	first := max(0, min(c.at-room/2, len(c.rows)-room))
	// Keep a target's heading on screen with it where that costs nothing.
	if first > 0 && c.rows[first].index >= 0 && c.rows[first-1].index < 0 && c.at < first+room-1 {
		first--
	}
	column := 0
	for _, row := range c.rows {
		if n := len([]rune(row.entry.Name)); row.index >= 0 && n <= 36 {
			column = max(column, n)
		}
	}
	lines := []string{styTitle.Render("Choose what to debug"), c.input.View(), ""}
	for i := first; i < len(c.rows) && i < first+room; i++ {
		row := c.rows[i]
		if row.index < 0 {
			heading := stySub.Bold(true).Render(store.Tilde(row.dir))
			if row.dir == c.here {
				heading += styDim.Render("   this project")
			}
			lines = append(lines, fit(heading, inner))
			if i+1 == len(c.rows) || c.rows[i+1].index < 0 {
				lines = append(lines, styDim.Render("    no targets here yet"))
			}
			continue
		}
		detail := row.entry.Target
		if row.entry.Name == row.entry.Target {
			detail = ""
		}
		if len(row.entry.Args) > 0 {
			detail = strings.TrimSpace(detail + "   " + shellJoin(row.entry.Args))
		}
		gap := repeat(" ", max(column-len([]rune(row.entry.Name)), 0)) + "   "
		text := "    " + row.entry.Name + gap + styDim.Render(detail)
		if i == c.at {
			text = stySelect.Render(fit("  ❯ "+row.entry.Name+gap+detail, inner))
		}
		lines = append(lines, zone.Mark(fmt.Sprintf("target:%d", i), fit(text, inner)))
	}
	foot := ""
	for _, button := range chooserButtons {
		foot += zone.Mark("target:"+button.action, styTool.Render(button.name)+styToolKey.Render(button.key+" ")) + " "
	}
	lines = append(lines, "", foot+styDim.Render("  enter debugs   esc closes"))
	return pane("", joinLines(lines), boxW, len(lines)+2, true)
}

// over lays a dialog on the middle of the screen, which is dimmed behind it.
// The screen's own colours and click zones go: only the dialog is live.
func over(screen, box string, width, height int) string {
	back := splitLines(screen)
	front := splitLines(box)
	boxW := 0
	for _, line := range front {
		boxW = max(boxW, ansi.StringWidth(line))
	}
	left := max((width-boxW)/2, 0)
	top := max((height-len(front))/3, 0)
	out := make([]string, height)
	for y := range out {
		plain := ""
		if y < len(back) {
			plain = ansi.Strip(back[y])
		}
		plain = fit(plain, width)
		if y < top || y >= top+len(front) {
			out[y] = styDim.Render(plain)
			continue
		}
		out[y] = styDim.Render(ansi.Truncate(plain, left, "")) + front[y-top] +
			styDim.Render(ansi.Cut(plain, left+boxW, width))
	}
	return joinLines(out)
}
