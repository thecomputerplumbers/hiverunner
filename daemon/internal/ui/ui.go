// Package ui draws what the daemon's commands print for a person: boxes,
// bars and the comb. Colour is used only on a terminal that wants it.
package ui

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Inner is the width of a box's contents, chosen so a box fits 80 columns.
const Inner = 70

// Screen knows whether what it prints may carry colour.
type Screen struct {
	color bool
}

// For returns a screen for a file: coloured when it is a terminal and the
// person has not asked for none with NO_COLOR.
func For(file *os.File) Screen {
	info, err := file.Stat()
	terminal := err == nil && info.Mode()&os.ModeCharDevice != 0
	return Screen{color: terminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"}
}

func (s Screen) paint(code, text string) string {
	if !s.color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Honey is the one colour: what is yours, and what is on.
func (s Screen) Honey(text string) string { return s.paint("38;5;214", text) }
func (s Screen) Dim(text string) string   { return s.paint("2", text) }
func (s Screen) Bold(text string) string  { return s.paint("1", text) }

var escape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Width is how many columns text takes once its colour codes are gone.
func Width(text string) int {
	return utf8.RuneCountInString(escape.ReplaceAllString(text, ""))
}

// Pad fills text out to a width with spaces. Text already wider is left.
func Pad(text string, width int) string {
	if gap := width - Width(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// Box frames rows under a title.
func (s Screen) Box(title string, rows ...string) string {
	var out strings.Builder
	rule := Inner + 2 - Width(title) - 3
	out.WriteString(s.Dim("╭─ ") + s.Bold(title) + s.Dim(" "+strings.Repeat("─", rule)+"╮") + "\n")
	for _, row := range rows {
		out.WriteString(s.Dim("│") + " " + Pad(row, Inner) + " " + s.Dim("│") + "\n")
	}
	out.WriteString(s.Dim("╰"+strings.Repeat("─", Inner+2)+"╯") + "\n")
	return out.String()
}

// Bar draws part of a whole, width columns wide. Any part at all shows at
// least one filled column.
func (s Screen) Bar(part, whole, width int) string {
	filled := 0
	if whole > 0 && part > 0 {
		filled = min(width, max(1, part*width/whole))
	}
	return s.Honey(strings.Repeat("█", filled)) + s.Dim(strings.Repeat("░", width-filled))
}

// Dot marks a state: filled and honey when on, hollow when not.
func (s Screen) Dot(on bool) string {
	if on {
		return s.Honey("●")
	}
	return s.Dim("○")
}

var comb = []string{
	`    __    __`,
	` __/  \__/  \__`,
	`/  \__/  \__/  \`,
	`\__/  \__/  \__/`,
	`   \__/  \__/`,
}

// Masthead is the comb with a few lines of text set beside it.
func (s Screen) Masthead(beside ...string) string {
	var out strings.Builder
	for i, line := range comb {
		out.WriteString(s.Honey(Pad(line, 20)))
		if i < len(beside) {
			out.WriteString(beside[i])
		}
		out.WriteString("\n")
	}
	return out.String()
}
