package browser

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"dbdeck/internal/driver"
	"dbdeck/internal/keymap"
	"dbdeck/internal/theme"
	"dbdeck/internal/widgets"
)

// tnode is one node of the explorer tree with its load state.
type tnode struct {
	driver.Node
	depth    int
	parent   *tnode
	children []*tnode
	expanded bool
	loaded   bool
	loading  bool
	err      string
}

type exAction int

const (
	exNone   exAction = iota
	exOpen            // open the node in the data view
	exLoad            // load children of the node
	exReload          // clear and reload the node's children
	exReloadAll
)

type explorer struct {
	roots       []*tnode
	vis         []*tnode
	s           widgets.Scroller
	filter      string
	input       textinput.Model
	filtering   bool
	rootsLoaded bool
	loadingRoot bool
	rootErr     string
	byID        map[string]*tnode
	active      string // ID of the node shown in the data view

	results    []*tnode // flat search results (redis); nil when not searching
	searchText string
	searching  bool
}

func newExplorer() *explorer {
	ti := textinput.New()
	ti.Prompt = " / "
	ti.Placeholder = "filter loaded names"
	return &explorer{s: widgets.NewScroller(), input: ti, byID: map[string]*tnode{}}
}

func (e *explorer) selected() *tnode {
	if e.s.Cursor < 0 || e.s.Cursor >= len(e.vis) {
		return nil
	}
	return e.vis[e.s.Cursor]
}

func (e *explorer) setRoots(nodes []driver.Node) {
	e.roots = e.roots[:0]
	e.byID = map[string]*tnode{}
	for _, n := range nodes {
		t := &tnode{Node: n}
		e.roots = append(e.roots, t)
		e.byID[n.ID] = t
	}
	e.rootsLoaded, e.loadingRoot, e.rootErr = true, false, ""
	e.flatten()
}

func (e *explorer) setChildren(parentID string, nodes []driver.Node, err error) {
	p := e.byID[parentID]
	if p == nil {
		return
	}
	p.loading = false
	if err != nil {
		p.err = err.Error()
		p.expanded = false
		e.flatten()
		return
	}
	p.err = ""
	p.children = p.children[:0]
	for _, n := range nodes {
		t := &tnode{Node: n, depth: p.depth + 1, parent: p}
		p.children = append(p.children, t)
		e.byID[n.ID] = t
	}
	p.loaded, p.expanded = true, true
	e.flatten()
}

func (e *explorer) setResults(text string, nodes []driver.Node) {
	e.searching, e.searchText = false, text
	e.results = make([]*tnode, 0, len(nodes))
	for _, n := range nodes {
		e.results = append(e.results, &tnode{Node: n})
	}
	e.s.Select(0)
	e.flatten()
}

func (e *explorer) clearFilter() {
	e.filter, e.results, e.searchText, e.searching = "", nil, "", false
	e.flatten()
}

func (e *explorer) flatten() {
	selID := ""
	if s := e.selected(); s != nil {
		selID = s.ID
	}
	e.vis = e.vis[:0]
	if e.results != nil {
		e.vis = append(e.vis, e.results...)
		e.s.SetTotal(len(e.vis))
		return
	}
	f := strings.ToLower(e.filter)
	var walk func(ns []*tnode)
	walk = func(ns []*tnode) {
		for _, n := range ns {
			if f == "" {
				e.vis = append(e.vis, n)
				if n.expanded {
					walk(n.children)
				}
				continue
			}
			sub := e.matching(n, f)
			e.vis = append(e.vis, sub...)
		}
	}
	walk(e.roots)
	e.s.SetTotal(len(e.vis))
	if selID != "" {
		for i, n := range e.vis {
			if n.ID == selID {
				e.s.Select(i)
				break
			}
		}
	}
}

// matching returns n and its loaded descendants when n or any descendant matches f.
func (e *explorer) matching(n *tnode, f string) []*tnode {
	var below []*tnode
	for _, c := range n.children {
		below = append(below, e.matching(c, f)...)
	}
	if strings.Contains(strings.ToLower(n.Name), f) || len(below) > 0 {
		return append([]*tnode{n}, below...)
	}
	return nil
}

func (e *explorer) selectID(id string) {
	for i, n := range e.vis {
		if n.ID == id {
			e.s.Select(i)
			return
		}
	}
}

// handleKey applies a NORMAL-mode key and says what the browser must do.
func (e *explorer) handleKey(k tea.KeyMsg) (exAction, *tnode) {
	if e.s.HandleKey(k) {
		return exNone, nil
	}
	n := e.selected()
	if k.String() == "esc" && (e.filter != "" || e.results != nil) {
		e.clearFilter()
		return exNone, nil
	}
	switch {
	case key.Matches(k, keymap.Tree.Open):
		if n == nil {
			return exNone, nil
		}
		switch {
		case n.Openable():
			return exOpen, n
		case n.Expandable() && !n.loaded:
			n.loading, n.expanded = true, true
			return exLoad, n
		case n.Expandable():
			n.expanded = !n.expanded
			e.flatten()
		}
	case key.Matches(k, keymap.Tree.Collapse):
		if n == nil {
			return exNone, nil
		}
		if n.expanded {
			n.expanded = false
			e.flatten()
		} else if n.parent != nil {
			e.selectID(n.parent.ID)
		}
	case key.Matches(k, keymap.Tree.Filter):
		e.filtering = true
		e.input.SetValue(e.filter)
		e.input.Focus()
	case key.Matches(k, keymap.Tree.Refresh):
		if n == nil {
			return exNone, nil
		}
		if !n.Expandable() && n.parent != nil {
			n = n.parent
			e.selectID(n.ID)
		}
		if n.Expandable() {
			n.loaded, n.loading, n.expanded = false, true, true
			return exReload, n
		}
	case key.Matches(k, keymap.Tree.RefreshAll):
		return exReloadAll, nil
	}
	return exNone, nil
}

func (e *explorer) filterKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		e.filter = strings.TrimSpace(e.input.Value())
		e.filtering = false
		e.input.Blur()
		e.flatten()
		return nil
	case "esc":
		e.filtering = false
		e.input.Blur()
		e.clearFilter()
		return nil
	}
	var cmd tea.Cmd
	e.input, cmd = e.input.Update(k)
	e.filter = strings.TrimSpace(e.input.Value()) // live filtering
	e.flatten()
	return cmd
}

func (e *explorer) view(w, h int, focused bool, spin string) string {
	e.s.SetHeight(h - 1)
	lines := make([]string, 0, h)
	switch {
	case e.filtering:
		lines = append(lines, e.input.View())
	case e.searching:
		lines = append(lines, theme.Warn.Render(" searching “"+e.filter+"” "+spin))
	case e.results != nil:
		lines = append(lines, theme.Warn.Render(fmt.Sprintf(" “%s” · %d keys", e.searchText, len(e.results)))+theme.Dim.Render("  esc clears"))
	case e.filter != "":
		lines = append(lines, theme.Warn.Render(" /"+e.filter)+theme.Dim.Render("  esc clears"))
	default:
		lines = append(lines, theme.Dim.Render(" / filter · ? help"))
	}
	switch {
	case e.loadingRoot:
		lines = append(lines, theme.Dim.Render(" "+spin+" loading…"))
	case e.rootErr != "":
		lines = append(lines, theme.Err.Render(" "+widgets.Clean(e.rootErr)))
	case e.rootsLoaded && len(e.vis) == 0:
		msg := " (empty)"
		if e.results != nil {
			msg = " no key matches"
		} else if e.filter != "" {
			msg = " no loaded names match"
		}
		lines = append(lines, theme.Dim.Render(msg))
	}
	if len(lines) > 1 {
		return strings.Join(lines, "\n")
	}
	avail := w - 1
	bar := widgets.Scrollbar(e.s.Offset, e.s.Total, h-1)
	start, end := e.s.Range()
	for i := start; i < end; i++ {
		n := e.vis[i]
		arrow := "  "
		if n.Expandable() {
			arrow = "▸ "
			if n.expanded {
				arrow = "▾ "
			}
		}
		left := strings.Repeat("  ", n.depth) + " " + arrow + widgets.Clean(n.Name)
		right := n.Detail
		switch {
		case n.loading:
			right = spin
		case n.err != "":
			right = "! " + n.err
		}
		if right != "" {
			if pad := avail - runewidth.StringWidth(left) - runewidth.StringWidth(right) - 1; pad >= 1 {
				left = left + strings.Repeat(" ", pad) + right
			} else {
				left = runewidth.Truncate(left, avail, "…")
			}
		}
		text := widgets.Fit(left, avail)
		switch {
		case i == e.s.Cursor && focused:
			text = theme.CellSel.Render(text)
		case i == e.s.Cursor:
			text = theme.RowSel.Render(text)
		case n.ID == e.active:
			text = theme.Title.Render(text)
		case n.err != "":
			text = theme.Err.Render(text)
		case n.Kind == driver.KindView:
			text = theme.Warn.Render(text)
		case !n.Expandable():
			// leaf keeps default colour
		default:
			text = theme.Header.Render(text)
		}
		lines = append(lines, text+theme.Dim.Render(bar[i-start]))
	}
	return strings.Join(lines, "\n")
}
