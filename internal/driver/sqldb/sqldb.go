// Package sqldb implements driver.Driver (+Editor, Transactor, Hinter) once
// on top of database/sql. Engines only supply a Dialect: identifier quoting,
// placeholders, and the catalog queries for the tree and primary keys.
package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"dbdeck/internal/driver"
)

const (
	countTimeout = 4 * time.Second
	// MaxQueryRows caps rows materialised for an ad-hoc query.
	MaxQueryRows = 10000
)

// Queryer is satisfied by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Dialect is the engine-specific part.
type Dialect interface {
	Quote(ident string) string
	Placeholder(i int) string // 1-based
	Roots(ctx context.Context, q Queryer) ([]driver.Node, error)
	Children(ctx context.Context, q Queryer, parent driver.Node) ([]driver.Node, error)
	TableRef(n driver.Node) string
	PrimaryKey(ctx context.Context, q Queryer, n driver.Node) ([]string, error)
	DefaultValues(ref string) string
	ExplainPrefix() string
}

// DB is a SQL connection with optional manual-transaction state.
type DB struct {
	engine string
	title  string
	db     *sql.DB
	d      Dialect

	sem   chan struct{} // serialises all operations; honours ctx while waiting
	tx    *sql.Tx
	txLog []string

	pk    map[string][]string
	names map[string]struct{} // table names seen, for completion
}

// New wraps an opened *sql.DB.
func New(engine, title string, db *sql.DB, d Dialect) *DB {
	return &DB{
		engine: engine, title: title, db: db, d: d,
		sem:   make(chan struct{}, 1),
		pk:    map[string][]string{},
		names: map[string]struct{}{},
	}
}

func (s *DB) Engine() string { return s.engine }
func (s *DB) Title() string  { return s.title }

func (s *DB) ExplainPrefix() string { return s.d.ExplainPrefix() }

func (s *DB) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *DB) release() { <-s.sem }

func (s *DB) q() Queryer {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// guarded runs fn; inside a manual transaction it is wrapped in a savepoint so
// a failed statement does not poison the whole transaction. Caller holds the lock.
func (s *DB) guarded(ctx context.Context, fn func(q Queryer) error) error {
	if s.tx == nil {
		return fn(s.db)
	}
	if _, err := s.tx.ExecContext(ctx, "SAVEPOINT dbdeck_sp"); err != nil {
		return err
	}
	if err := fn(s.tx); err != nil {
		_, _ = s.tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT dbdeck_sp")
		return err
	}
	_, err := s.tx.ExecContext(ctx, "RELEASE SAVEPOINT dbdeck_sp")
	return err
}

func (s *DB) Close() error {
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	return s.db.Close()
}

// ---- tree ------------------------------------------------------------------

func (s *DB) Tree(ctx context.Context, parent *Node) ([]driver.Node, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	var nodes []driver.Node
	err := s.guarded(ctx, func(q Queryer) error {
		var err error
		if parent == nil {
			nodes, err = s.d.Roots(ctx, q)
		} else {
			nodes, err = s.d.Children(ctx, q, *parent)
		}
		return err
	})
	if err == nil {
		for _, n := range nodes {
			if n.Openable() {
				s.names[n.Name] = struct{}{}
			}
		}
	}
	return nodes, err
}

// Node is re-exported so dialects read naturally.
type Node = driver.Node

// ---- browse / exec ----------------------------------------------------------

func (s *DB) Browse(ctx context.Context, n driver.Node, o driver.BrowseOptions) (driver.Result, error) {
	if err := s.acquire(ctx); err != nil {
		return driver.Result{}, err
	}
	defer s.release()

	ref := s.d.TableRef(n)
	where := ""
	if w := strings.TrimSpace(o.Where); w != "" {
		where = " WHERE " + w // the user's own database; raw expression by design
	}
	order := ""
	if o.SortCol != "" {
		dir := "ASC"
		if o.SortDesc {
			dir = "DESC"
		}
		order = " ORDER BY " + s.d.Quote(o.SortCol) + " " + dir
	}
	if o.Limit <= 0 {
		o.Limit = 200
	}

	start := time.Now()
	var res driver.Result
	err := s.guarded(ctx, func(q Queryer) error {
		total := -1
		if o.Offset == 0 {
			cctx, cancel := context.WithTimeout(ctx, countTimeout)
			var t int
			err := q.QueryRowContext(cctx, "SELECT COUNT(*) FROM "+ref+where).Scan(&t)
			cancel()
			switch {
			case err == nil:
				total = t
			case ctx.Err() != nil:
				return ctx.Err()
			case errors.Is(err, context.DeadlineExceeded) || cctx.Err() != nil:
				total = -1 // huge table: count is too slow, stay unknown
			default:
				return err
			}
		}
		rows, err := q.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", ref, where, order, o.Limit+1, o.Offset))
		if err != nil {
			return err
		}
		defer rows.Close()
		r, err := readRows(rows, o.Limit)
		if err != nil {
			return err
		}
		r.Total = total
		res = r
		return nil
	})
	res.Elapsed = time.Since(start)
	return res, err
}

func (s *DB) Exec(ctx context.Context, input string) (driver.Result, error) {
	stmts := driver.SplitStatements(input)
	if len(stmts) == 0 {
		return driver.Result{}, errors.New("nothing to run")
	}
	start := time.Now()
	var last driver.Result
	for i, st := range stmts {
		r, err := s.execOne(ctx, st)
		if err != nil {
			if len(stmts) > 1 {
				return last, fmt.Errorf("statement %d of %d: %w", i+1, len(stmts), err)
			}
			return last, err
		}
		last = r
	}
	last.Elapsed = time.Since(start)
	if len(stmts) > 1 {
		last.Message = fmt.Sprintf("%d statements ok · last: %s", len(stmts), last.Message)
	}
	return last, nil
}

func (s *DB) execOne(ctx context.Context, stmt string) (driver.Result, error) {
	switch strings.ToUpper(strings.TrimSpace(stmt)) {
	case "BEGIN", "START TRANSACTION", "BEGIN TRANSACTION":
		return driver.Result{Total: -1, Message: "transaction started (manual mode)"}, s.Begin(ctx)
	case "COMMIT", "END":
		return driver.Result{Total: -1, Message: "committed"}, s.Commit(ctx)
	case "ROLLBACK":
		return driver.Result{Total: -1, Message: "rolled back"}, s.Rollback(ctx)
	}

	if err := s.acquire(ctx); err != nil {
		return driver.Result{}, err
	}
	defer s.release()

	var res driver.Result
	start := time.Now()
	err := s.guarded(ctx, func(q Queryer) error {
		if returnsRows(stmt) {
			rows, err := q.QueryContext(ctx, stmt)
			if err != nil {
				return err
			}
			defer rows.Close()
			res, err = readRows(rows, MaxQueryRows)
			if err == nil {
				res.Message = fmt.Sprintf("%d rows", len(res.Rows))
				if res.Truncated {
					res.Message += fmt.Sprintf(" (truncated at %d)", MaxQueryRows)
				}
			}
			return err
		}
		r, err := q.ExecContext(ctx, stmt)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		res = driver.Result{Total: -1, Affected: n, Message: fmt.Sprintf("%d rows affected", n)}
		return nil
	})
	res.Elapsed = time.Since(start)
	if err == nil && s.tx != nil && !returnsRows(stmt) {
		s.txLog = append(s.txLog, oneLine(stmt))
	}
	return res, err
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 160 {
		s = string([]rune(s)[:160]) + "…"
	}
	return s
}

var rowKeywords = map[string]bool{
	"SELECT": true, "WITH": true, "SHOW": true, "EXPLAIN": true, "PRAGMA": true,
	"VALUES": true, "DESCRIBE": true, "DESC": true, "TABLE": true,
}

func returnsRows(stmt string) bool {
	f := strings.Fields(stmt)
	if len(f) == 0 {
		return false
	}
	first := strings.ToUpper(strings.TrimLeft(f[0], "("))
	if rowKeywords[first] {
		return true
	}
	return strings.Contains(strings.ToUpper(stmt), " RETURNING ")
}

// ---- transactions -----------------------------------------------------------

func (s *DB) Begin(ctx context.Context) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if s.tx != nil {
		return errors.New("a transaction is already open")
	}
	// The tx must outlive the request context that started it.
	tx, err := s.db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	s.tx, s.txLog = tx, nil
	return nil
}

func (s *DB) Commit(ctx context.Context) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if s.tx == nil {
		return errors.New("no open transaction")
	}
	err := s.tx.Commit()
	s.tx, s.txLog = nil, nil
	return err
}

func (s *DB) Rollback(ctx context.Context) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if s.tx == nil {
		return errors.New("no open transaction")
	}
	err := s.tx.Rollback()
	s.tx, s.txLog = nil, nil
	return err
}

// InTx and TxLog are called from the UI goroutine while no operation runs.
func (s *DB) InTx() bool { return s.tx != nil }
func (s *DB) TxLog() []string {
	return append([]string(nil), s.txLog...)
}

// ---- editing ---------------------------------------------------------------

func (s *DB) primaryKey(ctx context.Context, n driver.Node) ([]string, error) {
	if pk, ok := s.pk[n.ID]; ok {
		return pk, nil
	}
	var pk []string
	err := s.guarded(ctx, func(q Queryer) error {
		var err error
		pk, err = s.d.PrimaryKey(ctx, q, n)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.pk[n.ID] = pk
	return pk, nil
}

func (s *DB) CanEdit(ctx context.Context, n driver.Node) (bool, string) {
	if n.Kind != driver.KindTable {
		return false, "views are read-only"
	}
	if err := s.acquire(ctx); err != nil {
		return false, err.Error()
	}
	defer s.release()
	pk, err := s.primaryKey(ctx, n)
	if err != nil {
		return false, err.Error()
	}
	if len(pk) == 0 {
		return false, "table has no primary key — editing is disabled to avoid changing the wrong rows"
	}
	return true, ""
}

// whereByPK builds "pk1 = ? AND pk2 IS NULL ..." for a row, starting at placeholder `next`.
func (s *DB) whereByPK(pk []string, cols []driver.Column, row []driver.Cell, next int) (execSQL, show string, args []any, err error) {
	var ex, sh []string
	for _, name := range pk {
		idx := -1
		for i, c := range cols {
			if c.Name == name {
				idx = i
				break
			}
		}
		if idx < 0 || idx >= len(row) {
			return "", "", nil, fmt.Errorf("primary key column %q is not in the result", name)
		}
		v := row[idx]
		if v.Null {
			ex = append(ex, s.d.Quote(name)+" IS NULL")
			sh = append(sh, s.d.Quote(name)+" IS NULL")
			continue
		}
		ex = append(ex, s.d.Quote(name)+" = "+s.d.Placeholder(next))
		sh = append(sh, s.d.Quote(name)+" = "+lit(v))
		args = append(args, v.V)
		next++
	}
	return strings.Join(ex, " AND "), strings.Join(sh, " AND "), args, nil
}

func lit(c driver.Cell) string {
	if c.Null {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(c.V, "'", "''") + "'"
}

func arg(c driver.Cell) any {
	if c.Null {
		return nil
	}
	return c.V
}

func (s *DB) UpdateCell(ctx context.Context, n driver.Node, cols []driver.Column, row []driver.Cell, col int, v driver.Cell) (string, error) {
	if col < 0 || col >= len(cols) {
		return "", errors.New("no column selected")
	}
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	pk, err := s.primaryKey(ctx, n)
	if err != nil {
		return "", err
	}
	if len(pk) == 0 {
		return "", errors.New("table has no primary key")
	}
	ref := s.d.TableRef(n)
	wex, wsh, wargs, err := s.whereByPK(pk, cols, row, 2)
	if err != nil {
		return "", err
	}
	ex := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s", ref, s.d.Quote(cols[col].Name), s.d.Placeholder(1), wex)
	show := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s", ref, s.d.Quote(cols[col].Name), lit(v), wsh)
	return s.write(ctx, ex, show, append([]any{arg(v)}, wargs...))
}

func (s *DB) DeleteRow(ctx context.Context, n driver.Node, cols []driver.Column, row []driver.Cell) (string, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	pk, err := s.primaryKey(ctx, n)
	if err != nil {
		return "", err
	}
	if len(pk) == 0 {
		return "", errors.New("table has no primary key")
	}
	ref := s.d.TableRef(n)
	wex, wsh, wargs, err := s.whereByPK(pk, cols, row, 1)
	if err != nil {
		return "", err
	}
	return s.write(ctx, "DELETE FROM "+ref+" WHERE "+wex, "DELETE FROM "+ref+" WHERE "+wsh, wargs)
}

func (s *DB) InsertRow(ctx context.Context, n driver.Node, cols []driver.Column, vals map[string]driver.Cell) (string, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	ref := s.d.TableRef(n)
	var names, phs, shows []string
	var args []any
	i := 1
	for _, c := range cols { // keep table column order
		v, ok := vals[c.Name]
		if !ok {
			continue
		}
		names = append(names, s.d.Quote(c.Name))
		phs = append(phs, s.d.Placeholder(i))
		shows = append(shows, lit(v))
		args = append(args, arg(v))
		i++
	}
	if len(names) == 0 {
		sqlText := s.d.DefaultValues(ref)
		return s.write(ctx, sqlText, sqlText, nil)
	}
	ex := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", ref, strings.Join(names, ", "), strings.Join(phs, ", "))
	show := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", ref, strings.Join(names, ", "), strings.Join(shows, ", "))
	return s.write(ctx, ex, show, args)
}

// write runs one modifying statement that must affect exactly one row.
// Caller holds the lock. Outside a manual transaction it uses a short private one.
func (s *DB) write(ctx context.Context, execSQL, show string, args []any) (string, error) {
	check := func(q Queryer) error {
		res, err := q.ExecContext(ctx, execSQL, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n != 1 {
			return fmt.Errorf("expected to change exactly 1 row but %d matched — nothing was applied", n)
		}
		return nil
	}
	if s.tx == nil {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return "", err
		}
		if err := check(tx); err != nil {
			_ = tx.Rollback()
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return show, nil
	}
	if err := s.guarded(ctx, check); err != nil {
		return "", err
	}
	s.txLog = append(s.txLog, show)
	return show, nil
}

// ---- hints -----------------------------------------------------------------

var sqlKeywords = []string{
	"SELECT", "FROM", "WHERE", "GROUP BY", "ORDER BY", "HAVING", "LIMIT", "OFFSET", "JOIN", "LEFT JOIN",
	"INNER JOIN", "ON", "AS", "AND", "OR", "NOT", "NULL", "IS NULL", "IS NOT NULL", "IN", "LIKE", "BETWEEN",
	"INSERT INTO", "VALUES", "UPDATE", "SET", "DELETE FROM", "CREATE TABLE", "ALTER TABLE", "DROP TABLE",
	"CREATE INDEX", "DISTINCT", "COUNT", "SUM", "AVG", "MIN", "MAX", "UNION", "EXISTS", "CASE", "WHEN",
	"THEN", "ELSE", "END", "ASC", "DESC", "EXPLAIN", "BEGIN", "COMMIT", "ROLLBACK", "RETURNING",
}

// Hints returns keywords and known table names starting with prefix (case-insensitive).
func (s *DB) Hints(prefix string) []string {
	if prefix == "" {
		return nil
	}
	up := strings.ToUpper(prefix)
	var out []string
	for n := range s.names {
		if strings.HasPrefix(strings.ToUpper(n), up) && !strings.EqualFold(n, prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	for _, k := range sqlKeywords {
		if strings.HasPrefix(k, up) && k != up {
			out = append(out, k)
		}
	}
	return out
}
