package ui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flicksell-com/tongs/session"
)

// viewer shows one source file with a cursor that moves the way vim's does.
// It is for reading and for placing breakpoints; nothing in it edits.
type viewer struct {
	path   string
	plain  [][]rune // the file's lines
	shaded []string // the same, coloured
	line   int      // cursor line, 1-based
	col    int      // cursor column, 0-based
	want   int      // the column j and k try to keep
	top    int      // first line shown
	left   int      // first column shown
	needle string   // what was last searched for
	back   bool     // the last search went backward
	height int
	width  int // of the code, without the gutter
}

// read loads a file's text into the viewer.
func (v *viewer) read(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(strings.TrimRight(string(raw), "\n"), "\t", "    ")
	lines := strings.Split(text, "\n")
	v.path = path
	v.plain = make([][]rune, len(lines))
	for i, l := range lines {
		v.plain[i] = []rune(l)
	}
	v.shaded = highlight(path, text, lines)
	return nil
}

// reload reads the file again after it was edited, leaving the cursor on the
// line of code it was on, and that line where it was on the screen.
func (v *viewer) reload() error {
	if v.path == "" {
		return nil
	}
	old := make([]string, len(v.plain))
	for i, l := range v.plain {
		old[i] = string(l)
	}
	if err := v.read(v.path); err != nil {
		return err
	}
	now := make([]string, len(v.plain))
	for i, l := range v.plain {
		now[i] = string(l)
	}
	line := session.Follow(session.Match(old, now), len(now), v.line)
	v.top += line - v.line
	v.line = line
	v.settle()
	return nil
}

func (v *viewer) open(path string, line, col int) error {
	if path != v.path {
		if err := v.read(path); err != nil {
			return err
		}
	}
	v.line = min(max(line, 1), len(v.plain))
	v.col = col
	v.settle()
	v.top = v.line - v.height/2
	v.clamp()
	return nil
}

// highlight colours text line by line; plain lines where it can't.
func highlight(path, text string, plain []string) []string {
	lexer := lexers.Match(path)
	if lexer == nil {
		return plain
	}
	tokens, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return plain
	}
	style := styles.Get(sourceTheme)
	var out []string
	for _, line := range chroma.SplitTokensIntoLines(tokens.Tokens()) {
		var buf bytes.Buffer
		if formatters.TTY16m.Format(&buf, style, chroma.Literator(line...)) != nil {
			return plain
		}
		out = append(out, strings.TrimRight(buf.String(), "\n"))
	}
	if len(out) != len(plain) {
		return plain
	}
	return out
}

func (v *viewer) text() []rune { return v.plain[v.line-1] }

// gutter is how many cells stand before the code: the marks, the line number.
func (v *viewer) gutter() int { return 3 + len(fmt.Sprint(len(v.plain))) }

func (v *viewer) clamp() {
	v.top = max(1, min(v.top, len(v.plain)-v.height+1))
}

// settle keeps the cursor on the text and the text around it on screen.
func (v *viewer) settle() {
	if v.path == "" {
		return
	}
	v.line = min(max(v.line, 1), len(v.plain))
	v.col = max(0, min(v.col, len(v.text())-1))
	if v.line < v.top {
		v.top = v.line
	} else if v.line >= v.top+v.height {
		v.top = v.line - v.height + 1
	}
	v.clamp()
	if v.col < v.left {
		v.left = v.col
	} else if v.width > 0 && v.col >= v.left+v.width {
		v.left = v.col - v.width + 1
	}
}

func (v *viewer) resize(width, height int) {
	v.width, v.height = max(width, 1), max(height, 1)
	if v.line < v.top || v.line >= v.top+v.height {
		v.top = v.line - v.height/2
	}
	v.settle()
}

// to moves the cursor to a line, keeping the column j and k travel in.
func (v *viewer) to(line int) {
	v.line = line
	v.col = v.want
	v.settle()
}

// at moves the cursor to a line and column.
func (v *viewer) at(line, col int) {
	v.line, v.col = line, col
	v.settle()
	v.want = v.col
}

func (v *viewer) centre() {
	v.top = v.line - v.height/2
	v.clamp()
}

func (v *viewer) scroll(rows int) {
	v.top += rows
	v.clamp()
}

func wordy(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// class sorts a rune as blank, part of a word, or punctuation, for w b e.
func class(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case wordy(r):
		return 1
	}
	return 2
}

// step moves one rune forward or back through the file, crossing line ends.
// It reports false at either end of the file.
func (v *viewer) step(line, col, by int) (int, int, bool) {
	col += by
	for col < 0 || col >= len(v.plain[line-1]) {
		if by > 0 {
			if line == len(v.plain) {
				return line, max(len(v.plain[line-1])-1, 0), false
			}
			line, col = line+1, 0
			if len(v.plain[line-1]) == 0 {
				return line, 0, true // An empty line is a stop of its own.
			}
		} else {
			if line == 1 {
				return 1, 0, false
			}
			line--
			col = len(v.plain[line-1]) - 1
			if col < 0 {
				return line, 0, true
			}
		}
	}
	return line, col, true
}

func (v *viewer) classAt(line, col int) int {
	if col >= len(v.plain[line-1]) {
		return 0
	}
	return class(v.plain[line-1][col])
}

// word moves by words: "w" to the next start, "b" to the previous start,
// "e" to the next end.
func (v *viewer) word(kind string) {
	line, col := v.line, v.col
	ok := true
	switch kind {
	case "w":
		for start := v.classAt(line, col); ok && start != 0 && v.classAt(line, col) == start; {
			before := line
			line, col, ok = v.step(line, col, 1)
			if line != before {
				break
			}
		}
		for ok && v.classAt(line, col) == 0 && len(v.plain[line-1]) > 0 {
			line, col, ok = v.step(line, col, 1)
		}
	case "b":
		line, col, ok = v.step(line, col, -1)
		for ok && v.classAt(line, col) == 0 && len(v.plain[line-1]) > 0 {
			line, col, ok = v.step(line, col, -1)
		}
		for kind := v.classAt(line, col); col > 0 && v.classAt(line, col-1) == kind; {
			col--
		}
	case "e":
		line, col, ok = v.step(line, col, 1)
		for ok && v.classAt(line, col) == 0 {
			line, col, ok = v.step(line, col, 1)
		}
		for kind := v.classAt(line, col); col+1 < len(v.plain[line-1]) && v.classAt(line, col+1) == kind; {
			col++
		}
	}
	v.at(line, col)
}

// under is the word the cursor is on, and the column it starts at.
func (v *viewer) under() (string, int) {
	text := v.text()
	if v.col >= len(text) || !wordy(text[v.col]) {
		return "", v.col
	}
	start, end := v.col, v.col
	for start > 0 && wordy(text[start-1]) {
		start--
	}
	for end+1 < len(text) && wordy(text[end+1]) {
		end++
	}
	return string(text[start : end+1]), start
}

// firstText is the column of the first character on the line that isn't blank.
func (v *viewer) firstText() int {
	for i, r := range v.text() {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return 0
}

// pair moves to the bracket matching the one under the cursor.
func (v *viewer) pair() {
	const open, shut = "([{", ")]}"
	text := v.text()
	if v.col >= len(text) {
		return
	}
	here := text[v.col]
	by, other := 1, rune(0)
	if i := strings.IndexRune(open, here); i >= 0 {
		other = rune(shut[i])
	} else if i := strings.IndexRune(shut, here); i >= 0 {
		by, other = -1, rune(open[i])
	} else {
		return
	}
	depth, line, col, ok := 1, v.line, v.col, true
	for depth > 0 {
		if line, col, ok = v.step(line, col, by); !ok {
			return
		}
		if col < len(v.plain[line-1]) {
			switch v.plain[line-1][col] {
			case here:
				depth++
			case other:
				depth--
			}
		}
	}
	v.at(line, col)
}

// fold is text as it is compared when searching: lower case finds either
// case; a capital in the needle asks for that exact case.
func (v *viewer) fold(text string) string {
	if v.needle == strings.ToLower(v.needle) {
		return strings.ToLower(text)
	}
	return text
}

func (v *viewer) has(line int) bool {
	return v.needle != "" && strings.Contains(v.fold(string(v.plain[line-1])), v.needle)
}

// matches is where the needle is on a line, as rune columns.
func (v *viewer) matches(line int) []int {
	text := v.fold(string(v.plain[line-1]))
	var found []int
	for from := 0; ; {
		at := strings.Index(text[from:], v.needle)
		if at < 0 || v.needle == "" {
			return found
		}
		found = append(found, len([]rune(text[:from+at])))
		from += at + max(len(v.needle), 1)
	}
}

// find moves to the next match from (line, col), forward or back, wrapping
// round the file. here lets a match starting right at the position count.
// It reports whether there is one, and whether finding it wrapped.
func (v *viewer) find(line, col int, back, here bool) (found, wrapped bool) {
	if v.path == "" || v.needle == "" {
		return false, false
	}
	count := len(v.plain)
	for offset := 0; offset <= count; offset++ {
		at := line + offset
		if back {
			at = line - offset
		}
		wrap := at < 1 || at > count
		at = ((at-1)%count+count)%count + 1
		cols := v.matches(at)
		if back {
			for i := len(cols) - 1; i >= 0; i-- {
				if offset > 0 || cols[i] < col || (here && cols[i] == col) {
					v.at(at, cols[i])
					return true, wrap || offset == count
				}
			}
		} else {
			for _, c := range cols {
				if offset > 0 || c > col || (here && c == col) {
					v.at(at, c)
					return true, wrap || offset == count
				}
			}
		}
	}
	return false, false
}

// render draws the rows. pc is the line the program is stopped on in this
// file, or 0; marks are the breakpoints' marks by line, and notes what is
// written after the code of a line: the values of the variables it mentions.
func (v *viewer) render(pc int, marks, notes map[int]string, focused bool) []string {
	rows := make([]string, 0, v.height)
	digits := len(fmt.Sprint(len(v.plain)))
	for line := v.top; line < v.top+v.height && line <= len(v.plain); line++ {
		mark := " "
		if marks[line] != "" {
			mark = marks[line]
		}
		arrow := " "
		if line == pc {
			arrow = styHit.Render("❱")
		}
		number := fmt.Sprintf("%*d ", digits, line)
		numberStyle := styDim
		note := styNote
		var code string
		switch {
		case line == v.line && focused:
			numberStyle = styAccent
			code = v.cursorRow(line == pc)
			note = note.Background(colSurface)
			if line == pc {
				note = styNote.Background(styPC.GetBackground())
			}
		case line == pc:
			numberStyle = styHit
			code = styPC.Render(fit(v.cut(string(v.plain[line-1])), v.width))
			note = note.Background(styPC.GetBackground())
		case v.has(line):
			code = fit(v.cut(v.marked(line)), v.width)
		default:
			code = fit(v.cut(v.shaded[line-1]), v.width)
		}
		// The note goes after the code, in the room the line leaves.
		used := min(max(len(v.plain[line-1])-v.left, 0), v.width)
		if line == v.line && focused {
			used = max(used, min(v.col-v.left+1, v.width))
		}
		if room := v.width - used - 3; notes[line] != "" && room >= 8 {
			text := "   " + truncate(notes[line], room)
			code = ansi.Truncate(code, used, "") + note.Render(text+repeat(" ", v.width-used-ansi.StringWidth(text)))
		}
		rows = append(rows, mark+arrow+numberStyle.Render(number)+code)
	}
	return rows
}

// chain is the expression the cell at a line and column is the end of: the
// name there, with what it is a member of. It is "" off the code, on a number
// or on nothing that names a value.
func (v *viewer) chain(line, col int) string {
	if line < 1 || line > len(v.plain) || col < 0 || col >= len(v.plain[line-1]) {
		return ""
	}
	text := v.plain[line-1]
	if !wordy(text[col]) {
		return ""
	}
	end := col
	for end+1 < len(text) && wordy(text[end+1]) {
		end++
	}
	before := string(text[:end+1])
	found := session.Chain(before, len(before))
	if found == "" || unicode.IsDigit([]rune(found)[0]) {
		return ""
	}
	return found
}

// cut is the part of a line that is on screen sideways.
func (v *viewer) cut(s string) string {
	if v.left == 0 {
		return s
	}
	return ansi.Cut(s, v.left, v.left+v.width)
}

// cursorRow is the cursor's line, with the cursor's cell picked out.
func (v *viewer) cursorRow(pc bool) string {
	row := styCursor
	if pc {
		row = styPC
	}
	text := v.text()
	end := min(v.left+v.width, len(text))
	if v.left >= len(text) {
		return row.Render(styCell.Render(" ") + repeat(" ", v.width-1))
	}
	var out strings.Builder
	shown := text[v.left:end]
	at := v.col - v.left
	for i, r := range shown {
		if i == at {
			out.WriteString(styCell.Render(string(r)))
		} else {
			out.WriteString(row.Render(string(r)))
		}
	}
	pad := v.width - len(shown)
	if len(text) == 0 || at >= len(shown) {
		out.WriteString(styCell.Render(" "))
		pad--
	}
	return out.String() + row.Render(repeat(" ", pad))
}

// marked is a line with each match of the needle picked out.
func (v *viewer) marked(line int) string {
	text := v.plain[line-1]
	width := len([]rune(v.needle))
	var out strings.Builder
	from := 0
	for _, at := range v.matches(line) {
		out.WriteString(styText.Render(string(text[from:at])) + styMatch.Render(string(text[at:at+width])))
		from = at + width
	}
	out.WriteString(styText.Render(string(text[from:])))
	return out.String()
}
