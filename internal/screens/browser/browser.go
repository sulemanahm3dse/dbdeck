// Package browser is the main workspace: explorer tree, data grid and query editor.
package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	"dbdeck/internal/events"
	"dbdeck/internal/keymap"
	"dbdeck/internal/widgets"
)

type pane int

const (
	paneExplorer pane = iota
	paneData
	paneQuery
)

// ---- messages (results of background work) ---------------------------------

type treeMsg struct {
	parentID string
	root     bool
	nodes    []driver.Node
	err      error
}
type dataMsg struct {
	seq  int
	more bool
	res  driver.Result
	err  error
}
type execMsg struct {
	tabID int
	res   driver.Result
	err   error
}
type canEditMsg struct {
	nodeID string
	ok     bool
	why    string
}
type editMsg struct {
	kind string // update | insert | delete
	row  int
	col  int
	cell driver.Cell
	show string
	err  error
}
type txMsg struct {
	op   string
	err  error
	then string
}
type searchMsg struct {
	text  string
	nodes []driver.Node
	err   error
}
type tickMsg struct{}

// Model is the browser screen.
type Model struct {
	drv  driver.Driver
	prof config.Profile

	ex    *explorer
	data  *dataView
	query *queryPanel
	focus pane

	w, h                 int
	treeW, rightW        int
	paneH, dataH, queryH int

	overlay   widgets.Overlay
	status    string
	statusErr bool

	gotoEnd     bool // G pressed on a table with unloaded rows: keep loading, then jump
	cancelData  context.CancelFunc
	cancelQuery context.CancelFunc
	keepCursor  int
	onFirstLoad func()

	spin    int
	ticking bool

	lastClickAt  time.Time
	lastClickRow int
}

func New(drv driver.Driver, prof config.Profile) *Model {
	m := &Model{
		drv: drv, prof: prof,
		ex: newExplorer(), data: newDataView(drv.Engine()), query: newQueryPanel(drv.Engine()),
		focus: paneExplorer, keepCursor: -1,
	}
	m.ex.loadingRoot = true
	return m
}

func (m *Model) Init() tea.Cmd { return m.treeCmd(nil) }

// ---- helpers ------------------------------------------------------------------

func (m *Model) setStatus(s string, isErr bool) { m.status, m.statusErr = s, isErr }

func (m *Model) busy() bool {
	if m.data.loading || m.ex.loadingRoot || m.ex.searching {
		return true
	}
	for _, t := range m.query.tabs {
		if t.running {
			return true
		}
	}
	var walk func(ns []*tnode) bool
	walk = func(ns []*tnode) bool {
		for _, n := range ns {
			if n.loading || walk(n.children) {
				return true
			}
		}
		return false
	}
	return walk(m.ex.roots)
}

func tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) spinner() string { return widgets.SpinnerFrames[m.spin%len(widgets.SpinnerFrames)] }

func (m *Model) setFocus(p pane) {
	m.focus = p
	for _, t := range m.query.tabs {
		t.ta.Blur()
	}
	if p == paneQuery {
		m.query.tab().ta.Focus()
	}
}

func (m *Model) show(o widgets.Overlay) {
	o.Resize(m.w, m.h)
	m.overlay = o
}

func (m *Model) layout() {
	m.paneH = m.h - 1
	m.treeW = min(max(m.w/4, 24), 38)
	m.rightW = m.w - m.treeW
	m.dataH = max(m.paneH*58/100, 8)
	m.queryH = m.paneH - m.dataH
	if m.queryH < 7 {
		m.queryH = 7
		m.dataH = m.paneH - m.queryH
	}
	m.ex.s.SetHeight(m.paneH - 3)
	m.data.resize(m.rightW-2, m.dataH-2)
	m.query.resize(m.rightW-2, m.queryH-2)
}

// typing reports whether keys currently go into a text input (where runs of
// characters must stay together).
func (m *Model) typing() bool {
	if m.overlay != nil {
		_, isForm := m.overlay.(*widgets.Form)
		return isForm
	}
	return m.focus == paneQuery || m.data.editing || m.data.filtering || m.ex.filtering
}

func (m *Model) inTx() bool {
	tx, ok := m.drv.(driver.Transactor)
	return ok && tx.InTx()
}

// ---- Update ------------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	cmd := m.update(msg)
	if m.busy() && !m.ticking {
		m.ticking = true
		return tea.Batch(cmd, tick())
	}
	return cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.w >= 60 && m.h >= 16 {
			m.layout()
		}
		if m.overlay != nil {
			m.overlay.Resize(m.w, m.h)
		}
		return nil

	case tickMsg:
		m.spin++
		if m.busy() {
			return tick()
		}
		m.ticking = false
		return nil

	case widgets.Result:
		m.overlay = nil
		return m.handleResult(msg)

	case searchMsg:
		if msg.text != m.ex.filter {
			return nil // superseded
		}
		m.ex.searching = false
		if msg.err != nil {
			m.setStatus("search: "+msg.err.Error(), true)
			return nil
		}
		m.ex.setResults(msg.text, msg.nodes)
		return nil
	case treeMsg:
		return m.onTree(msg)
	case dataMsg:
		return m.onData(msg)
	case execMsg:
		return m.onExec(msg)
	case canEditMsg:
		if m.data.node != nil && m.data.node.ID == msg.nodeID {
			m.data.canEdit, m.data.checked, m.data.why = msg.ok, true, msg.why
		}
		return nil
	case editMsg:
		return m.onEdit(msg)
	case txMsg:
		return m.onTx(msg)

	case tea.KeyMsg:
		if !m.typing() {
			if parts := widgets.SplitRunes(msg); len(parts) > 1 {
				cmds := make([]tea.Cmd, 0, len(parts))
				for _, k := range parts {
					cmds = append(cmds, m.update(k))
				}
				return tea.Batch(cmds...)
			}
		}
		if m.overlay != nil {
			var cmd tea.Cmd
			m.overlay, cmd = m.overlay.Update(msg)
			return cmd
		}
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return nil
}

// ---- background commands -----------------------------------------------------

func (m *Model) treeCmd(parent *tnode) tea.Cmd {
	drv := m.drv
	var p *driver.Node
	id := ""
	if parent != nil {
		n := parent.Node
		p, id = &n, parent.ID
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		nodes, err := drv.Tree(ctx, p)
		return treeMsg{parentID: id, root: p == nil, nodes: nodes, err: err}
	}
}

func (m *Model) onTree(msg treeMsg) tea.Cmd {
	if msg.root {
		if msg.err != nil {
			m.ex.loadingRoot, m.ex.rootErr = false, msg.err.Error()
			m.setStatus("explorer: "+msg.err.Error(), true)
			return nil
		}
		m.ex.setRoots(msg.nodes)
		// a single schema / database (or Postgres' "public") is the obvious thing to open
		var cmds []tea.Cmd
		for _, n := range m.ex.roots {
			if n.Expandable() && (len(m.ex.roots) == 1 || n.Name == "public") {
				n.loading, n.expanded = true, true
				cmds = append(cmds, m.treeCmd(n))
			}
		}
		return tea.Batch(cmds...)
	}
	m.ex.setChildren(msg.parentID, msg.nodes, msg.err)
	if msg.err != nil {
		m.setStatus("explorer: "+msg.err.Error(), true)
	}
	return nil
}

func (m *Model) loadData(more bool) tea.Cmd {
	d := m.data
	if d.node == nil {
		return nil
	}
	if m.cancelData != nil {
		m.cancelData()
		m.cancelData = nil
	}
	d.seq++
	seq := d.seq
	offset := 0
	if more {
		offset = len(d.grid.Rows)
	}
	opts := driver.BrowseOptions{Where: d.where, SortCol: d.sortCol, SortDesc: d.sortDesc, Offset: offset, Limit: chunkSize}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	m.cancelData = cancel
	d.loading = true
	drv, node := m.drv, *d.node
	return func() tea.Msg {
		res, err := drv.Browse(ctx, node, opts)
		return dataMsg{seq: seq, more: more, res: res, err: err}
	}
}

func (m *Model) onData(msg dataMsg) tea.Cmd {
	d := m.data
	if msg.seq != d.seq {
		return nil // stale reply from an earlier request
	}
	if m.cancelData != nil {
		m.cancelData()
		m.cancelData = nil
	}
	d.loading = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			m.setStatus("cancelled", false)
		} else {
			m.setStatus(msg.err.Error(), true)
		}
		return nil
	}
	if msg.more {
		d.applyMore(msg.res)
		if m.gotoEnd {
			if d.more {
				return m.loadData(true)
			}
			m.finishGotoEnd(msg.res.Truncated)
		}
		return nil
	}
	d.applyFirst(msg.res)
	if m.gotoEnd {
		if d.more {
			return m.loadData(true)
		}
		m.finishGotoEnd(false)
	}
	if m.keepCursor >= 0 && len(d.grid.Rows) > 0 {
		d.grid.S.Select(min(m.keepCursor, len(d.grid.Rows)-1))
	}
	m.keepCursor = -1
	if m.onFirstLoad != nil {
		m.onFirstLoad()
		m.onFirstLoad = nil
	}
	return m.maybeLoadMore()
}

func (m *Model) finishGotoEnd(capped bool) {
	m.gotoEnd = false
	m.data.grid.S.Bottom()
	if capped {
		m.setStatus(fmt.Sprintf("stopped at %s rows — use / to filter for the rest", commas(maxLoaded)), false)
	} else {
		m.setStatus("", false)
	}
}

func (m *Model) maybeLoadMore() tea.Cmd {
	d := m.data
	if d.node != nil && d.more && !d.loading && len(d.grid.Rows) < maxLoaded && d.grid.S.NearEnd(loadAhead) {
		return m.loadData(true)
	}
	if d.node != nil && d.more && len(d.grid.Rows) >= maxLoaded && d.grid.S.NearEnd(2) {
		m.setStatus(fmt.Sprintf("stopped loading at %s rows — use / to filter", commas(maxLoaded)), false)
	}
	return nil
}

func (m *Model) checkEdit(n driver.Node) tea.Cmd {
	ed, ok := m.drv.(driver.Editor)
	return func() tea.Msg {
		if !ok {
			return canEditMsg{nodeID: n.ID, why: "this engine is read-only here"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		okk, why := ed.CanEdit(ctx, n)
		return canEditMsg{nodeID: n.ID, ok: okk, why: why}
	}
}

func (m *Model) openNode(n *tnode) tea.Cmd {
	m.ex.active = n.ID
	m.gotoEnd = false
	m.data.open(n.Node)
	m.setFocus(paneData)
	return tea.Batch(m.loadData(false), m.checkEdit(n.Node))
}

func (m *Model) reloadData() tea.Cmd {
	if m.data.node == nil {
		return nil
	}
	m.keepCursor = m.data.grid.S.Cursor
	return m.loadData(false)
}

// ---- queries -----------------------------------------------------------------

func (m *Model) runQuery(explain bool) tea.Cmd {
	t := m.query.tab()
	if t.running {
		return nil
	}
	input := t.input(m.query.redis)
	if strings.TrimSpace(input) == "" {
		t.status, t.isErr = "nothing to run", true
		return nil
	}
	if explain {
		ex, ok := m.drv.(driver.Explainer)
		if !ok {
			t.status, t.isErr = "explain is not available for this engine", true
			return nil
		}
		stmts := driver.SplitStatements(input)
		if len(stmts) == 0 {
			return nil
		}
		input = ex.ExplainPrefix() + stmts[0]
	} else {
		t.pushHistory(input)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.cancelQuery = cancel
	t.running, t.isErr, t.status = true, false, "running…"
	id, drv := t.id, m.drv
	return func() tea.Msg {
		res, err := drv.Exec(ctx, input)
		return execMsg{tabID: id, res: res, err: err}
	}
}

func (m *Model) onExec(msg execMsg) tea.Cmd {
	if m.cancelQuery != nil {
		m.cancelQuery()
		m.cancelQuery = nil
	}
	t := m.query.byID(msg.tabID)
	if t == nil {
		return nil
	}
	t.running = false
	if msg.err != nil {
		t.isErr = true
		if errors.Is(msg.err, context.Canceled) {
			t.status = "cancelled"
		} else {
			t.status = msg.err.Error()
		}
		return nil
	}
	t.isErr = false
	t.status = strings.TrimSpace(msg.res.Message + " · " + msg.res.Elapsed.Round(100*time.Microsecond).String())
	if len(msg.res.Columns) > 0 {
		m.data.showQuery(msg.res)
		return nil
	}
	// a statement without rows may have changed data or schema
	m.ex.loadingRoot = true
	return tea.Batch(m.treeCmd(nil), m.reloadData())
}

// cancelAny aborts running work; it reports whether there was any.
func (m *Model) cancelAny() bool {
	did := false
	m.gotoEnd = false
	if m.cancelQuery != nil {
		m.cancelQuery()
		m.cancelQuery = nil
		did = true
	}
	if m.cancelData != nil && m.data.loading {
		m.cancelData()
		m.cancelData = nil
		m.data.loading = false
		m.data.seq++ // drop the reply
		did = true
	}
	if did {
		m.setStatus("cancelled", false)
	}
	return did
}

// ---- transactions ------------------------------------------------------------

func (m *Model) txCmd(op, then string) tea.Cmd {
	tx, ok := m.drv.(driver.Transactor)
	if !ok {
		m.setStatus("transactions are not supported by this engine", true)
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		switch op {
		case "begin":
			err = tx.Begin(ctx)
		case "commit":
			err = tx.Commit(ctx)
		case "rollback":
			err = tx.Rollback(ctx)
		}
		return txMsg{op: op, err: err, then: then}
	}
}

func (m *Model) onTx(msg txMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	switch msg.op {
	case "begin":
		m.setStatus("manual transaction started — edits stay pending until you commit (ctrl+s)", false)
	case "commit":
		m.setStatus("transaction committed", false)
	case "rollback":
		m.setStatus("transaction rolled back", false)
	}
	switch msg.then {
	case "quit":
		return tea.Quit
	case "disconnect":
		return func() tea.Msg { return events.DisconnectMsg{} }
	}
	if msg.op != "begin" {
		return m.reloadData()
	}
	return nil
}

func (m *Model) txMenu() tea.Cmd {
	tx, ok := m.drv.(driver.Transactor)
	if !ok {
		m.setStatus("transactions are not supported by this engine", true)
		return nil
	}
	if tx.InTx() {
		log := tx.TxLog()
		lines := []string{"MANUAL mode — edits are pending until you commit.", "", fmt.Sprintf("%d pending statement(s):", len(log))}
		lines = append(lines, log...)
		m.show(widgets.NewMenu("tx", "Transaction", lines, []widgets.MenuOption{{Key: "c", Label: "commit"}, {Key: "r", Label: "rollback"}}))
		return nil
	}
	m.show(widgets.NewMenu("tx", "Transaction", []string{
		"AUTO mode — every edit is applied immediately.",
		"",
		"Start a manual transaction to collect edits and review them",
		"before they are written. Nothing changes until you commit.",
	}, []widgets.MenuOption{{Key: "m", Label: "start manual transaction"}}))
	return nil
}

// guardPending asks what to do with an open transaction before leaving.
func (m *Model) guardPending(id, verb string) bool {
	tx, ok := m.drv.(driver.Transactor)
	if !ok || !tx.InTx() {
		return false
	}
	lines := []string{fmt.Sprintf("%d statement(s) are pending in a manual transaction.", len(tx.TxLog())), "", "What should happen to them before you " + verb + "?"}
	lines = append(lines, tx.TxLog()...)
	m.show(widgets.NewMenu(id, "Pending transaction", lines, []widgets.MenuOption{{Key: "c", Label: "commit"}, {Key: "r", Label: "rollback"}}))
	return true
}

func (m *Model) requestQuit() tea.Cmd {
	if m.guardPending("quit-tx", "quit") {
		return nil
	}
	return tea.Quit
}

func (m *Model) requestDisconnect() tea.Cmd {
	if m.guardPending("disc-tx", "disconnect") {
		return nil
	}
	return func() tea.Msg { return events.DisconnectMsg{} }
}

// ---- overlay results ---------------------------------------------------------

func (m *Model) handleResult(r widgets.Result) tea.Cmd {
	switch r.ID {
	case "tx":
		switch r.Choice {
		case "m":
			return m.txCmd("begin", "")
		case "c":
			return m.txCmd("commit", "")
		case "r":
			return m.txCmd("rollback", "")
		}
	case "quit-tx", "disc-tx":
		then := "quit"
		if r.ID == "disc-tx" {
			then = "disconnect"
		}
		switch r.Choice {
		case "c":
			return m.txCmd("commit", then)
		case "r":
			return m.txCmd("rollback", then)
		}
	case "delete":
		if r.Choice == "y" {
			return m.doDelete()
		}
	case "insert":
		if vals, ok := r.Data.(map[string]driver.Cell); ok && r.Choice == "ok" {
			return m.doInsert(vals)
		}
	case "inspect":
		switch r.Choice {
		case "copy":
			m.copyText(fmt.Sprint(r.Data))
		case "edit":
			m.startEdit()
		}
	}
	return nil
}

func (m *Model) copyText(s string) {
	if err := clipboard.WriteAll(s); err != nil {
		m.setStatus("clipboard unavailable: "+err.Error(), true)
		return
	}
	m.setStatus(fmt.Sprintf("copied %d characters", len([]rune(s))), false)
}

// ---- key handling --------------------------------------------------------------

func (m *Model) handleKey(k tea.KeyMsg) tea.Cmd {
	if k.String() == "ctrl+c" {
		if m.cancelAny() {
			return nil
		}
		return m.requestQuit()
	}
	m.setStatus("", false)

	// modal inputs swallow keys first
	switch {
	case m.focus == paneQuery:
		return m.queryKey(k)
	case m.focus == paneData && (m.data.editing || m.data.filtering):
		return m.dataInputKey(k)
	case m.focus == paneExplorer && m.ex.filtering:
		return m.explorerFilterKey(k)
	}

	switch {
	case key.Matches(k, keymap.Global.Quit):
		return m.requestQuit()
	case key.Matches(k, keymap.Global.Help):
		m.show(widgets.NewInspector("help", "Key bindings", " "+strings.ReplaceAll(strings.TrimRight(keymap.HelpText(), "\n"), "\n", "\n "), false))
		return nil
	case key.Matches(k, keymap.Global.Next):
		m.setFocus((m.focus + 1) % 3)
		return nil
	case key.Matches(k, keymap.Global.Prev):
		m.setFocus((m.focus + 2) % 3)
		return nil
	case key.Matches(k, keymap.Global.Pane1):
		m.setFocus(paneExplorer)
		return nil
	case key.Matches(k, keymap.Global.Pane2):
		m.setFocus(paneData)
		return nil
	case key.Matches(k, keymap.Global.Pane3):
		m.setFocus(paneQuery)
		return nil
	case key.Matches(k, keymap.Global.Connections):
		return m.requestDisconnect()
	case key.Matches(k, keymap.Global.Tx):
		return m.txMenu()
	case key.Matches(k, keymap.Global.Commit):
		if m.inTx() {
			return m.txCmd("commit", "")
		}
		m.setStatus("no open transaction (press T to start one)", false)
		return nil
	}

	switch m.focus {
	case paneExplorer:
		return m.explorerKey(k)
	case paneData:
		return m.dataKey(k)
	}
	return nil
}

// explorerFilterKey edits the name filter; pressing enter also searches the
// whole database when the driver can (redis keys that are not expanded yet).
func (m *Model) explorerFilterKey(k tea.KeyMsg) tea.Cmd {
	enter := k.String() == "enter"
	cmd := m.ex.filterKey(k)
	if s, ok := m.drv.(driver.Searcher); ok && enter && m.ex.filter != "" {
		m.ex.searching = true
		text := m.ex.filter
		return tea.Batch(cmd, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			nodes, err := s.Search(ctx, text, 1000)
			return searchMsg{text: text, nodes: nodes, err: err}
		})
	}
	return cmd
}

func (m *Model) explorerKey(k tea.KeyMsg) tea.Cmd {
	act, n := m.ex.handleKey(k)
	switch act {
	case exOpen:
		return m.openNode(n)
	case exLoad:
		return m.treeCmd(n)
	case exReload:
		n.children = nil
		m.ex.flatten()
		return m.treeCmd(n)
	case exReloadAll:
		m.ex.loadingRoot, m.ex.rootsLoaded = true, false
		return m.treeCmd(nil)
	}
	return nil
}

func (m *Model) queryKey(k tea.KeyMsg) tea.Cmd {
	q := m.query
	t := q.tab()
	switch {
	case key.Matches(k, keymap.Query.Leave):
		m.setFocus(paneData)
		return nil
	case key.Matches(k, keymap.Query.Run):
		return m.runQuery(false)
	case key.Matches(k, keymap.Query.Explain):
		return m.runQuery(true)
	case key.Matches(k, keymap.Query.NewTab):
		q.add()
		m.setFocus(paneQuery)
		return nil
	case key.Matches(k, keymap.Query.CloseTab):
		q.closeCur()
		m.setFocus(paneQuery)
		return nil
	case key.Matches(k, keymap.Query.PrevTab):
		q.switchTo(q.cur - 1)
		m.setFocus(paneQuery)
		return nil
	case key.Matches(k, keymap.Query.NextTab):
		q.switchTo(q.cur + 1)
		m.setFocus(paneQuery)
		return nil
	case key.Matches(k, keymap.Query.HistPrev):
		t.histMove(-1)
		return nil
	case key.Matches(k, keymap.Query.HistNext):
		t.histMove(1)
		return nil
	case key.Matches(k, keymap.Query.Complete):
		if !t.complete() {
			t.ta.InsertString("  ")
		}
		return nil
	}
	var cmd tea.Cmd
	t.ta, cmd = t.ta.Update(k)
	h, _ := m.drv.(driver.Hinter)
	t.refreshHints(h)
	return cmd
}

func (m *Model) dataInputKey(k tea.KeyMsg) tea.Cmd {
	d := m.data
	var cmd tea.Cmd
	if d.editing {
		switch k.String() {
		case "enter":
			return m.commitEdit()
		case "esc":
			d.editing = false
		case "ctrl+n":
			d.editNull = !d.editNull
		default:
			d.editNull = false
			d.edit, cmd = d.edit.Update(k)
		}
		return cmd
	}
	switch k.String() {
	case "enter":
		d.where = strings.TrimSpace(d.filter.Value())
		d.filtering = false
		d.filter.Blur()
		return m.loadData(false)
	case "esc":
		d.filtering = false
		d.filter.Blur()
		d.filter.SetValue(d.where)
	default:
		d.filter, cmd = d.filter.Update(k)
	}
	return cmd
}

func (m *Model) dataKey(k tea.KeyMsg) tea.Cmd {
	d := m.data
	g := d.grid
	if key.Matches(k, keymap.Scroll.Bottom) && d.node != nil && d.more {
		m.gotoEnd = true
		m.setStatus("loading to the last row… (ctrl+c cancels)", false)
		if d.loading {
			return nil
		}
		return m.loadData(true)
	}
	if g.S.HandleKey(k) {
		return m.maybeLoadMore()
	}
	switch {
	case key.Matches(k, keymap.Data.Left):
		g.MoveCol(-1)
	case key.Matches(k, keymap.Data.Right):
		g.MoveCol(1)
	case key.Matches(k, keymap.Data.FirstCol):
		g.FirstCol()
	case key.Matches(k, keymap.Data.LastCol):
		g.LastCol()
	case key.Matches(k, keymap.Data.Freeze):
		g.Freeze = !g.Freeze
	case key.Matches(k, keymap.Data.Sort), key.Matches(k, keymap.Data.ClearSort):
		if len(g.Cols) == 0 {
			return nil
		}
		if key.Matches(k, keymap.Data.ClearSort) {
			d.sortCol, d.sortDesc, g.SortCol = "", false, ""
		} else {
			d.cycleSort()
		}
		if d.isQuery {
			d.sortLocal()
			return nil
		}
		return m.loadData(false)
	case key.Matches(k, keymap.Data.Filter):
		if d.node == nil {
			m.setStatus("filtering works on tables and keys — query results can be sorted with s", false)
			return nil
		}
		d.filtering = true
		d.filter.SetValue(d.where)
		d.filter.CursorEnd()
		d.filter.Focus()
	case key.Matches(k, keymap.Data.Refresh):
		if d.node == nil {
			m.setStatus("re-run the query (ctrl+r) to refresh its result", false)
			return nil
		}
		return m.reloadData()
	case key.Matches(k, keymap.Data.Inspect):
		if c, ok := g.CurrentCell(); ok {
			m.show(widgets.NewInspector("inspect", g.Cols[g.Col].Name, c.String(), d.canEdit && !d.isQuery))
		}
	case key.Matches(k, keymap.Data.Edit):
		m.startEdit()
	case key.Matches(k, keymap.Data.Add):
		m.startInsert()
	case key.Matches(k, keymap.Data.Delete):
		m.startDelete()
	case key.Matches(k, keymap.Data.Copy):
		if c, ok := g.CurrentCell(); ok {
			m.copyText(c.String())
		}
	case key.Matches(k, keymap.Data.CopyRow):
		if row, ok := g.Current(); ok {
			parts := make([]string, len(row))
			for i, c := range row {
				parts[i] = c.String()
			}
			m.copyText(strings.Join(parts, "\t"))
		}
	}
	return nil
}

// ---- editing -----------------------------------------------------------------

// editable checks that the open node can be modified, setting a status if not.
func (m *Model) editable() (driver.Editor, bool) {
	d := m.data
	switch {
	case d.node == nil:
		m.setStatus("query results are read-only — open a table to edit it", false)
	case !d.checked:
		m.setStatus("still checking whether this table can be edited…", false)
	case !d.canEdit:
		m.setStatus("read-only: "+d.why, true)
	default:
		if ed, ok := m.drv.(driver.Editor); ok {
			return ed, true
		}
		m.setStatus("this engine is read-only here", true)
	}
	return nil, false
}

func (m *Model) startEdit() {
	if _, ok := m.editable(); !ok {
		return
	}
	d := m.data
	c, ok := d.grid.CurrentCell()
	if !ok {
		return
	}
	d.editing, d.editRow, d.editCol, d.editNull = true, d.grid.S.Cursor, d.grid.Col, c.Null
	d.edit.SetValue(c.V)
	d.edit.CursorEnd()
	d.edit.Focus()
	m.setFocus(paneData)
}

func (m *Model) commitEdit() tea.Cmd {
	d := m.data
	ed, ok := m.editable()
	d.editing = false
	if !ok || d.editRow >= len(d.grid.Rows) {
		return nil
	}
	cell := driver.Text(d.edit.Value())
	if d.editNull {
		cell = driver.NullCell()
	}
	old := d.grid.Rows[d.editRow][d.editCol]
	if old == cell {
		m.setStatus("unchanged", false)
		return nil
	}
	row := append([]driver.Cell(nil), d.grid.Rows[d.editRow]...)
	cols, node, ri, ci := d.grid.Cols, *d.node, d.editRow, d.editCol
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		show, err := ed.UpdateCell(ctx, node, cols, row, ci, cell)
		return editMsg{kind: "update", row: ri, col: ci, cell: cell, show: show, err: err}
	}
}

func (m *Model) startInsert() {
	if _, ok := m.editable(); !ok {
		return
	}
	var fields []widgets.FormField
	for _, c := range m.data.grid.Cols {
		if m.data.redis && c.Name == "index" {
			continue
		}
		fields = append(fields, widgets.FormField{Name: c.Name, Type: c.Type})
	}
	if len(fields) == 0 {
		m.setStatus("no columns known yet — reload and try again", true)
		return
	}
	m.show(widgets.NewForm("insert", "Add row · "+m.data.node.Name, fields))
}

func (m *Model) doInsert(vals map[string]driver.Cell) tea.Cmd {
	ed, ok := m.editable()
	if !ok {
		return nil
	}
	cols, node := m.data.grid.Cols, *m.data.node
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		show, err := ed.InsertRow(ctx, node, cols, vals)
		return editMsg{kind: "insert", show: show, err: err}
	}
}

func (m *Model) startDelete() {
	if _, ok := m.editable(); !ok {
		return
	}
	g := m.data.grid
	row, ok := g.Current()
	if !ok {
		return
	}
	lines := []string{"This permanently removes the selected row:", ""}
	for i, c := range g.Cols {
		if i >= 6 {
			lines = append(lines, "  …")
			break
		}
		lines = append(lines, fmt.Sprintf("  %s = %s", c.Name, row[i].String()))
	}
	if m.inTx() {
		lines = append(lines, "", "(inside a manual transaction — nothing is final until you commit)")
	}
	m.show(widgets.NewConfirm("delete", "Delete row?", lines...))
}

func (m *Model) doDelete() tea.Cmd {
	ed, ok := m.editable()
	if !ok {
		return nil
	}
	g := m.data.grid
	row, ok := g.Current()
	if !ok {
		return nil
	}
	row = append([]driver.Cell(nil), row...)
	cols, node, ri := g.Cols, *m.data.node, g.S.Cursor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		show, err := ed.DeleteRow(ctx, node, cols, row)
		return editMsg{kind: "delete", row: ri, show: show, err: err}
	}
}

func (m *Model) onEdit(msg editMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	suffix := ""
	if m.inTx() {
		suffix = "  (pending — ctrl+s to commit)"
	}
	m.setStatus("✔ "+widgets.Clean(msg.show)+suffix, false)
	g := m.data.grid
	switch msg.kind {
	case "update":
		if msg.row < len(g.Rows) {
			g.Rows[msg.row][msg.col] = msg.cell
		}
	case "delete":
		if msg.row < len(g.Rows) {
			rows := append(append([][]driver.Cell(nil), g.Rows[:msg.row]...), g.Rows[msg.row+1:]...)
			g.ReplaceRows(rows)
			if m.data.total > 0 {
				m.data.total--
			}
		}
	case "insert":
		m.onFirstLoad = func() {
			if !m.data.more {
				m.data.grid.S.Bottom()
			}
		}
		return m.loadData(false)
	}
	return nil
}
