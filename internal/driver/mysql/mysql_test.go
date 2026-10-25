package mysql_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/mysql"
)

// Integration test: set dbdeck_TEST_MYSQL to a mysql:// URL of a scratch database (user needs all privileges).
func open(t *testing.T) (driver.Driver, config.Profile) {
	t.Helper()
	url := os.Getenv("dbdeck_TEST_MYSQL")
	if url == "" {
		t.Skip("set dbdeck_TEST_MYSQL=mysql://user:pass@host:port/db to run")
	}
	p, err := config.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	d, err := driver.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	for _, s := range []string{
		"DROP DATABASE IF EXISTS other_db", "DROP VIEW IF EXISTS v_done",
		"DROP TABLE IF EXISTS orders, nopk, pairs, `Weird Name`",
		"CREATE TABLE orders (id INT AUTO_INCREMENT PRIMARY KEY, status VARCHAR(20) NOT NULL DEFAULT 'new', amount DECIMAL(10,2), paid TINYINT(1) DEFAULT 0, created DATETIME DEFAULT CURRENT_TIMESTAMP, meta JSON, note TEXT)",
		`INSERT INTO orders (status, amount, paid, meta)
			WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 450)
			SELECT IF(i % 3 = 0, 'done', 'pending'), i * 1.5, i % 2, JSON_OBJECT('n', i, 'nested', JSON_OBJECT('ok', true)) FROM n`,
		"CREATE TABLE nopk (a VARCHAR(10))",
		"CREATE TABLE pairs (a INT, b INT, note VARCHAR(20), PRIMARY KEY (a, b))",
		"INSERT INTO pairs VALUES (1,1,'x'),(1,2,'y'),(2,1,'z')",
		"CREATE VIEW v_done AS SELECT * FROM orders WHERE status = 'done'",
		"CREATE TABLE `Weird Name` (`Id` INT PRIMARY KEY, `Qty` INT)",
		"INSERT INTO `Weird Name` VALUES (1, 10), (2, 20)",
		"CREATE DATABASE other_db", "CREATE TABLE other_db.t (x INT PRIMARY KEY)", "INSERT INTO other_db.t VALUES (42)",
	} {
		if _, err := d.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return d, p
}

func find(t *testing.T, nodes []driver.Node, name string) driver.Node {
	t.Helper()
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("%q not in %+v", name, nodes)
	return driver.Node{}
}

func tables(t *testing.T, d driver.Driver, db string) []driver.Node {
	t.Helper()
	roots, err := d.Tree(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := find(t, roots, db)
	kids, err := d.Tree(context.Background(), &s)
	if err != nil {
		t.Fatal(err)
	}
	return kids
}

func TestTreeAndBrowse(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	roots, _ := d.Tree(ctx, nil)
	for _, r := range roots {
		if r.Kind != driver.KindDatabase || r.Name == "mysql" || r.Name == "information_schema" || r.Name == "sys" || r.Name == "performance_schema" {
			t.Fatalf("unexpected root %+v", r)
		}
	}
	find(t, roots, "shop")
	find(t, roots, "other_db")
	shop := tables(t, d, "shop")
	if find(t, shop, "orders").Kind != driver.KindTable || find(t, shop, "v_done").Kind != driver.KindView {
		t.Fatalf("kinds: %+v", shop)
	}
	if find(t, tables(t, d, "other_db"), "t").Path[0] != "other_db" {
		t.Fatal("other database tables")
	}
	n := find(t, shop, "orders")
	r, err := d.Browse(ctx, n, driver.BrowseOptions{Limit: 200})
	if err != nil || r.Total != 450 || len(r.Rows) != 200 || !r.Truncated {
		t.Fatalf("%+v %v", r.Total, err)
	}
	row := r.Rows[0]
	if row[0].V != "1" || row[2].V != "1.50" || row[3].V != "1" || !strings.Contains(row[5].V, "nested") || !row[6].Null {
		t.Fatalf("formatting: %+v", row)
	}
	r2, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 200, Offset: 400})
	if len(r2.Rows) != 50 || r2.Truncated {
		t.Fatalf("last chunk %d", len(r2.Rows))
	}
	r3, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "status = 'done' AND amount > 600", SortCol: "id", SortDesc: true, Limit: 5})
	if err != nil || r3.Total != 17 || r3.Rows[0][0].V != "450" {
		t.Fatalf("filter/sort total=%d err=%v", r3.Total, err)
	}
	if _, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "nope ==", Limit: 5}); err == nil {
		t.Fatal("bad WHERE must error")
	}
	if r, err := d.Browse(ctx, find(t, shop, "Weird Name"), driver.BrowseOptions{Limit: 5, SortCol: "Qty", SortDesc: true}); err != nil || r.Rows[0][1].V != "20" {
		t.Fatalf("backtick quoting: %+v %v", r, err)
	}
	if r, err := d.Browse(ctx, find(t, shop, "v_done"), driver.BrowseOptions{Limit: 5}); err != nil || r.Total != 150 {
		t.Fatalf("view: %v", err)
	}
}

func TestEditing(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	ed := d.(driver.Editor)
	shop := tables(t, d, "shop")
	n := find(t, shop, "orders")
	if ok, why := ed.CanEdit(ctx, n); !ok {
		t.Fatal(why)
	}
	if ok, why := ed.CanEdit(ctx, find(t, shop, "nopk")); ok || !strings.Contains(why, "primary key") {
		t.Fatalf("nopk: %v %q", ok, why)
	}
	if ok, _ := ed.CanEdit(ctx, find(t, shop, "v_done")); ok {
		t.Fatal("views are read-only")
	}
	r, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 3})
	edit := func(col int, v driver.Cell) {
		t.Helper()
		if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], col, v); err != nil {
			t.Fatalf("col %s: %v", r.Columns[col].Name, err)
		}
	}
	edit(1, driver.Text("it's \"quoted\" \\ back"))
	edit(2, driver.Text("99.95"))
	edit(3, driver.Text("0"))
	edit(4, driver.Text("2030-01-02 03:04:05"))
	edit(5, driver.Text(`{"edited": [1, 2]}`))
	edit(6, driver.Text("multi\nline"))
	g, _ := d.Exec(ctx, "SELECT status, CAST(amount AS CHAR), paid, DATE(created), JSON_EXTRACT(meta, '$.edited[1]'), note FROM orders WHERE id = 1")
	row := g.Rows[0]
	if row[0].V != `it's "quoted" \ back` || row[1].V != "99.95" || row[2].V != "0" || row[3].V != "2030-01-02" || row[4].V != "2" || row[5].V != "multi\nline" {
		t.Fatalf("stored: %+v", row)
	}
	// writing the same value again must not look like "0 rows matched"
	edit(1, driver.Text(`it's "quoted" \ back`))
	edit(6, driver.NullCell())
	if g, _ := d.Exec(ctx, "SELECT note IS NULL FROM orders WHERE id = 1"); g.Rows[0][0].V != "1" {
		t.Fatal("NULL update failed")
	}
	if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 4, driver.Text("not-a-date")); err == nil {
		t.Fatal("server type errors must surface")
	}

	if _, err := ed.InsertRow(ctx, n, r.Columns, map[string]driver.Cell{"status": driver.Text("fresh")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.InsertRow(ctx, n, r.Columns, map[string]driver.Cell{}); err != nil {
		t.Fatalf("all-default insert: %v", err)
	}
	last, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 5, SortCol: "id", SortDesc: true})
	if last.Rows[0][1].V != "new" || last.Rows[1][1].V != "fresh" {
		t.Fatalf("defaults: %+v %+v", last.Rows[0], last.Rows[1])
	}
	if _, err := ed.DeleteRow(ctx, n, last.Columns, last.Rows[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.DeleteRow(ctx, n, last.Columns, last.Rows[0]); err == nil {
		t.Fatal("deleting a missing row must be reported")
	}

	pn := find(t, shop, "pairs")
	pr, _ := d.Browse(ctx, pn, driver.BrowseOptions{Limit: 10, SortCol: "a"})
	if _, err := ed.UpdateCell(ctx, pn, pr.Columns, pr.Rows[1], 2, driver.Text("changed")); err != nil {
		t.Fatal(err)
	}
	if c, _ := d.Exec(ctx, "SELECT note FROM pairs WHERE a = 1 AND b = 2"); c.Rows[0][0].V != "changed" {
		t.Fatal("composite key update missed")
	}
	if c, _ := d.Exec(ctx, "SELECT count(*) FROM pairs WHERE note = 'changed'"); c.Rows[0][0].V != "1" {
		t.Fatal("composite key update hit other rows")
	}
	// table in another database
	on := find(t, tables(t, d, "other_db"), "t")
	or, _ := d.Browse(ctx, on, driver.BrowseOptions{Limit: 5})
	if _, err := ed.UpdateCell(ctx, on, or.Columns, or.Rows[0], 0, driver.Text("43")); err != nil {
		t.Fatalf("cross-database edit: %v", err)
	}
}

func TestTransactionsAndExec(t *testing.T) {
	d, p := open(t)
	ctx := context.Background()
	tx := d.(driver.Transactor)
	ed := d.(driver.Editor)
	n := find(t, tables(t, d, "shop"), "orders")
	r, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 3})

	if err := tx.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 1, driver.Text("in-tx")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, "SELECT * FROM no_such_table"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := d.Exec(ctx, "UPDATE orders SET amount = 1 WHERE id = 2"); err != nil {
		t.Fatalf("tx unusable after a failed statement: %v", err)
	}
	if len(tx.TxLog()) != 2 {
		t.Fatalf("log %v", tx.TxLog())
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Exec(ctx, "SELECT status FROM orders WHERE id = 1"); g.Rows[0][0].V == "in-tx" {
		t.Fatal("rollback failed")
	}
	if _, err := d.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	d.Exec(ctx, "UPDATE orders SET status = 'kept' WHERE id = 3")
	if _, err := d.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Exec(ctx, "SELECT status FROM orders WHERE id = 3"); g.Rows[0][0].V != "kept" {
		t.Fatal("commit lost")
	}

	// statement kinds
	for _, q := range []string{"SHOW TABLES", "DESCRIBE orders", "EXPLAIN SELECT * FROM orders"} {
		if r, err := d.Exec(ctx, q); err != nil || len(r.Rows) == 0 {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if r, err := d.Exec(ctx, "SELECT 1 AS a; SELECT 2 AS b"); err != nil || r.Rows[0][0].V != "2" {
		t.Fatalf("multi: %v", err)
	}
	if _, err := d.Exec(ctx, "SELECT 1/0 AS x; SELEC bad"); err == nil || !strings.Contains(err.Error(), "statement 2") {
		t.Fatalf("error should name the failing statement: %v", err)
	}

	cctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := d.Exec(cctx, "SELECT SLEEP(20)"); err == nil && time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	if r, err := d.Exec(ctx, "SELECT 'alive'"); err != nil || r.Rows[0][0].V != "alive" {
		t.Fatalf("unusable after cancel: %v", err)
	}

	bad := p
	bad.Password = "definitely-wrong-pw"
	if _, err := driver.Open(ctx, bad); err == nil || strings.Contains(err.Error(), "definitely-wrong-pw") {
		t.Fatalf("auth failure must not echo the password: %v", err)
	} else {
		t.Logf("wrong-password error: %v", err)
	}
	unreachable := p
	unreachable.Port = 1
	start = time.Now()
	if _, err := driver.Open(ctx, unreachable); err == nil || time.Since(start) > 12*time.Second {
		t.Fatalf("unreachable: %v after %v", err, time.Since(start))
	}
}
