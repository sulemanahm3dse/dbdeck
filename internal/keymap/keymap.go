// Package keymap is the single place key bindings are defined. Handlers match
// against these, and the help overlay and status-bar hints are generated from them.
package keymap

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

func bind(keys []string, helpKey, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(helpKey, desc))
}

var Scroll = struct{ Up, Down, HalfUp, HalfDown, PageUp, PageDown, Top, Bottom key.Binding }{
	Up:       bind([]string{"k", "up"}, "k/↑", "up"),
	Down:     bind([]string{"j", "down"}, "j/↓", "down"),
	HalfUp:   bind([]string{"ctrl+u"}, "ctrl+u", "half page up"),
	HalfDown: bind([]string{"ctrl+d"}, "ctrl+d", "half page down"),
	PageUp:   bind([]string{"ctrl+b", "pgup"}, "ctrl+b/PgUp", "page up"),
	PageDown: bind([]string{"ctrl+f", "pgdown"}, "ctrl+f/PgDn", "page down"),
	Top:      bind([]string{"g", "home"}, "g/Home", "top"),
	Bottom:   bind([]string{"G", "end"}, "G/End", "bottom"),
}

var Global = struct {
	Quit, Help, Next, Prev, Pane1, Pane2, Pane3, Connections, Tx, Commit key.Binding
}{
	Quit:        bind([]string{"q"}, "q", "quit"),
	Help:        bind([]string{"?"}, "?", "this help"),
	Next:        bind([]string{"tab"}, "tab", "next pane"),
	Prev:        bind([]string{"shift+tab"}, "shift+tab", "previous pane"),
	Pane1:       bind([]string{"alt+1"}, "alt+1", "focus explorer"),
	Pane2:       bind([]string{"alt+2"}, "alt+2", "focus data"),
	Pane3:       bind([]string{"alt+3", "i", ":"}, "alt+3/i", "focus query editor"),
	Connections: bind([]string{"ctrl+o"}, "ctrl+o", "back to connections"),
	Tx:          bind([]string{"T"}, "T", "transaction menu"),
	Commit:      bind([]string{"ctrl+s"}, "ctrl+s", "commit transaction"),
}

var Tree = struct{ Open, Collapse, Filter, Refresh, RefreshAll key.Binding }{
	Open:       bind([]string{"enter", "l", "right"}, "enter/l", "open / expand"),
	Collapse:   bind([]string{"h", "left"}, "h/←", "collapse / parent"),
	Filter:     bind([]string{"/"}, "/", "filter names"),
	Refresh:    bind([]string{"r"}, "r", "reload node"),
	RefreshAll: bind([]string{"R"}, "R", "reload everything"),
}

var Data = struct {
	Left, Right, FirstCol, LastCol, Sort, ClearSort, Filter, Refresh key.Binding
	Inspect, Edit, Add, Delete, Copy, CopyRow, Freeze                key.Binding
}{
	Left:      bind([]string{"h", "left"}, "h/←", "column left"),
	Right:     bind([]string{"l", "right"}, "l/→", "column right"),
	FirstCol:  bind([]string{"0"}, "0", "first column"),
	LastCol:   bind([]string{"$"}, "$", "last column"),
	Sort:      bind([]string{"s"}, "s", "sort by column (asc → desc → off)"),
	ClearSort: bind([]string{"S"}, "S", "clear sort"),
	Filter:    bind([]string{"/"}, "/", "filter (SQL: WHERE · redis: contains)"),
	Refresh:   bind([]string{"r"}, "r", "reload"),
	Inspect:   bind([]string{"enter"}, "enter", "inspect cell"),
	Edit:      bind([]string{"e"}, "e", "edit cell"),
	Add:       bind([]string{"a"}, "a", "add row"),
	Delete:    bind([]string{"x"}, "x", "delete row"),
	Copy:      bind([]string{"y"}, "y", "copy cell"),
	CopyRow:   bind([]string{"Y"}, "Y", "copy row (TSV)"),
	Freeze:    bind([]string{"f"}, "f", "freeze first column"),
}

var Query = struct {
	Run, Explain, NewTab, CloseTab, PrevTab, NextTab, HistPrev, HistNext, Complete, Leave key.Binding
}{
	Run:      bind([]string{"ctrl+r"}, "ctrl+r", "run (SQL: all statements · redis: current line)"),
	Explain:  bind([]string{"ctrl+e"}, "ctrl+e", "explain first statement"),
	NewTab:   bind([]string{"ctrl+t"}, "ctrl+t", "new query tab"),
	CloseTab: bind([]string{"ctrl+w"}, "ctrl+w", "close query tab"),
	PrevTab:  bind([]string{"alt+left"}, "alt+←", "previous tab"),
	NextTab:  bind([]string{"alt+right"}, "alt+→", "next tab"),
	HistPrev: bind([]string{"alt+up"}, "alt+↑", "previous statement in history"),
	HistNext: bind([]string{"alt+down"}, "alt+↓", "next statement in history"),
	Complete: bind([]string{"tab"}, "tab", "complete word (or indent)"),
	Leave:    bind([]string{"esc"}, "esc", "back to data pane"),
}

func section(b *strings.Builder, title string, bs ...key.Binding) {
	fmt.Fprintf(b, "%s\n", title)
	for _, k := range bs {
		h := k.Help()
		fmt.Fprintf(b, "  %-16s %s\n", h.Key, h.Desc)
	}
	b.WriteString("\n")
}

// HelpText renders every binding, grouped by pane.
func HelpText() string {
	var b strings.Builder
	section(&b, "Everywhere", Global.Help, Global.Next, Global.Prev, Global.Pane1, Global.Pane2, Global.Pane3,
		Global.Tx, Global.Commit, Global.Connections, Global.Quit)
	section(&b, "Scrolling (explorer, data, inspector)", Scroll.Down, Scroll.Up, Scroll.HalfDown, Scroll.HalfUp,
		Scroll.PageDown, Scroll.PageUp, Scroll.Top, Scroll.Bottom)
	b.WriteString("  mouse wheel      scroll the pane under the pointer (shift+wheel: sideways)\n")
	b.WriteString("  click            focus a pane / select a row or cell\n\n")
	section(&b, "Explorer", Tree.Open, Tree.Collapse, Tree.Filter, Tree.Refresh, Tree.RefreshAll)
	section(&b, "Data", Data.Left, Data.Right, Data.FirstCol, Data.LastCol, Data.Sort, Data.ClearSort, Data.Filter,
		Data.Refresh, Data.Inspect, Data.Edit, Data.Add, Data.Delete, Data.Copy, Data.CopyRow, Data.Freeze)
	section(&b, "Query editor", Query.Run, Query.Explain, Query.NewTab, Query.CloseTab, Query.PrevTab, Query.NextTab,
		Query.HistPrev, Query.HistNext, Query.Complete, Query.Leave)
	b.WriteString("Editing a cell: enter apply · esc cancel · ctrl+n toggle NULL\n")
	b.WriteString("Transactions: T opens the menu. Manual mode keeps every edit pending until you commit.\n")
	return b.String()
}
