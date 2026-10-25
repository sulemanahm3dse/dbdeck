package browser

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"

	"dbdeck/internal/driver"
	"dbdeck/internal/theme"
	"dbdeck/internal/widgets"
)

const maxTabs = 9

type queryTab struct {
	id      int
	ta      textarea.Model
	status  string
	isErr   bool
	running bool
	hist    []string
	hpos    int // == len(hist) when not browsing history
	draft   string
	hints   []string
}

type queryPanel struct {
	tabs   []*queryTab
	cur    int
	nextID int
	w, h   int
	redis  bool
}

func newQueryPanel(engine string) *queryPanel {
	q := &queryPanel{redis: engine == "redis"}
	q.add()
	return q
}

func (q *queryPanel) newTA() textarea.Model {
	ta := textarea.New()
	if q.redis {
		ta.Placeholder = "PING   (one command per line · ctrl+r runs the current line)"
	} else {
		ta.Placeholder = "SELECT * FROM ...;   (ctrl+r run · ctrl+e explain)"
	}
	ta.Prompt = " "
	ta.ShowLineNumbers = true
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.SetWidth(max(q.w, 20))
	ta.SetHeight(max(q.h-2, 2))
	return ta
}

func (q *queryPanel) add() {
	if len(q.tabs) >= maxTabs {
		return
	}
	q.nextID++
	t := &queryTab{id: q.nextID, ta: q.newTA()}
	q.tabs = append(q.tabs, t)
	q.cur = len(q.tabs) - 1
}

func (q *queryPanel) closeCur() {
	if len(q.tabs) == 1 {
		q.tabs[0].ta.SetValue("")
		q.tabs[0].status, q.tabs[0].isErr = "", false
		return
	}
	q.tabs = append(q.tabs[:q.cur], q.tabs[q.cur+1:]...)
	if q.cur >= len(q.tabs) {
		q.cur = len(q.tabs) - 1
	}
}

func (q *queryPanel) tab() *queryTab { return q.tabs[q.cur] }

func (q *queryPanel) byID(id int) *queryTab {
	for _, t := range q.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

func (q *queryPanel) switchTo(i int) {
	if i < 0 || i >= len(q.tabs) {
		return
	}
	q.tabs[q.cur].ta.Blur()
	q.cur = i
}

func (q *queryPanel) resize(w, h int) {
	q.w, q.h = w, h
	for _, t := range q.tabs {
		t.ta.SetWidth(max(w, 20))
		t.ta.SetHeight(max(h-2, 2))
	}
}

// input returns the text to execute: the whole buffer for SQL, the current line for redis.
func (t *queryTab) input(redis bool) string {
	if !redis {
		return t.ta.Value()
	}
	lines := strings.Split(t.ta.Value(), "\n")
	if r := t.ta.Line(); r >= 0 && r < len(lines) {
		return lines[r]
	}
	return ""
}

func (t *queryTab) pushHistory(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if n := len(t.hist); n == 0 || t.hist[n-1] != s {
		t.hist = append(t.hist, s)
	}
	t.hpos = len(t.hist)
	t.draft = ""
}

func (t *queryTab) histMove(dir int) {
	if len(t.hist) == 0 {
		return
	}
	if t.hpos == len(t.hist) {
		t.draft = t.ta.Value()
	}
	next := t.hpos + dir
	switch {
	case next < 0:
		next = 0
	case next >= len(t.hist):
		t.hpos = len(t.hist)
		t.ta.SetValue(t.draft)
		return
	}
	t.hpos = next
	t.ta.SetValue(t.hist[next])
}

// word returns the identifier fragment immediately before the cursor.
func (t *queryTab) word() string {
	lines := strings.Split(t.ta.Value(), "\n")
	row := t.ta.Line()
	if row < 0 || row >= len(lines) {
		return ""
	}
	li := t.ta.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	rs := []rune(lines[row])
	if col > len(rs) {
		col = len(rs)
	}
	start := col
	for start > 0 && (unicode.IsLetter(rs[start-1]) || unicode.IsDigit(rs[start-1]) || rs[start-1] == '_') {
		start--
	}
	return string(rs[start:col])
}

func (t *queryTab) refreshHints(h driver.Hinter) {
	t.hints = nil
	if h == nil {
		return
	}
	if w := t.word(); len(w) >= 2 {
		t.hints = h.Hints(w)
		if len(t.hints) > 6 {
			t.hints = t.hints[:6]
		}
	}
}

// complete inserts the first hint's remaining characters; reports whether it did.
func (t *queryTab) complete() bool {
	if len(t.hints) == 0 {
		return false
	}
	w, hint := t.word(), t.hints[0]
	if len(hint) <= len(w) {
		return false
	}
	suffix := hint[len(w):]
	if w == strings.ToLower(w) && hint == strings.ToUpper(hint) {
		suffix = strings.ToLower(suffix) // keep the user's lower-case style for keywords
	}
	t.ta.InsertString(suffix)
	t.hints = nil
	return true
}

func (q *queryPanel) view(w, h int, focused bool) string {
	var tabs []string
	for i, t := range q.tabs {
		label := fmt.Sprintf(" %d ", t.id)
		if t.running {
			label = fmt.Sprintf(" %d… ", t.id)
		}
		if i == q.cur {
			tabs = append(tabs, theme.CellSel.Render(label))
		} else {
			tabs = append(tabs, theme.Dim.Render(label))
		}
	}
	title := theme.Title.Render(" Query ") + strings.Join(tabs, "")
	if focused {
		title += theme.Dim.Render("  ctrl+r run · ctrl+t new tab · esc back")
	}
	t := q.tab()
	status := ""
	switch {
	case len(t.hints) > 0 && focused:
		status = theme.Dim.Render(" tab ▸ ") + theme.Warn.Render(strings.Join(t.hints, "  "))
	case t.isErr:
		status = theme.Err.Render(" ✖ " + widgets.Clean(t.status))
	case t.status != "":
		status = theme.Ok.Render(" ✔ " + widgets.Clean(t.status))
	}
	return widgets.FitStyled(title, w) + "\n" + t.ta.View() + "\n" + widgets.FitStyled(status, w)
}
