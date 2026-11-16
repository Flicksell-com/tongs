package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func splitLines(s string) []string { return strings.Split(s, "\n") }
func joinLines(l []string) string  { return strings.Join(l, "\n") }
func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}

// truncate cuts s to width cells, colour codes intact, with an ellipsis.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// fit makes s exactly width cells wide: cut or padded.
func fit(s string, width int) string {
	s = ansi.Truncate(s, width, "")
	return s + repeat(" ", width-ansi.StringWidth(s))
}

// block makes lines exactly height rows of width cells.
func block(lines []string, width, height int) string {
	out := make([]string, height)
	for i := range out {
		if i < len(lines) {
			out[i] = fit(lines[i], width)
		} else {
			out[i] = repeat(" ", width)
		}
	}
	return joinLines(out)
}

// shellSplit splits a line the way a shell would: on spaces, with quotes
// keeping a word together. ok is false for an unclosed quote.
func shellSplit(s string) (words []string, ok bool) {
	var word strings.Builder
	quote, in := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				words = append(words, word.String())
				word.Reset()
				in = false
			}
		default:
			word.WriteRune(r)
			in = true
		}
	}
	if in {
		words = append(words, word.String())
	}
	return words, quote == 0
}

// shellJoin writes words so that shellSplit gives them back.
func shellJoin(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		if w == "" || strings.ContainsAny(w, " \t'\"") {
			w = "'" + strings.ReplaceAll(w, "'", `'"'"'`) + "'"
		}
		quoted[i] = w
	}
	return strings.Join(quoted, " ")
}
