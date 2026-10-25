package widgets

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"dbdeck/internal/driver"
)

func sample(rows, cols int) *Grid {
	g := NewGrid()
	var cs []driver.Column
	for c := 0; c < cols; c++ {
		cs = append(cs, driver.Column{Name: fmt.Sprintf("col%d", c)})
	}
	var rs [][]driver.Cell
	for r := 0; r < rows; r++ {
		row := make([]driver.Cell, cols)
		for c := range row {
			row[c] = driver.Text(fmt.Sprintf("r%d-c%d-value", r, c))
		}
		rs = append(rs, row)
	}
	g.SetSize(50, 12)
	g.SetData(cs, rs)
	return g
}

func checkExact(t *testing.T, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("want %d lines, got %d", h, len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("line %d is %d wide, want %d: %q", i, got, w, ansi.Strip(l))
		}
	}
}

func TestRenderIsExactlySized(t *testing.T) {
	g := sample(1000, 12)
	checkExact(t, g.Render(true), 50, 12)
	for i := 0; i < 40; i++ {
		g.S.Move(7)
		g.MoveCol(1)
		checkExact(t, g.Render(true), 50, 12)
	}
	g.SetSize(9, 4) // tiny
	checkExact(t, g.Render(true), 9, 4)
}

func TestSelectedColumnAlwaysVisible(t *testing.T) {
	g := sample(20, 12)
	for c := 0; c < 12; c++ {
		g.Col = c
		out := ansi.Strip(g.Render(true))
		if !strings.Contains(out, fmt.Sprintf("col%d", c)) && c != 0 {
			t.Fatalf("column %d not visible after scrolling:\n%s", c, out)
		}
	}
	g.Col = 0
	out := ansi.Strip(g.Render(true))
	if !strings.Contains(out, "col0") {
		t.Fatalf("scrolling back left failed:\n%s", out)
	}
}

func TestFrozenFirstColumn(t *testing.T) {
	g := sample(5, 12)
	g.Freeze = true
	g.Col = 9
	first := strings.Split(ansi.Strip(g.Render(true)), "\n")[0]
	if !strings.Contains(first, "col0") || !strings.Contains(first, "col9") {
		t.Fatalf("frozen col0 and selected col9 should both show: %q", first)
	}
}

func TestVirtualisedScrolling(t *testing.T) {
	g := sample(100000, 3)
	g.S.Bottom()
	out := ansi.Strip(g.Render(true))
	if !strings.Contains(out, "r99999-c0") || strings.Contains(out, "r0-c0") {
		t.Fatalf("bottom view wrong:\n%s", out)
	}
}

func TestNullWideRunesAndSort(t *testing.T) {
	g := NewGrid()
	g.SetSize(40, 5)
	g.SetData([]driver.Column{{Name: "名前"}, {Name: "n"}},
		[][]driver.Cell{{driver.Text("日本語のテキスト"), driver.NullCell()}})
	g.SortCol = "n"
	out := g.Render(true)
	checkExact(t, out, 40, 5)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "NULL") || !strings.Contains(plain, "n ↑") {
		t.Fatalf("expected NULL and sort arrow:\n%s", plain)
	}
}

func TestAppendKeepsSelection(t *testing.T) {
	g := sample(10, 2)
	g.S.Select(5)
	g.Append([][]driver.Cell{{driver.Text("x"), driver.Text("y")}})
	if g.S.Cursor != 5 || g.S.Total != 11 {
		t.Fatalf("cursor=%d total=%d", g.S.Cursor, g.S.Total)
	}
}

func TestCellAt(t *testing.T) {
	g := sample(30, 4)
	g.Render(true)
	row, col, ok := g.CellAt(g.spans[1].x0+1, 3)
	if !ok || row != 2 || col != 1 {
		t.Fatalf("row=%d col=%d ok=%v", row, col, ok)
	}
	if _, _, ok := g.CellAt(5, 0); ok {
		t.Fatal("header click should not select a cell")
	}
}
