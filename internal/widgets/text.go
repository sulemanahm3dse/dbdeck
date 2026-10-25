// Package widgets contains the reusable, individually testable UI pieces.
package widgets

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"dbdeck/internal/theme"
)

// Clean makes a value safe for a single terminal cell line.
func Clean(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ", "\x1b", "␛").Replace(s)
}

// Fit truncates or pads plain text to exactly w display cells.
func Fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = runewidth.Truncate(Clean(s), w, "…")
	return runewidth.FillRight(s, w)
}

// FitStyled forces an already-styled string to exactly w cells.
func FitStyled(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// FitLines returns exactly h lines, each exactly w cells wide.
func FitLines(body string, w, h int) []string {
	lines := strings.Split(body, "\n")
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out[i] = FitStyled(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", max(w, 0))
		}
	}
	return out
}

// Pane draws a rounded box of OUTER size w×h with a title in the top border.
func Pane(title, body string, w, h int, focused bool) string {
	if w < 4 || h < 3 {
		return ""
	}
	border := theme.BorderOff
	if focused {
		border = theme.BorderOn
	}
	iw, ih := w-2, h-2
	t := " " + runewidth.Truncate(title, max(iw-4, 1), "…") + " "
	tw := runewidth.StringWidth(t)
	fill := w - 3 - tw
	if fill < 0 {
		fill = 0
	}
	var b strings.Builder
	b.WriteString(border.Render("╭─") + theme.Title.Render(t) + border.Render(strings.Repeat("─", fill)+"╮") + "\n")
	side := border.Render("│")
	for _, l := range FitLines(body, iw, ih) {
		b.WriteString(side + l + side + "\n")
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", w-2) + "╯"))
	return b.String()
}

// Center places a box in the middle of a w×h area.
func Center(box string, w, h int) string {
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// Spinner frames for loading indicators.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
