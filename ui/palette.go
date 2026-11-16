package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	zone "github.com/lrstanley/bubblezone"
	"github.com/sahilm/fuzzy"
)

// item is one thing a palette offers.
type item struct {
	label, detail string
	value         any
	hits          []int // the letters of label the query matched
}

// palette is the one way to pick from a list: type to narrow, enter to take.
// It serves the menu, files, functions, breakpoints and versions.
type palette struct {
	kind, title, hint string
	input             textinput.Model
	all, shown        []item
	at                int
	// asks is set for a list that has to be fetched for each query (the
	// program's functions), rather than narrowed from one loaded up front.
	asks bool
	seq  int
}

func newPalette(kind, title, hint string, items []item) *palette {
	input := textinput.New()
	input.Prompt = "❯ "
	input.PromptStyle = styAccent
	input.Focus()
	p := &palette{kind: kind, title: title, hint: hint, input: input, all: items}
	p.narrow()
	return p
}

// narrow fills shown with the items the query matches, best first.
func (p *palette) narrow() {
	query := strings.TrimSpace(p.input.Value())
	p.at = 0
	if query == "" || p.asks {
		p.shown = p.all
		return
	}
	labels := make([]string, len(p.all))
	for i, it := range p.all {
		labels[i] = it.label
	}
	p.shown = p.shown[:0:0]
	for _, match := range fuzzy.Find(query, labels) {
		it := p.all[match.Index]
		it.hits = match.MatchedIndexes
		p.shown = append(p.shown, it)
	}
}

func (p *palette) move(step int) {
	if len(p.shown) > 0 {
		p.at = ((p.at+step)%len(p.shown) + len(p.shown)) % len(p.shown)
	}
}

func (p *palette) current() (item, bool) {
	if p.at < len(p.shown) {
		return p.shown[p.at], true
	}
	return item{}, false
}

// view is a box over the middle of the screen, the same size whatever is
// typed, so that it does not jump about as the list narrows.
func (p *palette) view(width, height int) string {
	boxW := min(width, max(width-8, 40), 96)
	boxH := max(min(height-2, 26), 6)
	inner := boxW - 4
	lines := []string{styTitle.Render(p.title), p.input.View(), ""}
	room := max(boxH-2-len(lines)-2, 1)
	first := max(0, min(p.at-room/2, len(p.shown)-room))
	// The details line up in a column, where the labels are short enough.
	column := 0
	for _, it := range p.shown {
		if n := len([]rune(it.label)); n <= 44 {
			column = max(column, n)
		}
	}
	for i := first; i < len(p.shown) && i < first+room; i++ {
		it := p.shown[i]
		hit := map[int]bool{}
		for _, at := range it.hits {
			hit[at] = true
		}
		var label strings.Builder
		for at, r := range it.label {
			if hit[at] {
				label.WriteString(styHit.Render(string(r)))
			} else {
				label.WriteString(string(r))
			}
		}
		gap := repeat(" ", column-len([]rune(it.label))) + "   "
		row := "  " + label.String()
		if it.detail != "" {
			row += gap + styDim.Render(it.detail)
		}
		if i == p.at {
			row = stySelect.Render(fit("❯ "+it.label+gap+it.detail, inner))
		}
		lines = append(lines, zone.Mark(fmt.Sprintf("pal:%d", i), fit(row, inner)))
	}
	if len(p.shown) == 0 {
		lines = append(lines, styDim.Render("  nothing matches"))
	}
	for len(lines) < boxH-3 {
		lines = append(lines, "")
	}
	lines = append(lines, styDim.Render(truncate(p.hint, inner)))
	return pane("", joinLines(lines), boxW, boxH, true)
}
