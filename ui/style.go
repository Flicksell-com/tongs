package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// theme is one set of colours for everything on screen, and the name of the
// source colours (one of chroma's styles) that go with it.
type theme struct {
	name, source string
	// The shades panes are painted in, darkest to lightest on a dark theme:
	// crust behind everything, mantle and base for panes, surface for their
	// headings and what is selected, raised for what is picked out further.
	crust, mantle, base, surface, raised string
	overlay, subtext, text               string // from quiet to plain
	blue, green, yellow, red             string
	mauve, peach, teal                   string
}

var themes = []theme{
	{"Catppuccin Mocha", "catppuccin-mocha", "#11111b", "#181825", "#1e1e2e", "#313244", "#45475a",
		"#6c7086", "#a6adc8", "#cdd6f4", "#89b4fa", "#a6e3a1", "#f9e2af", "#f38ba8", "#cba6f7", "#fab387", "#94e2d5"},
	{"Catppuccin Latte", "catppuccin-latte", "#dce0e8", "#e6e9ef", "#eff1f5", "#ccd0da", "#bcc0cc",
		"#8c8fa1", "#5c5f77", "#4c4f69", "#1e66f5", "#40a02b", "#df8e1d", "#d20f39", "#8839ef", "#fe640b", "#179299"},
	{"Tokyo Night", "tokyonight-night", "#13141c", "#16161e", "#1a1b26", "#292e42", "#3b4261",
		"#565f89", "#a9b1d6", "#c0caf5", "#7aa2f7", "#9ece6a", "#e0af68", "#f7768e", "#bb9af7", "#ff9e64", "#73daca"},
	{"Gruvbox", "gruvbox", "#17191a", "#1d2021", "#282828", "#3c3836", "#504945",
		"#7c6f64", "#bdae93", "#ebdbb2", "#83a598", "#b8bb26", "#fabd2f", "#fb4934", "#d3869b", "#fe8019", "#8ec07c"},
	{"Nord", "nord", "#20242c", "#272c36", "#2e3440", "#3b4252", "#4c566a",
		"#69748c", "#c2cad8", "#eceff4", "#88c0d0", "#a3be8c", "#ebcb8b", "#bf616a", "#b48ead", "#d08770", "#8fbcbb"},
	{"Rosé Pine", "rose-pine", "#131120", "#191724", "#1f1d2e", "#26233a", "#403d52",
		"#6e6a86", "#908caa", "#e0def4", "#9ccfd8", "#31748f", "#f6c177", "#eb6f92", "#c4a7e7", "#ebbcba", "#9ccfd8"},
}

// The theme in use: its colours, and the styles made of them. use sets them.
var (
	sourceTheme string

	colCrust, colMantle, colBase, colSurface, colRaised lipgloss.Color
	colOverlay, colSubtext, colText                     lipgloss.Color
	colBlue, colGreen, colYellow, colRed                lipgloss.Color
	colMauve, colPeach, colTeal                         lipgloss.Color

	styText, styDim, stySub, styAccent, styError, styAgent lipgloss.Style
	styTitle, styPC, styCursor, styCell, styMode           lipgloss.Style
	styModeAlt, styMatch, stySelect, styHit                lipgloss.Style
	// What the pointer is over, where a click would do something.
	styHover lipgloss.Style
	// A value that is not what it was at the last stop.
	styChanged lipgloss.Style
	// A value written beside the source line that mentions it.
	styNote lipgloss.Style
	styTip  lipgloss.Style
	// A button: in the status line, at the foot of a dialog.
	styTool, styToolKey lipgloss.Style

	// The escape codes that set each background, for paint.
	grounds = map[lipgloss.Color]string{}
)

func init() { themes[0].use() }

// use makes a theme the one everything is drawn in.
func (t theme) use() {
	sourceTheme = t.source
	colCrust, colMantle, colBase = lipgloss.Color(t.crust), lipgloss.Color(t.mantle), lipgloss.Color(t.base)
	colSurface, colRaised = lipgloss.Color(t.surface), lipgloss.Color(t.raised)
	colOverlay, colSubtext, colText = lipgloss.Color(t.overlay), lipgloss.Color(t.subtext), lipgloss.Color(t.text)
	colBlue, colGreen, colYellow, colRed = lipgloss.Color(t.blue), lipgloss.Color(t.green), lipgloss.Color(t.yellow), lipgloss.Color(t.red)
	colMauve, colPeach, colTeal = lipgloss.Color(t.mauve), lipgloss.Color(t.peach), lipgloss.Color(t.teal)
	grounds = map[lipgloss.Color]string{}

	fg := func(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	on := func(bg, c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Background(bg).Foreground(c) }
	styText, styDim, stySub = fg(colText), fg(colOverlay), fg(colSubtext)
	styAccent, styError, styAgent = fg(colBlue), fg(colRed), fg(colYellow)
	styTitle = fg(colMauve).Bold(true)
	styPC = on(colRaised, colText).Bold(true)
	styCursor = on(colSurface, colText)
	styCell = on(colText, colBase)
	styMode = on(colBlue, colBase).Bold(true)
	styModeAlt = on(colGreen, colBase).Bold(true)
	styMatch = on(colYellow, colBase)
	stySelect = on(colSurface, colText).Bold(true)
	styHit = fg(colYellow).Bold(true)
	styHover = on(colRaised, colText)
	styChanged = fg(colYellow).Bold(true)
	styNote = fg(colOverlay).Italic(true)
	styTip = on(colRaised, colText)
	styTool = on(colSurface, colText)
	styToolKey = on(colSurface, colOverlay)
}

const reset = "\x1b[0m"

// paint puts a line on a background. The line's own colours end in resets,
// which would let the terminal's background through, so the background is
// set again after each. A part painted already keeps its own.
func paint(line string, bg lipgloss.Color) string {
	ground, known := grounds[bg]
	if !known {
		// What lipgloss writes ahead of text on this background; nothing,
		// where colours are off.
		ground, _, _ = strings.Cut(lipgloss.NewStyle().Background(bg).Render("\x00"), "\x00")
		grounds[bg] = ground
	}
	if ground == "" {
		return line
	}
	return ground + strings.ReplaceAll(line, reset, reset+ground) + reset
}

// mix is the colour a part of the way from one to another.
func mix(from, to lipgloss.Color, part float64) lipgloss.Color {
	var a, b [3]int
	fmt.Sscanf(string(from), "#%02x%02x%02x", &a[0], &a[1], &a[2])
	fmt.Sscanf(string(to), "#%02x%02x%02x", &b[0], &b[1], &b[2])
	part = min(max(part, 0), 1)
	for i := range a {
		a[i] += int(float64(b[i]-a[i]) * part)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", a[0], a[1], a[2]))
}

// scroll is where a pane's lines are in all there are of them: how many,
// and the first shown. The zero value is one with nothing to scroll.
type scroll struct{ total, top int }

// thumb is the cell of the scroll bar on a row of a pane rows high.
func (s scroll) thumb(row, rows int) string {
	if s.total <= rows || rows < 2 {
		return " "
	}
	size := max(rows*rows/s.total, 1)
	at := s.top * (rows - size) / (s.total - rows)
	if row < at || row >= at+size {
		return " "
	}
	return lipgloss.NewStyle().Foreground(colRaised).Render("▐")
}

// heading is a pane's name as it is written in its strip.
func heading(text string, focused bool) string {
	if focused {
		return styAccent.Bold(true).Render(text)
	}
	return stySub.Render(text)
}

// panel is a pane: a strip with its name at the left and a remark at the
// right, over its lines, on a shade of its own. It has no border; the strip
// and the shade set it apart. Both labels come coloured, and may hold click
// zones. The last column is the scroll bar. It returns the strip and the
// body apart, for the body to be marked as the place clicks land.
func panel(head, right string, lines []string, width, height int, bg lipgloss.Color, focused, lit bool, bar scroll) (strip, body string) {
	lead := " "
	if focused {
		lead = styAccent.Render("▎")
	}
	stripBg := colSurface
	if lit {
		stripBg = colRaised // The pointer is on it, and it can be dragged.
	}
	if lipgloss.Width(head)+lipgloss.Width(right)+2 > width {
		right = ""
	}
	strip = paint(fit(lead+head, width-lipgloss.Width(right))+right, stripBg)
	rows := max(height-1, 0)
	out := make([]string, rows)
	for i := range out {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out[i] = paint(fit(line, width-1)+bar.thumb(i, rows), bg)
	}
	return strip, joinLines(out)
}

// stack puts a pane's strip over its body.
func stack(strip, body string) string {
	if body == "" {
		return strip
	}
	return strip + "\n" + body
}

// divider is the line between two panes side by side: a column height rows
// tall, the top of it part of their strips. It is lit while it is dragged or
// the pointer is on it.
func divider(height int, lit bool) string {
	rows := make([]string, max(height, 0))
	for i := range rows {
		switch {
		case i == 0:
			rows[i] = paint(" ", colSurface)
		case lit:
			rows[i] = paint(styAccent.Render("┃"), colCrust)
		default:
			rows[i] = paint(" ", colCrust) // A gap, darker than either pane.
		}
	}
	return joinLines(rows)
}

// pane is a bordered box with a title in its top edge: a dialog, floating
// over the screen.
func pane(title, body string, width, height int, focused bool) string {
	label := ""
	if title != "" {
		label = heading(" "+truncate(title, width-6)+" ", focused)
	}
	// A cell of margin inside the border.
	return framed(label, "", " "+strings.ReplaceAll(body, "\n", "\n "), width, height, focused)
}

// framed is a bordered box with, written over its top edge, a label at the
// left and another at the right. Both come already coloured, and may hold
// click zones. The right one is left out where there is no room for both.
func framed(label, right, body string, width, height int, focused bool) string {
	edge := colRaised
	if focused {
		edge = colBlue
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(edge).
		Width(width - 2).Height(height - 2).MaxHeight(height).Render(body)
	lines := splitLines(box)
	if (label != "" || right != "") && width >= 8 {
		label = ansi.Truncate(label, width-4, "")
		if lipgloss.Width(label)+lipgloss.Width(right) > width-5 {
			right = ""
		}
		edgeStyle := lipgloss.NewStyle().Foreground(edge)
		rest := width - 4 - lipgloss.Width(label) - lipgloss.Width(right)
		lines[0] = edgeStyle.Render("╭─") + label + edgeStyle.Render(repeat("─", rest)) + right + edgeStyle.Render("─╮")
	}
	for i, line := range lines {
		lines[i] = paint(line, colMantle)
	}
	return joinLines(lines)
}
