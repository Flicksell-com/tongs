package ui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// One palette for everything on screen (Catppuccin Mocha), and the source
// colours that go with it.
const sourceTheme = "catppuccin-mocha"

var (
	colBase    = lipgloss.Color("#1e1e2e")
	colMantle  = lipgloss.Color("#181825")
	colSurface = lipgloss.Color("#313244")
	colOverlay = lipgloss.Color("#6c7086")
	colSubtext = lipgloss.Color("#a6adc8")
	colText    = lipgloss.Color("#cdd6f4")
	colBlue    = lipgloss.Color("#89b4fa")
	colGreen   = lipgloss.Color("#a6e3a1")
	colYellow  = lipgloss.Color("#f9e2af")
	colRed     = lipgloss.Color("#f38ba8")
	colMauve   = lipgloss.Color("#cba6f7")

	styText    = lipgloss.NewStyle().Foreground(colText)
	styDim     = lipgloss.NewStyle().Foreground(colOverlay)
	stySub     = lipgloss.NewStyle().Foreground(colSubtext)
	styAccent  = lipgloss.NewStyle().Foreground(colBlue)
	styBold    = lipgloss.NewStyle().Foreground(colText).Bold(true)
	styError   = lipgloss.NewStyle().Foreground(colRed)
	styAgent   = lipgloss.NewStyle().Foreground(colYellow)
	styBar     = lipgloss.NewStyle().Background(colSurface).Foreground(colText)
	styTool    = lipgloss.NewStyle().Background(colMantle).Foreground(colText).Padding(0, 1)
	styToolKey = lipgloss.NewStyle().Background(colMantle).Foreground(colOverlay)
	styTitle   = lipgloss.NewStyle().Foreground(colMauve).Bold(true)
	styPC      = lipgloss.NewStyle().Background(lipgloss.Color("#45475a")).Foreground(colText).Bold(true)
	styCursor  = lipgloss.NewStyle().Background(colSurface).Foreground(colText)
	styCell    = lipgloss.NewStyle().Background(colText).Foreground(colBase)
	styMode    = lipgloss.NewStyle().Background(colBlue).Foreground(colBase).Bold(true)
	styModeAlt = lipgloss.NewStyle().Background(colGreen).Foreground(colBase).Bold(true)
	styMatch   = lipgloss.NewStyle().Background(colYellow).Foreground(colBase)
	stySelect  = lipgloss.NewStyle().Background(colSurface).Foreground(colText).Bold(true)
	styHit     = lipgloss.NewStyle().Foreground(colYellow).Bold(true)
	// What the pointer is over, where a click would do something.
	styHover = lipgloss.NewStyle().Background(lipgloss.Color("#45475a")).Foreground(colText)
	// A value that is not what it was at the last stop.
	styChanged = lipgloss.NewStyle().Foreground(colYellow).Bold(true)
	// A value written beside the source line that mentions it.
	styNote = lipgloss.NewStyle().Foreground(colOverlay).Italic(true)
	styTip  = lipgloss.NewStyle().Background(lipgloss.Color("#45475a")).Foreground(colText)
)

// pane is a bordered box with a title in its top edge. Its border is the
// accent colour when it has the keys.
func pane(title, body string, width, height int, focused bool) string {
	label := ""
	if title != "" {
		style := stySub
		if focused {
			style = styAccent.Bold(true)
		}
		label = style.Render(" " + truncate(title, width-6) + " ")
	}
	return framed(label, "", body, width, height, focused)
}

// framed is a bordered box with, written over its top edge, a label at the
// left and another at the right. Both come already coloured, and may hold
// click zones. The right one is left out where there is no room for both.
func framed(label, right, body string, width, height int, focused bool) string {
	edge := colSurface
	if focused {
		edge = colBlue
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(edge).
		Width(width - 2).Height(height - 2).MaxHeight(height).Render(body)
	if (label == "" && right == "") || width < 8 {
		return box
	}
	label = ansi.Truncate(label, width-4, "")
	if lipgloss.Width(label)+lipgloss.Width(right) > width-5 {
		right = ""
	}
	lines := splitLines(box)
	edgeStyle := lipgloss.NewStyle().Foreground(edge)
	rest := width - 4 - lipgloss.Width(label) - lipgloss.Width(right)
	lines[0] = edgeStyle.Render("╭─") + label + edgeStyle.Render(repeat("─", rest)) + right + edgeStyle.Render("─╮")
	return joinLines(lines)
}
