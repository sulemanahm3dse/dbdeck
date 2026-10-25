package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/postgres"
)

// Integration test: set dbdeck_TEST_POSTGRES to a postgres:// URL of a scratch database.
func open(t *testing.T) (driver.Driver, config.Profile) {
	t.Helper()
	url := os.Getenv("dbdeck_TEST_POSTGRES")
	if url == "" {
		t.Skip("set dbdeck_TEST_POSTGRES=postgres://user:pass@host:port/db to run")
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
		`DROP SCHEMA IF EXISTS sales CASCADE`, `DROP TABLE IF EXISTS public.orders, public.nopk, public.pairs CASCADE`,
		`DROP MATERIALIZED VIEW IF EXISTS public.mv_totals`, `DROP VIEW IF EXISTS public.v_done`,
		`CREATE TABLE public.orders (
			id serial PRIMARY KEY, status text NOT NULL DEFAULT 'new', amount numeric(10,2),
			paid boolean DEFAULT false, created timestamptz DEFAULT now(), meta jsonb, tags int[])`,
		`INSERT INTO public.orders (status, amount, paid, meta, tags)
			SELECT CASE i % 3 WHEN 0 THEN 'done' ELSE 'pending' END, i * 1.5, i % 2 = 0,
			       jsonb_build_object('n', i, 'nested', jsonb_build_object('ok', true)), ARRAY[i, i+1]
			FROM generate_series(1, 450) i`,
		`CREATE TABLE public.nopk (a text)`,
		`CREATE TABLE public.pairs (a int, b int, note text, PRIMARY KEY (a, b))`,
		`INSERT INTO public.pairs VALUES (1,1,'x'), (1,2,'y'), (2,1,'z')`,
		`CREATE VIEW public.v_done AS SELECT * FROM public.orders WHERE status = 'done'`,
		`CREATE MATERIALIZED VIEW public.mv_totals AS SELECT status, sum(amount) AS total FROM public.orders GROUP BY status`,
		`CREATE SCHEMA sales`, `CREATE TABLE sales."Weird Name" ("Id" int PRIMARY KEY, "Qty" int)`,
		`INSERT INTO sales."Weird Name" VALUES (1, 10), (2, 20)`,
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

func tables(t *testing.T, d driver.Driver, schema string) []driver.Node {
	t.Helper()
	roots, err := d.Tree(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := find(t, roots, schema)
	kids, err := d.Tree(context.Background(), &s)
	if err != nil {
		t.Fatal(err)
	}
	return kids
}

func TestTree(t *testing.T) {
	d, _ := open(t)
	roots, _ := d.Tree(context.Background(), nil)
	names := []string{}
	for _, r := range roots {
		names = append(names, r.Name)
		if r.Kind != driver.KindSchema {
			t.Fatalf("root %+v should be a schema", r)
		}
	}
	if names[0] != "public" || !contains(names, "sales") || contains(names, "pg_catalog") || contains(names, "information_schema") {
		t.Fatalf("schemas: %v", names)
	}
	pub := tables(t, d, "public")
	if find(t, pub, "orders").Kind != driver.KindTable || find(t, pub, "v_done").Kind != driver.KindView || find(t, pub, "mv_totals").Kind != driver.KindView {
		t.Fatalf("kinds wrong: %+v", pub)
	}
	if find(t, tables(t, d, "sales"), "Weird Name").Kind != driver.KindTable {
		t.Fatal("mixed-case, spaced names must work")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestBrowseTypesPagingFilterSort(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	n := find(t, tables(t, d, "public"), "orders")
	r, err := d.Browse(ctx, n, driver.BrowseOptions{Limit: 200})
	if err != nil || r.Total != 450 || len(r.Rows) != 200 || !r.Truncated || len(r.Columns) != 7 {
		t.Fatalf("%+v %v", r, err)
	}
	row := r.Rows[0]
	if row[0].V != "1" || row[1].V != "pending" || row[2].V != "1.50" || row[3].V != "false" || !strings.Contains(row[5].V, `"nested"`) || row[6].V == "" {
		t.Fatalf("value formatting: %+v", row)
	}
	if r.Columns[2].Type == "" {
		t.Fatal("column type names should be reported")
	}
	r2, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 200, Offset: 400})
	if len(r2.Rows) != 50 || r2.Truncated || r2.Rows[0][0].V != "401" {
		t.Fatalf("last chunk: %d rows, first %s", len(r2.Rows), r2.Rows[0][0].V)
	}
	r3, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "status = 'done' AND amount > 600", SortCol: "id", SortDesc: true, Limit: 5})
	if err != nil || r3.Total != 17 || r3.Rows[0][0].V != "450" {
		t.Fatalf("filter/sort: total=%d err=%v", r3.Total, err)
	}
	if _, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "nope ==", Limit: 5}); err == nil {
		t.Fatal("bad WHERE must error")
	}
	if r, err := d.Browse(ctx, find(t, tables(t, d, "sales"), "Weird Name"), driver.BrowseOptions{Limit: 5, SortCol: "Qty", SortDesc: true}); err != nil || r.Rows[0][1].V != "20" {
		t.Fatalf("quoted identifiers: %+v %v", r, err)
	}
	if r, err := d.Browse(ctx, find(t, tables(t, d, "public"), "mv_totals"), driver.BrowseOptions{Limit: 5}); err != nil || len(r.Rows) != 2 {
		t.Fatalf("materialized view: %+v %v", r, err)
	}
}

func TestEditingAllColumnTypes(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	ed := d.(driver.Editor)
	pub := tables(t, d, "public")
	n := find(t, pub, "orders")
	if ok, why := ed.CanEdit(ctx, n); !ok {
		t.Fatal(why)
	}
	if ok, why := ed.CanEdit(ctx, find(t, pub, "nopk")); ok || !strings.Contains(why, "primary key") {
		t.Fatalf("nopk: %v %q", ok, why)
	}
	for _, v := range []string{"v_done", "mv_totals"} {
		if ok, _ := ed.CanEdit(ctx, find(t, pub, v)); ok {
			t.Fatalf("%s must be read-only", v)
		}
	}
	r, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 3})
	edit := func(col int, v driver.Cell) {
		t.Helper()
		if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], col, v); err != nil {
			t.Fatalf("col %s: %v", r.Columns[col].Name, err)
		}
	}
	edit(1, driver.Text("it's \"quoted\""))     // text with quotes
	edit(2, driver.Text("99.95"))               // numeric
	edit(3, driver.Text("true"))                // boolean
	edit(4, driver.Text("2030-01-02 03:04:05")) // timestamptz
	edit(5, driver.Text(`{"edited": [1,2]}`))   // jsonb
	edit(6, driver.Text("{7,8,9}"))             // int[]
	got, err := d.Exec(ctx, "SELECT status, amount::text, paid, created::date::text, meta->>'edited', tags::text FROM public.orders WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	g := got.Rows[0]
	if g[0].V != `it's "quoted"` || g[1].V != "99.95" || g[2].V != "true" || g[3].V != "2030-01-02" || g[4].V != "[1, 2]" || g[5].V != "{7,8,9}" {
		t.Fatalf("stored values: %+v", g)
	}
	edit(2, driver.NullCell())
	if got, _ := d.Exec(ctx, "SELECT amount IS NULL FROM public.orders WHERE id = 1"); got.Rows[0][0].V != "true" {
		t.Fatal("NULL update failed")
	}
	if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 3, driver.Text("not-a-bool")); err == nil {
		t.Fatal("server type errors must be reported")
	}

	// insert with defaults, then delete it again
	show, err := ed.InsertRow(ctx, n, r.Columns, map[string]driver.Cell{"status": driver.Text("fresh")})
	if err != nil || !strings.Contains(show, "INSERT INTO") {
		t.Fatalf("%q %v", show, err)
	}
	if _, err := ed.InsertRow(ctx, n, r.Columns, map[string]driver.Cell{}); err != nil {
		t.Fatalf("all-default insert: %v", err)
	}
	last, _ := d.Browse(ctx, n, driver.BrowseOptions{Offset: 0, Limit: 5, SortCol: "id", SortDesc: true})
	if last.Rows[0][1].V != "new" || last.Rows[1][1].V != "fresh" || last.Rows[1][3].V != "false" {
		t.Fatalf("defaults not applied: %+v %+v", last.Rows[0], last.Rows[1])
	}
	if _, err := ed.DeleteRow(ctx, n, last.Columns, last.Rows[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.DeleteRow(ctx, n, last.Columns, last.Rows[0]); err == nil {
		t.Fatal("deleting an already-deleted row must report it")
	}

	// composite primary key
	pn := find(t, pub, "pairs")
	pr, _ := d.Browse(ctx, pn, driver.BrowseOptions{Limit: 10, SortCol: "a"})
	if _, err := ed.UpdateCell(ctx, pn, pr.Columns, pr.Rows[1], 2, driver.Text("changed")); err != nil {
		t.Fatal(err)
	}
	chk, _ := d.Exec(ctx, "SELECT note FROM public.pairs WHERE a = 1 AND b = 2")
	other, _ := d.Exec(ctx, "SELECT count(*) FROM public.pairs WHERE note = 'changed'")
	if chk.Rows[0][0].V != "changed" || other.Rows[0][0].V != "1" {
		t.Fatalf("composite key hit the wrong row(s): %v %v", chk.Rows, other.Rows)
	}
}

func TestTransactionsAndSavepoints(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	tx := d.(driver.Transactor)
	ed := d.(driver.Editor)
	n := find(t, tables(t, d, "public"), "orders")
	r, _ := d.Browse(ctx, n, driver.BrowseOptions{Limit: 3})

	if err := tx.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.UpdateCell(ctx, n, r.Columns, r.Rows[0], 1, driver.Text("in-tx")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, "SELECT * FROM no_such_table"); err == nil {
		t.Fatal("expected an error")
	}
	// postgres would normally refuse everything now ("current transaction is aborted")
	if _, err := d.Exec(ctx, "UPDATE public.orders SET amount = 1 WHERE id = 2"); err != nil {
		t.Fatalf("savepoint should keep the transaction usable: %v", err)
	}
	if _, err := d.Browse(ctx, n, driver.BrowseOptions{Where: "bad ==", Limit: 1}); err == nil {
		t.Fatal("expected a filter error")
	}
	if len(tx.TxLog()) != 2 {
		t.Fatalf("log: %v", tx.TxLog())
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Exec(ctx, "SELECT status FROM public.orders WHERE id = 1"); g.Rows[0][0].V == "in-tx" {
		t.Fatal("rollback did not undo the edit")
	}
	// commit path
	if err := tx.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	d.Exec(ctx, "UPDATE public.orders SET status = 'kept' WHERE id = 3")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Exec(ctx, "SELECT status FROM public.orders WHERE id = 3"); g.Rows[0][0].V != "kept" {
		t.Fatal("commit lost")
	}
}

func TestExecKindsCancelAndErrors(t *testing.T) {
	d, p := open(t)
	ctx := context.Background()
	r, err := d.Exec(ctx, "INSERT INTO public.pairs VALUES (9, 9, 'r') RETURNING a, note")
	if err != nil || len(r.Rows) != 1 || r.Rows[0][1].V != "r" {
		t.Fatalf("RETURNING should produce rows: %+v %v", r, err)
	}
	r, err = d.Exec(ctx, "CREATE TABLE public.tmp_x (i int); INSERT INTO public.tmp_x VALUES (1),(2); SELECT count(*) AS n FROM public.tmp_x; DROP TABLE public.tmp_x")
	if err != nil || !strings.Contains(r.Message, "4 statements") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := d.Exec(ctx, "SELECT 1/0"); err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("server error text should reach the user: %v", err)
	}
	if r, err := d.Exec(ctx, d.(driver.Explainer).ExplainPrefix()+"SELECT * FROM public.orders"); err != nil || len(r.Rows) == 0 {
		t.Fatalf("explain: %v", err)
	}
	// $$ bodies are not split
	if _, err := d.Exec(ctx, "DO $$ BEGIN PERFORM 1; PERFORM 2; END $$"); err != nil {
		t.Fatalf("dollar quoting: %v", err)
	}

	// cancellation returns promptly and the connection pool stays healthy
	cctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := d.Exec(cctx, "SELECT pg_sleep(20)"); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	if r, err := d.Exec(ctx, "SELECT 'still alive'"); err != nil || r.Rows[0][0].V != "still alive" {
		t.Fatalf("connection unusable after cancel: %v", err)
	}

	// wrong password: clear error, secret never echoed
	bad := p
	bad.Password = "definitely-wrong-pw"
	_, err = driver.Open(ctx, bad)
	if err == nil || strings.Contains(err.Error(), "definitely-wrong-pw") {
		t.Fatalf("auth failure should be reported without echoing the password: %v", err)
	}
	t.Logf("wrong-password error: %v", err)
	// unreachable host fails fast
	unreachable := p
	unreachable.Port = 1
	start = time.Now()
	if _, err := driver.Open(ctx, unreachable); err == nil || time.Since(start) > 12*time.Second {
		t.Fatalf("unreachable: %v after %v", err, time.Since(start))
	}
}
