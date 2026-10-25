package browser

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"

	"dbdeck/internal/driver"
	"dbdeck/internal/theme"
	"dbdeck/internal/widgets"
)

const (
	chunkSize = 200   // rows fetched per request
	loadAhead = 40    // fetch the next chunk when the cursor is this close to the end
	maxLoaded = 50000 // stop auto-loading beyond this many rows
)

type dataView struct {
	node    *driver.Node
	isQuery bool
	grid    *widgets.Grid
	orig    [][]driver.Cell // unsorted query rows, to restore on "sort off"

	where    string
	sortCol  string
	sortDesc bool

	total   int // -1 unknown
	more    bool
	loading bool
	meta    string
	msg     string
	elapsed time.Duration
	seq     int

	filter    textinput.Model
	filtering bool

	edit     textinput.Model
	editing  bool
	editRow  int
	editCol  int
	editNull bool

	canEdit bool
	checked bool
	why     string

	redis bool
	w, h  int
}

func newDataView(engine string) *dataView {
	f := textinput.New()
	f.Prompt = " WHERE "
	f.Placeholder = "status = 'done' AND amount > 100"
	if engine == "redis" {
		f.Prompt = " contains "
		f.Placeholder = "text to look for in fields / members"
	}
	e := textinput.New()
	e.Prompt = ""
	return &dataView{grid: widgets.NewGrid(), filter: f, edit: e, total: -1, redis: engine == "redis"}
}

func (d *dataView) resize(w, h int) {
	d.w, d.h = w, h
	d.grid.SetSize(w, h-1)
	d.filter.Width = max(w-20, 10)
	d.edit.Width = max(w-24, 10)
}

// open resets the view for a freshly selected table / key.
func (d *dataView) open(n driver.Node) {
	nn := n
	d.node, d.isQuery = &nn, false
	d.where, d.sortCol, d.sortDesc = "", "", false
	d.filter.SetValue("")
	d.filtering, d.editing = false, false
	d.total, d.more, d.loading = -1, false, false
	d.meta, d.msg, d.elapsed = "", "", 0
	d.canEdit, d.checked, d.why = false, false, ""
	d.grid.SortCol = ""
	d.grid.SetData(nil, nil)
	d.orig = nil
}

// showQuery displays a query result (read-only, sorted locally).
func (d *dataView) showQuery(res driver.Result) {
	d.node, d.isQuery = nil, true
	d.where, d.sortCol, d.sortDesc = "", "", false
	d.filtering, d.editing, d.loading = false, false, false
	d.total, d.more = len(res.Rows), false
	d.meta, d.msg, d.elapsed = "", res.Message, res.Elapsed
	d.canEdit, d.checked = false, true
	d.grid.SortCol = ""
	d.grid.SetData(res.Columns, res.Rows)
	d.orig = append([][]driver.Cell(nil), res.Rows...)
}

func (d *dataView) applyFirst(res driver.Result) {
	col, off := d.grid.Col, d.grid.ColOff
	d.grid.SetData(res.Columns, res.Rows)
	if col < len(res.Columns) { // a reload keeps the selected column
		d.grid.Col, d.grid.ColOff = col, min(off, len(res.Columns)-1)
	}
	d.grid.SortCol, d.grid.SortDesc = d.sortCol, d.sortDesc
	d.total = res.Total
	d.more = res.Truncated
	d.meta, d.msg, d.elapsed = res.Meta, res.Message, res.Elapsed
	d.loading = false
}

func (d *dataView) applyMore(res driver.Result) {
	d.grid.Append(res.Rows)
	d.more = res.Truncated && len(d.grid.Rows) < maxLoaded
	d.loading = false
}

// cycleSort advances asc → desc → off on the selected column.
func (d *dataView) cycleSort() {
	if len(d.grid.Cols) == 0 {
		return
	}
	name := d.grid.Cols[d.grid.Col].Name
	switch {
	case d.sortCol != name:
		d.sortCol, d.sortDesc = name, false
	case !d.sortDesc:
		d.sortDesc = true
	default:
		d.sortCol, d.sortDesc = "", false
	}
	d.grid.SortCol, d.grid.SortDesc = d.sortCol, d.sortDesc
}

// sortLocal sorts query-result rows in memory using the current sort state.
func (d *dataView) sortLocal() {
	if d.sortCol == "" {
		d.grid.ReplaceRows(append([][]driver.Cell(nil), d.orig...))
		return
	}
	idx := -1
	for i, c := range d.grid.Cols {
		if c.Name == d.sortCol {
			idx = i
		}
	}
	if idx < 0 {
		return
	}
	rows := append([][]driver.Cell(nil), d.orig...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i][idx], rows[j][idx]
		if a.Null != b.Null {
			return b.Null // NULLs last
		}
		less := cellLess(a.V, b.V)
		if d.sortDesc {
			return cellLess(b.V, a.V)
		}
		return less
	})
	d.grid.ReplaceRows(rows)
}

func cellLess(a, b string) bool {
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		return fa < fb
	}
	return a < b
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (d *dataView) infoLine(spin string) string {
	switch {
	case d.editing:
		name := ""
		if d.editCol < len(d.grid.Cols) {
			name = d.grid.Cols[d.editCol].Name
		}
		val := d.edit.View()
		if d.editNull {
			val = theme.Null.Render("NULL")
		}
		return theme.Title.Render(" EDIT "+name+" ▸ ") + val + theme.Dim.Render("   enter apply · esc cancel · ctrl+n NULL")
	case d.filtering:
		return d.filter.View() + theme.Dim.Render("   enter apply · esc cancel")
	}
	if d.node == nil && !d.isQuery {
		return theme.Dim.Render(" nothing open")
	}
	var parts []string
	title := "query result"
	if d.node != nil {
		title = d.node.Name
	}
	parts = append(parts, theme.Title.Render(" "+title))
	if d.where != "" {
		parts = append(parts, theme.Warn.Render("WHERE "+d.where))
	}
	if d.sortCol != "" {
		arrow := "↑"
		if d.sortDesc {
			arrow = "↓"
		}
		parts = append(parts, theme.Dim.Render("sort "+arrow+d.sortCol))
	}
	loaded := len(d.grid.Rows)
	switch {
	case d.total >= 0 && d.total != loaded:
		parts = append(parts, theme.Dim.Render(fmt.Sprintf("%s of %s rows", commas(loaded), commas(d.total))))
	case d.more:
		parts = append(parts, theme.Dim.Render(commas(loaded)+"+ rows"))
	default:
		parts = append(parts, theme.Dim.Render(commas(loaded)+" rows"))
	}
	if loaded > 0 {
		parts = append(parts, theme.Dim.Render(fmt.Sprintf("row %s", commas(d.grid.S.Cursor+1))))
	}
	if d.node != nil && d.checked && !d.canEdit {
		parts = append(parts, theme.Warn.Render("read-only"))
	}
	if d.meta != "" {
		parts = append(parts, theme.Dim.Render(d.meta))
	}
	if d.isQuery && d.msg != "" && len(d.grid.Cols) == 0 {
		parts = append(parts, theme.Ok.Render(d.msg))
	}
	if d.elapsed > 0 {
		parts = append(parts, theme.Dim.Render(d.elapsed.Round(100*time.Microsecond).String()))
	}
	if d.loading {
		parts = append(parts, theme.Warn.Render(spin+" loading"))
	}
	return strings.Join(parts, theme.Dim.Render("  ·  "))
}

func (d *dataView) view(focused bool, spin string) string {
	info := widgets.FitStyled(d.infoLine(spin), d.w)
	if d.node == nil && !d.isQuery {
		hint := []string{
			"",
			theme.Dim.Render("  Pick a table (or key) in the explorer and press enter,"),
			theme.Dim.Render("  or write a query in the editor below (alt+3)."),
			"",
			theme.Dim.Render("  ? shows every key binding."),
		}
		return info + "\n" + strings.Join(hint, "\n")
	}
	body := d.grid.Render(focused)
	if len(d.grid.Rows) == 0 && !d.loading {
		lines := strings.Split(body, "\n")
		if len(lines) > 2 {
			text := "  (no rows)"
			if len(d.grid.Cols) == 0 {
				text = "  " + d.msg
				if d.msg == "" {
					text = "  (nothing to show)"
				}
			}
			lines[2] = theme.Dim.Render(text)
			body = strings.Join(lines, "\n")
		}
	}
	return info + "\n" + body
}
