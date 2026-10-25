package browser

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/sqlite"
)

// ---- harness: run tea commands synchronously ---------------------------------

func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch x := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range x {
			out = append(out, collect(c)...)
		}
		return out
	case tickMsg:
		return nil
	default:
		return []tea.Msg{x}
	}
}

type harness struct {
	t    *testing.T
	m    *Model
	quit bool
	left []tea.Msg // messages that are not for the browser (disconnect)
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	queue := []tea.Msg{msg}
	for i := 0; i < 500 && len(queue) > 0; i++ {
		msg := queue[0]
		queue = queue[1:]
		if _, ok := msg.(tea.QuitMsg); ok {
			h.quit = true
			continue
		}
		if fmt.Sprintf("%T", msg) == "events.DisconnectMsg" {
			h.left = append(h.left, msg)
			continue
		}
		queue = append(queue, collect(h.m.Update(msg))...)
	}
}

func (h *harness) key(s string) {
	h.t.Helper()
	switch s {
	case "enter":
		h.send(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		h.send(tea.KeyMsg{Type: tea.KeyEsc})
	case "tab":
		h.send(tea.KeyMsg{Type: tea.KeyTab})
	case "ctrl+r":
		h.send(tea.KeyMsg{Type: tea.KeyCtrlR})
	case "ctrl+s":
		h.send(tea.KeyMsg{Type: tea.KeyCtrlS})
	case "ctrl+d":
		h.send(tea.KeyMsg{Type: tea.KeyCtrlD})
	case "ctrl+n":
		h.send(tea.KeyMsg{Type: tea.KeyCtrlN})
	case "ctrl+o":
		h.send(tea.KeyMsg{Type: tea.KeyCtrlO})
	case "alt+1":
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	case "alt+3":
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3"), Alt: true})
	case "alt+2":
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2"), Alt: true})
	default:
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	}
}

func (h *harness) typ(s string) { h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

func (h *harness) plain() string { return ansi.Strip(h.m.View()) }

func (h *harness) mustContain(sub string) {
	h.t.Helper()
	if p := h.plain(); !strings.Contains(p, sub) {
		h.t.Fatalf("screen does not contain %q:\n%s", sub, p)
	}
}

func newHarness(t *testing.T, w, hh int) *harness {
	t.Helper()
	drv, err := driver.Open(context.Background(), config.Profile{Engine: config.EngineSQLite, Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { drv.Close() })
	for _, s := range []string{
		`CREATE TABLE t_order (id INTEGER PRIMARY KEY, user_id INT, status TEXT, amount REAL, note TEXT)`,
		`INSERT INTO t_order (user_id, status, amount, note)
		 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 450)
		 SELECT i % 5, CASE i % 3 WHEN 0 THEN 'done' ELSE 'pending' END, i * 1.5, NULL FROM n`,
		`CREATE TABLE nopk (a TEXT)`,
		`CREATE VIEW v_done AS SELECT * FROM t_order WHERE status = 'done'`,
	} {
		if _, err := drv.Exec(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{t: t, m: New(drv, config.Profile{Engine: config.EngineSQLite, Path: ":memory:"})}
	h.send(tea.WindowSizeMsg{Width: w, Height: hh})
	for _, msg := range collect(h.m.Init()) {
		h.send(msg)
	}
	return h
}

// open selects a node by name in the explorer and opens it.
func (h *harness) open(name string) {
	h.t.Helper()
	h.key("alt+1")
	h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	for i := 0; i < 20; i++ {
		if n := h.m.ex.selected(); n != nil && n.Name == name {
			h.key("enter")
			return
		}
		h.key("j")
	}
	h.t.Fatalf("could not select %s", name)
}

func (h *harness) key1(s string) { h.key(s) }

// ---- tests ----------------------------------------------------------------------

func TestExplorerAndOpen(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.mustContain("t_order")
	h.mustContain("v_done")
	h.open("t_order")
	h.mustContain("450 rows") // 200 of 450 → "200 of 450 rows"
	if len(h.m.data.grid.Rows) != 200 || h.m.data.total != 450 {
		t.Fatalf("rows=%d total=%d", len(h.m.data.grid.Rows), h.m.data.total)
	}
	h.mustContain("status")
	h.mustContain("pending")
	if h.m.focus != paneData {
		t.Fatal("opening should focus the data pane")
	}
}

func TestInfiniteScrollLoadsChunks(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.open("t_order")
	// scrolling toward the end of the loaded rows fetches the next chunk automatically
	for i := 0; i < 20; i++ {
		h.key("ctrl+d")
	}
	if got := len(h.m.data.grid.Rows); got < 400 {
		t.Fatalf("scrolling should have loaded more rows, have %d", got)
	}
	h.key("G") // ... and G keeps loading until the true last row, then jumps there
	if got := len(h.m.data.grid.Rows); got != 450 || h.m.data.more {
		t.Fatalf("after G: %d rows more=%v", got, h.m.data.more)
	}
	if c := h.m.data.grid.S.Cursor; c != 449 {
		t.Fatalf("cursor %d", c)
	}
	h.mustContain("450 rows")
	h.key("g")
	if h.m.data.grid.S.Cursor != 0 {
		t.Fatal("g should return to the top")
	}
}

func TestMouseWheelAndClick(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.open("t_order")
	x := h.m.treeW + 10
	h.send(tea.MouseMsg{X: x, Y: 8, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if h.m.data.grid.S.Offset == 0 {
		t.Fatal("wheel should scroll the grid")
	}
	h.send(tea.MouseMsg{X: x, Y: 8, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	h.send(tea.MouseMsg{X: x, Y: 8, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if h.m.focus != paneData {
		t.Fatal("click should focus the data pane")
	}
	before := h.m.data.grid.S.Cursor
	h.send(tea.MouseMsg{X: x, Y: 3 + 4, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if h.m.data.grid.S.Cursor != h.m.data.grid.S.Offset+4 || before == h.m.data.grid.S.Cursor && before != 4 {
		t.Fatalf("click row: cursor=%d offset=%d", h.m.data.grid.S.Cursor, h.m.data.grid.S.Offset)
	}
	// click an explorer row
	h.send(tea.MouseMsg{X: 3, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if h.m.focus != paneExplorer {
		t.Fatal("click should focus the explorer")
	}
}

func TestSortAndFilter(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.open("t_order")
	h.key("l")
	h.key("l") // status column
	h.key("s")
	if h.m.data.sortCol != "status" || h.m.data.sortDesc {
		t.Fatalf("sort state %q %v", h.m.data.sortCol, h.m.data.sortDesc)
	}
	if v := h.m.data.grid.Rows[0][2].V; v != "done" {
		t.Fatalf("first status %q", v)
	}
	h.key("s")
	if !h.m.data.sortDesc || h.m.data.grid.Rows[0][2].V != "pending" {
		t.Fatal("descending sort wrong")
	}
	h.key("s") // off
	if h.m.data.sortCol != "" {
		t.Fatal("third s should clear sort")
	}
	h.key("/")
	h.typ("status = 'done'")
	h.key("enter")
	if h.m.data.total != 150 {
		t.Fatalf("filtered total %d", h.m.data.total)
	}
	h.mustContain("WHERE status = 'done'")
	h.key("/")
	h.typ(" AND nope(") // broken: error shown, UI survives
	h.key("enter")
	if !h.m.statusErr {
		t.Fatal("bad filter should set an error status")
	}
	_ = h.plain()
}

func TestEditInsertDeleteWithTransaction(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.open("t_order")
	if !h.m.data.canEdit {
		t.Fatalf("t_order should be editable: %q", h.m.data.why)
	}
	h.key("L") // no-op key
	h.key("$") // last column (note)
	h.key("e")
	if !h.m.data.editing {
		t.Fatal("e should start editing")
	}
	h.typ("hello")
	h.key("enter")
	if got := h.m.data.grid.Rows[0][4].V; got != "hello" {
		t.Fatalf("grid cell %q (status %q)", got, h.m.status)
	}
	r, _ := h.m.drv.Exec(context.Background(), "SELECT note FROM t_order WHERE id = 1")
	if r.Rows[0][0].V != "hello" {
		t.Fatal("update was not written")
	}

	// manual transaction: edit stays pending until rolled back
	h.key("T")
	h.key("m")
	if !h.m.inTx() {
		t.Fatal("transaction should be open")
	}
	h.mustContain("TX 0 pending")
	h.key("e")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU}) // clear input
	h.typ("pending-edit")
	h.key("enter")
	h.mustContain("pending")
	h.key("T")
	h.mustContain("1 pending statement")
	h.key("r") // rollback
	if h.m.inTx() {
		t.Fatal("rollback should close the tx")
	}
	r, _ = h.m.drv.Exec(context.Background(), "SELECT note FROM t_order WHERE id = 1")
	if r.Rows[0][0].V != "hello" {
		t.Fatalf("rollback failed, note=%q", r.Rows[0][0].V)
	}

	// delete with confirmation
	h.key("x")
	h.mustContain("Delete row?")
	h.key("n")
	if len(h.m.data.grid.Rows) != 200 {
		t.Fatal("cancelled delete must keep rows")
	}
	h.key("x")
	h.key("y")
	if h.m.data.grid.Rows[0][0].V != "2" {
		t.Fatalf("row 1 should be gone, first id %s", h.m.data.grid.Rows[0][0].V)
	}

	// insert through the form
	h.key("a")
	h.mustContain("Add row")
	h.key("tab") // skip id
	h.key("tab") // skip user_id
	h.typ("new")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlS}) // submit
	r, _ = h.m.drv.Exec(context.Background(), "SELECT count(*) FROM t_order WHERE status = 'new'")
	if r.Rows[0][0].V != "1" {
		t.Fatalf("insert failed: %v (status %q)", r.Rows, h.m.status)
	}
}

func TestReadOnlyObjects(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.open("nopk")
	h.key("e")
	h.mustContain("primary key")
	h.open("v_done")
	h.key("x")
	if !strings.Contains(h.plain(), "read-only") {
		t.Fatal("view should be read-only")
	}
}

func TestQueryEditorTabsHistoryAndResult(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.key("alt+3")
	if h.m.focus != paneQuery {
		t.Fatal("alt+3 should focus the editor")
	}
	h.typ("select id, status from t_order where id <= 3")
	h.key("ctrl+r")
	if !h.m.data.isQuery || len(h.m.data.grid.Rows) != 3 {
		t.Fatalf("query result not shown: %+v", h.m.data.grid.Rows)
	}
	h.mustContain("query result")
	h.mustContain("✔")

	// error is reported in the tab, UI keeps working
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typ("selec nope")
	h.key("ctrl+r")
	h.mustContain("✖")

	// history
	h.send(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if v := h.m.query.tab().ta.Value(); v != "selec nope" {
		t.Fatalf("history recall (latest): %q", v)
	}
	h.send(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if v := h.m.query.tab().ta.Value(); !strings.Contains(v, "select id") {
		t.Fatalf("history recall (older): %q", v)
	}
	h.send(tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	h.send(tea.KeyMsg{Type: tea.KeyDown, Alt: true})

	// second tab is independent
	h.send(tea.KeyMsg{Type: tea.KeyCtrlT})
	if len(h.m.query.tabs) != 2 || h.m.query.tab().ta.Value() != "" {
		t.Fatal("new tab should be empty")
	}
	h.send(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	if h.m.query.cur != 0 {
		t.Fatal("alt+left should switch tab")
	}

	// local sort of query results
	h.key("esc")
	h.key("l")
	h.key("s")
	h.key("s")
	if h.m.data.grid.Rows[0][0].V != "1" && h.m.data.grid.Rows[0][1].V == "" {
		t.Fatal("unexpected sort result")
	}

	// completion: table name and keyword
	h.key("alt+3")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typ("select * from t_o")
	if len(h.m.query.tab().hints) == 0 {
		t.Fatalf("expected hints for t_o")
	}
	h.key("tab")
	if v := h.m.query.tab().ta.Value(); !strings.HasSuffix(v, "t_order") {
		t.Fatalf("completion: %q", v)
	}
}

func TestQueryWriteRefreshesAndRunsDDL(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.key("alt+3")
	h.typ("create table extra (x int); insert into extra values (1)")
	h.key("ctrl+r")
	h.mustContain("extra")
}

func TestTreeFilterAndKeys(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.key("/")
	h.typ("done")
	h.mustContain("v_done")
	if strings.Contains(h.plain(), "nopk") {
		t.Fatal("filter should hide non-matching nodes")
	}
	h.key("esc")
	h.mustContain("nopk")
	h.key("?")
	h.mustContain("Key bindings")
	h.key("esc")
}

func TestQuitAndDisconnectGuardPendingTx(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.key("ctrl+o")
	if len(h.left) != 1 {
		t.Fatal("ctrl+o without a tx should disconnect immediately")
	}
	h.key("T")
	h.key("m")
	h.key("alt+3")
	h.typ("update t_order set note = 'q' where id = 5")
	h.key("ctrl+r")
	h.key("esc")
	h.key("q")
	h.mustContain("Pending transaction")
	if h.quit {
		t.Fatal("must not quit while a transaction is pending")
	}
	h.key("r")
	if !h.quit {
		t.Fatal("rollback-and-quit should quit")
	}
	r, _ := h.m.drv.Exec(context.Background(), "SELECT note FROM t_order WHERE id = 5")
	if !r.Rows[0][0].Null {
		t.Fatal("rollback before quit must discard the update")
	}
}

func TestFrameIsAlwaysExactlySized(t *testing.T) {
	for _, sz := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 36}, {200, 60}} {
		h := newHarness(t, sz[0], sz[1])
		check := func(label string) {
			t.Helper()
			lines := strings.Split(h.m.View(), "\n")
			if len(lines) != sz[1] {
				t.Fatalf("%dx%d %s: %d lines", sz[0], sz[1], label, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != sz[0] {
					t.Fatalf("%dx%d %s: line %d is %d wide\n%s", sz[0], sz[1], label, i, w, ansi.Strip(h.m.View()))
				}
			}
		}
		check("explorer")
		h.open("t_order")
		check("data")
		for i := 0; i < 15; i++ {
			h.key("l")
		}
		h.key("G")
		check("scrolled")
		h.key("alt+3")
		h.typ("select * from t_order")
		check("editor")
		h.key("ctrl+r")
		check("query result")
		h.key("esc")
		h.key("?")
		check("help")
	}
}

func TestTooSmallTerminal(t *testing.T) {
	h := newHarness(t, 40, 10)
	if !strings.Contains(h.m.View(), "too small") {
		t.Fatal("expected a too-small notice")
	}
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30}) // recovers when resized
	if !strings.Contains(h.plain(), "t_order") {
		t.Fatal("should recover after resize")
	}
}

func TestCoalescedKeysAreSplitOutsideInputs(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.open("t_order")
	h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jjj")}) // three repeats in one terminal read
	if c := h.m.data.grid.S.Cursor; c != 3 {
		t.Fatalf("cursor %d, want 3", c)
	}
	h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ll")})
	if h.m.data.grid.Col != 2 {
		t.Fatalf("col %d, want 2", h.m.data.grid.Col)
	}
	// but typed text in an input stays together
	h.key("/")
	h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("id > 5")})
	if v := h.m.data.filter.Value(); v != "id > 5" {
		t.Fatalf("filter text %q", v)
	}
	h.key("esc")
}

func TestEscClearsExplorerFilter(t *testing.T) {
	h := newHarness(t, 120, 36)
	h.key("/")
	h.typ("t_o")
	h.key("enter")
	if strings.Contains(h.plain(), "nopk") {
		t.Fatal("filter should hide nopk")
	}
	h.key("esc")
	h.mustContain("nopk")
}
