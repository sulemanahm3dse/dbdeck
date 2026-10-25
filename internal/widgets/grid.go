package widgets

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"

	"dbdeck/internal/driver"
	"dbdeck/internal/theme"
)

const (
	sepWidth     = 3 // " │ "
	maxColWidth  = 40
	minColWidth  = 4
	widthSamples = 300
)

type span struct{ x0, x1, col int }

// Grid is a virtualised, horizontally and vertically scrolling table.
// Only the visible rows and columns are rendered, so 100k rows cost the same as 30.
type Grid struct {
	Cols     []driver.Column
	Rows     [][]driver.Cell
	S        Scroller
	Col      int // selected column
	ColOff   int // first visible (non-frozen) column
	Freeze   bool
	SortCol  string
	SortDesc bool

	widths  []int
	sampled int
	w, h    int
	gutter  int
	spans   []span
}

func NewGrid() *Grid { return &Grid{S: NewScroller()} }

// SetSize sets the outer area, header line included.
func (g *Grid) SetSize(w, h int) {
	g.w, g.h = w, h
	g.S.SetHeight(h - 1)
}

// SetData replaces the content and resets the selection.
func (g *Grid) SetData(cols []driver.Column, rows [][]driver.Cell) {
	g.Cols, g.Rows = cols, rows
	g.S.Cursor, g.S.Offset = 0, 0
	g.Col, g.ColOff = 0, 0
	g.S.SetTotal(len(rows))
	g.measure()
}

// Append adds rows (infinite loading) without moving the selection.
func (g *Grid) Append(rows [][]driver.Cell) {
	from := len(g.Rows)
	g.Rows = append(g.Rows, rows...)
	g.S.SetTotal(len(g.Rows))
	g.extendWidths(from)
}

// ReplaceRows swaps the row slice in place, keeping selection (after sort or edits).
func (g *Grid) ReplaceRows(rows [][]driver.Cell) {
	g.Rows = rows
	g.S.SetTotal(len(rows))
	g.measure()
}

func (g *Grid) measure() {
	g.widths = make([]int, len(g.Cols))
	for i, c := range g.Cols {
		g.widths[i] = runewidth.StringWidth(c.Name) + 2 // room for the sort arrow
	}
	g.sampled = 0
	g.extendWidths(0)
}

func (g *Grid) extendWidths(from int) {
	for r := from; r < len(g.Rows) && g.sampled < widthSamples; r++ {
		for i, c := range g.Rows[r] {
			if i >= len(g.widths) {
				break
			}
			if w := runewidth.StringWidth(Clean(c.String())); w > g.widths[i] {
				g.widths[i] = w
			}
		}
		g.sampled++
	}
	for i, w := range g.widths {
		g.widths[i] = min(max(w, minColWidth), maxColWidth)
	}
}

// Current returns the selected row.
func (g *Grid) Current() ([]driver.Cell, bool) {
	if g.S.Cursor < 0 || g.S.Cursor >= len(g.Rows) {
		return nil, false
	}
	return g.Rows[g.S.Cursor], true
}

// CurrentCell returns the selected cell.
func (g *Grid) CurrentCell() (driver.Cell, bool) {
	row, ok := g.Current()
	if !ok || g.Col < 0 || g.Col >= len(row) {
		return driver.Cell{}, false
	}
	return row[g.Col], true
}

func (g *Grid) MoveCol(d int) {
	if len(g.Cols) == 0 {
		return
	}
	g.Col = min(max(g.Col+d, 0), len(g.Cols)-1)
}
func (g *Grid) FirstCol() { g.Col = 0 }
func (g *Grid) LastCol()  { g.Col = max(len(g.Cols)-1, 0) }

type colSpec struct {
	idx, w int
	full   bool
}

func (g *Grid) layout(avail int) []colSpec {
	var out []colSpec
	used := 0
	add := func(i, w int, full bool) {
		if len(out) > 0 {
			used += sepWidth
		}
		out = append(out, colSpec{i, w, full})
		used += w
	}
	n := len(g.widths)
	start := g.ColOff
	if g.Freeze && n > 0 {
		add(0, min(g.widths[0], avail), g.widths[0] <= avail)
		if start < 1 {
			start = 1
		}
	}
	for i := start; i < n; i++ {
		need := g.widths[i]
		if len(out) > 0 {
			need += sepWidth
		}
		if used+need > avail {
			if rem := avail - used - sepWidth; rem >= minColWidth || len(out) == 0 {
				if len(out) == 0 {
					rem = avail
				}
				add(i, rem, false)
			}
			break
		}
		add(i, g.widths[i], true)
	}
	return out
}

func (g *Grid) ensureVisible(avail int) []colSpec {
	if len(g.widths) == 0 {
		return nil
	}
	g.ColOff = min(max(g.ColOff, 0), len(g.widths)-1)
	if g.Col < g.ColOff && !(g.Freeze && g.Col == 0) {
		g.ColOff = g.Col
	}
	for {
		cols := g.layout(avail)
		for _, c := range cols {
			if c.idx == g.Col && (c.full || g.ColOff >= g.Col) {
				return cols
			}
		}
		if g.ColOff >= g.Col || g.ColOff >= len(g.widths)-1 {
			return cols
		}
		g.ColOff++
	}
}

// Render draws the grid into exactly g.w × g.h cells.
func (g *Grid) Render(focused bool) string {
	if g.w <= 0 || g.h <= 0 {
		return ""
	}
	g.gutter = len(strconv.Itoa(max(len(g.Rows), 1))) + 1
	avail := g.w - 1 - g.gutter // scrollbar + row-number gutter
	cols := g.ensureVisible(avail)

	g.spans = g.spans[:0]
	x := g.gutter
	for i, c := range cols {
		if i > 0 {
			x += sepWidth
		}
		g.spans = append(g.spans, span{x, x + c.w, c.idx})
		x += c.w
	}

	sep := theme.Dim.Render(" │ ")
	selSep := theme.RowSel.Render(" │ ")
	lines := make([]string, 0, g.h)

	// header
	var hb strings.Builder
	hb.WriteString(strings.Repeat(" ", g.gutter))
	for i, c := range cols {
		if i > 0 {
			hb.WriteString(sep)
		}
		name := g.Cols[c.idx].Name
		if g.SortCol != "" && name == g.SortCol {
			if g.SortDesc {
				name += " ↓"
			} else {
				name += " ↑"
			}
		}
		hb.WriteString(theme.Header.Render(Fit(name, c.w)))
	}
	lines = append(lines, hb.String())

	// body
	start, end := g.S.Range()
	for r := start; r < end; r++ {
		row := g.Rows[r]
		sel := r == g.S.Cursor
		var b strings.Builder
		num := fmt.Sprintf("%*d ", g.gutter-1, r+1)
		if sel {
			b.WriteString(theme.Title.Render(num))
		} else {
			b.WriteString(theme.Dim.Render(num))
		}
		for i, c := range cols {
			if i > 0 {
				if sel {
					b.WriteString(selSep)
				} else {
					b.WriteString(sep)
				}
			}
			var cell driver.Cell
			if c.idx < len(row) {
				cell = row[c.idx]
			}
			txt := Fit(cell.String(), c.w)
			switch {
			case sel && focused && c.idx == g.Col:
				txt = theme.CellSel.Render(txt)
			case sel && cell.Null:
				txt = theme.RowSel.Inherit(theme.Null).Render(txt)
			case sel:
				txt = theme.RowSel.Render(txt)
			case cell.Null:
				txt = theme.Null.Render(txt)
			}
			b.WriteString(txt)
		}
		lines = append(lines, b.String())
	}

	// scrollbar
	bar := Scrollbar(g.S.Offset, g.S.Total, g.h-1)
	out := make([]string, g.h)
	for i := range out {
		var l string
		if i < len(lines) {
			l = lines[i]
		}
		ch := " "
		if i > 0 && i-1 < len(bar) {
			ch = theme.Dim.Render(bar[i-1])
		}
		out[i] = FitStyled(l, g.w-1) + ch
	}
	return strings.Join(out, "\n")
}

// CellAt maps pane-local coordinates (0,0 = top-left of the grid) to a row and column.
func (g *Grid) CellAt(x, y int) (row, col int, ok bool) {
	if y < 1 {
		return 0, 0, false
	}
	row = g.S.Offset + y - 1
	if row >= len(g.Rows) {
		return 0, 0, false
	}
	for _, s := range g.spans {
		if x >= s.x0 && x < s.x1+1 {
			return row, s.col, true
		}
	}
	return row, g.Col, true
}
