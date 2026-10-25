package sqlite_test

import (
	"context"
	"strings"
	"testing"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/sqlite"
)

func open(t *testing.T) driver.Driver {
	t.Helper()
	d, err := driver.Open(context.Background(), config.Profile{Engine: config.EngineSQLite, Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	for _, s := range []string{
		`CREATE TABLE t_order (id INTEGER PRIMARY KEY, status TEXT, amount REAL, note TEXT)`,
		`INSERT INTO t_order (status, amount, note)
		 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 450)
		 SELECT CASE i % 3 WHEN 0 THEN 'done' ELSE 'pending' END, i * 1.5, NULL FROM n`,
		`CREATE TABLE nopk (a TEXT)`,
		`CREATE VIEW v_done AS SELECT * FROM t_order WHERE status = 'done'`,
	} {
		if _, err := d.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return d
}

func node(d driver.Driver, t *testing.T, name string) driver.Node {
	t.Helper()
	nodes, err := d.Tree(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("node %s not found in %v", name, nodes)
	return driver.Node{}
}

func TestTreeAndBrowseChunks(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	nodes, _ := d.Tree(ctx, nil)
	if len(nodes) != 3 {
		t.Fatalf("want 3 nodes, got %v", nodes)
	}
	n := node(d, t, "t_order")

	r, err := d.Browse(ctx, n, driver.BrowseOptions{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 450 || len(r.Rows) != 200 || !r.Truncated || len(r.Columns) != 4 {
		t.Fatalf("chunk1: total=%d rows=%d trunc=%v cols=%d", r.Total, len(r.Rows), r.Truncated, len(r.Columns))
	}
	r2, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 200, Offset: 400})
	if len(r2.Rows) != 50 || r2.Truncated || r2.Total != -1 {
		t.Fatalf("last chunk: rows=%d trunc=%v total=%d", len(r2.Rows), r2.Truncated, r2.Total)
	}
	if r2.Rows[0][0].V != "401" {
		t.Fatalf("offset wrong, first id %s", r2.Rows[0][0].V)
	}
	if !r.Rows[0][3].Null {
		t.Fatal("note should be NULL")
	}
}

func TestFilterSortAndBadWhere(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	n := node(d, t, "t_order")
	r, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "status = 'done'", SortCol: "amount", SortDesc: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 150 || r.Rows[0][0].V != "450" {
		t.Fatalf("total=%d first=%s", r.Total, r.Rows[0][0].V)
	}
	if _, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "nope ==", Limit: 10}); err == nil {
		t.Fatal("expected error for bad WHERE")
	}
}

func TestExecKinds(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	r, err := d.Exec(ctx, "SELECT count(*) AS c FROM t_order; SELECT 1 AS one, 'x' AS two")
	if err != nil || len(r.Rows) != 1 || r.Columns[1].Name != "two" || !strings.Contains(r.Message, "2 statements") {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = d.Exec(ctx, "UPDATE t_order SET status='x' WHERE id <= 5")
	if err != nil || r.Affected != 5 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := d.Exec(ctx, "SELEC broken"); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestEditing(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	ed := d.(driver.Editor)
	n := node(d, t, "t_order")

	if ok, why := ed.CanEdit(ctx, n); !ok {
		t.Fatal(why)
	}
	if ok, why := ed.CanEdit(ctx, node(d, t, "nopk")); ok || !strings.Contains(why, "primary key") {
		t.Fatalf("nopk should be refused: %v %q", ok, why)
	}
	if ok, _ := ed.CanEdit(ctx, node(d, t, "v_done")); ok {
		t.Fatal("views must be read-only")
	}

	r, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 5})
	show, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 3, driver.Text("it's new"))
	if err != nil || !strings.Contains(show, "'it''s new'") {
		t.Fatalf("%q %v", show, err)
	}
	r, _ = d.Browse(ctx, n, driver.BrowseOptions{Limit: 5})
	if r.Rows[0][3].V != "it's new" {
		t.Fatalf("update not applied: %+v", r.Rows[0])
	}
	// set NULL
	if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 3, driver.NullCell()); err != nil {
		t.Fatal(err)
	}

	if _, err := ed.InsertRow(ctx, n, r.Columns, map[string]driver.Cell{"status": driver.Text("new"), "amount": driver.Text("9.5")}); err != nil {
		t.Fatal(err)
	}
	r, _ = d.Browse(ctx, n, driver.BrowseOptions{Limit: 5})
	if r.Total != 451 {
		t.Fatalf("total after insert %d", r.Total)
	}
	if _, err := ed.DeleteRow(ctx, n, r.Columns, r.Rows[0]); err != nil {
		t.Fatal(err)
	}
	r, _ = d.Browse(ctx, n, driver.BrowseOptions{Limit: 5})
	if r.Total != 450 || r.Rows[0][0].V != "2" {
		t.Fatalf("after delete total=%d first=%s", r.Total, r.Rows[0][0].V)
	}
	// a stale row (pk no longer exists) must not silently succeed
	if _, err := ed.DeleteRow(ctx, n, r.Columns, []driver.Cell{driver.Text("99999"), {}, {}, {}}); err == nil {
		t.Fatal("deleting a missing row should error")
	}
}

func TestTransactions(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	tx := d.(driver.Transactor)
	ed := d.(driver.Editor)
	n := node(d, t, "t_order")

	if err := tx.Begin(ctx); err != nil || !tx.InTx() {
		t.Fatal(err)
	}
	r, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 3})
	if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 1, driver.Text("tx-edit")); err != nil {
		t.Fatal(err)
	}
	// a failing statement must not poison the transaction (savepoint)
	if _, err := d.Exec(ctx, "SELECT * FROM does_not_exist"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := d.Exec(ctx, "UPDATE t_order SET amount = 0 WHERE id = 2"); err != nil {
		t.Fatalf("tx unusable after error: %v", err)
	}
	if len(tx.TxLog()) != 2 {
		t.Fatalf("tx log: %v", tx.TxLog())
	}
	if err := tx.Rollback(ctx); err != nil || tx.InTx() {
		t.Fatal(err)
	}
	r, _ = d.Browse(ctx, n, driver.BrowseOptions{Limit: 3})
	if r.Rows[0][1].V == "tx-edit" {
		t.Fatal("rollback did not undo edit")
	}

	// commit path, driven through SQL text
	if _, err := d.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, "UPDATE t_order SET status = 'kept' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, "COMMIT"); err != nil || tx.InTx() {
		t.Fatal(err)
	}
	r, _ = d.Browse(ctx, n, driver.BrowseOptions{Limit: 1})
	if r.Rows[0][1].V != "kept" {
		t.Fatalf("commit lost: %v", r.Rows[0])
	}
}

func TestCancelAndHints(t *testing.T) {
	d := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("cancelled context should fail")
	}
	node(d, t, "t_order") // populates names
	h := d.(driver.Hinter).Hints("t_o")
	if len(h) == 0 || h[0] != "t_order" {
		t.Fatalf("hints: %v", h)
	}
	if h := d.(driver.Hinter).Hints("sel"); len(h) == 0 || h[0] != "SELECT" {
		t.Fatalf("keyword hints: %v", h)
	}
}
