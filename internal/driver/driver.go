// Package driver defines the engine-agnostic interfaces the UI talks to.
// Engines register a Factory; the UI only ever sees Driver and the optional
// capability interfaces (Editor, Transactor, Hinter).
package driver

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"dbdeck/internal/config"
)

// Kind classifies a tree node.
type Kind string

const (
	KindDatabase Kind = "database"
	KindSchema   Kind = "schema"
	KindTable    Kind = "table"
	KindView     Kind = "view"
	KindFolder   Kind = "folder"
	KindKey      Kind = "key"
)

// Node is one entry of the explorer tree.
type Node struct {
	ID     string   // unique and stable within one connection
	Name   string   // display name
	Kind   Kind     // what it is
	Path   []string // driver-specific address (schema+table, db+key, ...)
	Detail string   // short extra info (redis type, key count, ...)
}

// Expandable reports whether the node has children.
func (n Node) Expandable() bool {
	return n.Kind == KindDatabase || n.Kind == KindSchema || n.Kind == KindFolder
}

// Openable reports whether the node can be shown in the data view.
func (n Node) Openable() bool {
	return n.Kind == KindTable || n.Kind == KindView || n.Kind == KindKey
}

// Column describes one result column.
type Column struct {
	Name string
	Type string
}

// Cell is one value. Null distinguishes SQL NULL from the string "NULL".
type Cell struct {
	V    string
	Null bool
}

func Text(s string) Cell { return Cell{V: s} }
func NullCell() Cell     { return Cell{Null: true} }

// String is the display form of the cell.
func (c Cell) String() string {
	if c.Null {
		return "NULL"
	}
	return c.V
}

// BrowseOptions describes one chunk of a table browse.
type BrowseOptions struct {
	Where    string // raw expression typed by the user (no WHERE keyword)
	SortCol  string
	SortDesc bool
	Offset   int
	Limit    int
}

// Result is a materialised, string-rendered result set.
type Result struct {
	Columns   []Column
	Rows      [][]Cell
	Total     int // total matching rows for a browse; -1 when unknown
	Affected  int64
	Elapsed   time.Duration
	Truncated bool   // more rows exist than were returned
	Meta      string // one-line info (redis: TYPE/TTL/LEN)
	Message   string // human message for statements without rows
}

// Driver is the minimum every engine implements.
type Driver interface {
	Engine() string
	Title() string
	// Tree lists the children of parent (nil = root).
	Tree(ctx context.Context, parent *Node) ([]Node, error)
	// Browse returns one chunk of a table / view / key.
	Browse(ctx context.Context, n Node, o BrowseOptions) (Result, error)
	// Exec runs user input: SQL statements, or one redis command per line.
	Exec(ctx context.Context, input string) (Result, error)
	Close() error
}

// Editor is implemented by drivers that can modify data.
type Editor interface {
	CanEdit(ctx context.Context, n Node) (bool, string)
	UpdateCell(ctx context.Context, n Node, cols []Column, row []Cell, col int, v Cell) (string, error)
	DeleteRow(ctx context.Context, n Node, cols []Column, row []Cell) (string, error)
	// InsertRow inserts using only the columns present in vals (others use defaults).
	InsertRow(ctx context.Context, n Node, cols []Column, vals map[string]Cell) (string, error)
}

// Transactor is implemented by drivers that support manual transactions.
type Transactor interface {
	Begin(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	InTx() bool
	TxLog() []string
}

// Hinter provides completion candidates for the query editor.
type Hinter interface {
	Hints(prefix string) []string
}

// Searcher finds nodes by name across everything the driver knows about,
// including parts of the tree that are not expanded yet (redis keys).
type Searcher interface {
	Search(ctx context.Context, text string, limit int) ([]Node, error)
}

// Explainer wraps a statement so it returns the query plan.
type Explainer interface {
	ExplainPrefix() string
}

// Factory opens a connection for a profile.
type Factory func(ctx context.Context, p config.Profile) (Driver, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes an engine available. Called from engine packages' init().
func Register(engine string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[engine] = f
}

// Engines returns the registered engine names.
func Engines() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Open validates the profile and connects, with a timeout.
func Open(ctx context.Context, p config.Profile) (Driver, error) {
	p = p.WithDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	mu.RLock()
	f, ok := registry[p.Engine]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("engine %q is not available in this build", p.Engine)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return f(ctx, p)
}
