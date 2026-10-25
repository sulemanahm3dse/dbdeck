package redisdrv

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"dbdeck/internal/driver"
)

func setup(t *testing.T) (driver.Driver, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.Set("greeting", "hello")
	mr.HSet("user:1", "name", "Ann", "age", "30")
	mr.HSet("user:2", "name", "Bob")
	mr.RPush("queue:jobs", "a", "b", "c")
	mr.SAdd("tags", "x", "y")
	mr.ZAdd("rank", 1, "alice")
	mr.ZAdd("rank", 2.5, "bob")
	d, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, mr
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

func TestTreeGroupsOnColon(t *testing.T) {
	d, _ := setup(t)
	ctx := context.Background()
	dbs, err := d.Tree(ctx, nil)
	if err != nil || len(dbs) == 0 || dbs[0].Kind != driver.KindDatabase {
		t.Fatalf("%v %v", dbs, err)
	}
	top, err := d.Tree(ctx, &dbs[0])
	if err != nil {
		t.Fatal(err)
	}
	user := find(t, top, "user")
	if user.Kind != driver.KindFolder || user.Detail != "2" {
		t.Fatalf("user folder: %+v", user)
	}
	if g := find(t, top, "greeting"); g.Kind != driver.KindKey || g.Detail != "string" {
		t.Fatalf("greeting: %+v", g)
	}
	// folders come before keys
	if top[0].Kind != driver.KindFolder {
		t.Fatalf("folders should sort first: %+v", top)
	}
	kids, err := d.Tree(ctx, &user)
	if err != nil || len(kids) != 2 || kids[0].Name != "1" || kids[0].Detail != "hash" {
		t.Fatalf("%+v %v", kids, err)
	}
}

func browse(t *testing.T, d driver.Driver, db, key string, o driver.BrowseOptions) driver.Result {
	t.Helper()
	r, err := d.Browse(context.Background(), driver.Node{Kind: driver.KindKey, Path: []string{db, key}}, o)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return r
}

func TestBrowseEachType(t *testing.T) {
	d, _ := setup(t)
	o := driver.BrowseOptions{Limit: 100}

	r := browse(t, d, "0", "greeting", o)
	if r.Rows[0][0].V != "hello" || !strings.Contains(r.Meta, "TYPE string") {
		t.Fatalf("%+v", r)
	}
	r = browse(t, d, "0", "user:1", o)
	if r.Total != 2 || r.Rows[0][0].V != "age" || r.Rows[1][1].V != "Ann" {
		t.Fatalf("hash: %+v", r)
	}
	r = browse(t, d, "0", "queue:jobs", driver.BrowseOptions{Limit: 2})
	if r.Total != 3 || len(r.Rows) != 2 || !r.Truncated || r.Rows[1][1].V != "b" {
		t.Fatalf("list: %+v", r)
	}
	r = browse(t, d, "0", "queue:jobs", driver.BrowseOptions{Limit: 2, Offset: 2})
	if len(r.Rows) != 1 || r.Rows[0][0].V != "2" || r.Truncated {
		t.Fatalf("list page 2: %+v", r)
	}
	r = browse(t, d, "0", "tags", o)
	if r.Total != 2 || r.Rows[0][0].V != "x" {
		t.Fatalf("set: %+v", r)
	}
	r = browse(t, d, "0", "rank", o)
	if r.Total != 2 || r.Rows[1][0].V != "bob" || r.Rows[1][1].V != "2.5" {
		t.Fatalf("zset: %+v", r)
	}
	r = browse(t, d, "0", "user:1", driver.BrowseOptions{Limit: 10, Where: "an"})
	if r.Total != 1 || r.Rows[0][0].V != "name" {
		t.Fatalf("filter: %+v", r)
	}
	if _, err := d.Browse(context.Background(), driver.Node{Path: []string{"0", "missing"}}, o); err == nil {
		t.Fatal("missing key should error")
	}
}

func TestConsole(t *testing.T) {
	d, mr := setup(t)
	ctx := context.Background()
	r, err := d.Exec(ctx, `SET demo "hello world"`)
	if err != nil || r.Message != "OK" {
		t.Fatalf("%+v %v", r, err)
	}
	if v, _ := mr.Get("demo"); v != "hello world" {
		t.Fatalf("quoted arg lost: %q", v)
	}
	r, err = d.Exec(ctx, "LRANGE queue:jobs 0 -1")
	if err != nil || len(r.Rows) != 3 || r.Rows[2][1].V != "c" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = d.Exec(ctx, "GET nothing-here")
	if err != nil || r.Message != "(nil)" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := d.Exec(ctx, "NOSUCHCMD"); err == nil {
		t.Fatal("expected server error")
	}
	if _, err := d.Exec(ctx, "MONITOR"); err == nil {
		t.Fatal("MONITOR must be refused")
	}
	r, err = d.Exec(ctx, "SELECT 1\nSET inone 1\nSELECT 0\nGET inone")
	if err != nil || r.Message == "" {
		t.Fatalf("%+v %v", r, err)
	}
	if v, _ := mr.DB(1).Get("inone"); v != "1" {
		t.Fatal("SELECT 1 should have redirected the next command to db1")
	}
	if h := d.(driver.Hinter).Hints("hge"); !reflect.DeepEqual(h, []string{"HGET", "HGETALL"}) {
		t.Fatalf("hints %v", h)
	}
}

func TestEditing(t *testing.T) {
	d, mr := setup(t)
	ctx := context.Background()
	ed := d.(driver.Editor)
	mk := func(k string) driver.Node { return driver.Node{Kind: driver.KindKey, Path: []string{"0", k}} }

	if ok, _ := ed.CanEdit(ctx, mk("user:1")); !ok {
		t.Fatal("hash should be editable")
	}
	cols := []driver.Column{{Name: "field"}, {Name: "value"}}
	if _, err := ed.UpdateCell(ctx, mk("user:1"), cols, []driver.Cell{driver.Text("name"), driver.Text("Ann")}, 1, driver.Text("Annie")); err != nil {
		t.Fatal(err)
	}
	if v := mr.HGet("user:1", "name"); v != "Annie" {
		t.Fatalf("hash edit: %q", v)
	}
	if _, err := ed.UpdateCell(ctx, mk("user:1"), cols, []driver.Cell{driver.Text("name"), driver.Text("x")}, 0, driver.Text("n")); err == nil {
		t.Fatal("renaming fields must be refused")
	}
	if _, err := ed.UpdateCell(ctx, mk("greeting"), nil, nil, 0, driver.NullCell()); err == nil {
		t.Fatal("NULL must be refused")
	}
	if _, err := ed.InsertRow(ctx, mk("user:1"), cols, map[string]driver.Cell{"field": driver.Text("city"), "value": driver.Text("Lahore")}); err != nil {
		t.Fatal(err)
	}
	if mr.HGet("user:1", "city") != "Lahore" {
		t.Fatal("hash insert failed")
	}
	if _, err := ed.InsertRow(ctx, mk("rank"), nil, map[string]driver.Cell{"member": driver.Text("carol"), "score": driver.Text("abc")}); err == nil {
		t.Fatal("bad score must error")
	}
	if _, err := ed.InsertRow(ctx, mk("rank"), nil, map[string]driver.Cell{"member": driver.Text("carol"), "score": driver.Text("9")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.DeleteRow(ctx, mk("rank"), nil, []driver.Cell{driver.Text("carol"), driver.Text("9")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.DeleteRow(ctx, mk("rank"), nil, []driver.Cell{driver.Text("carol"), driver.Text("9")}); err == nil {
		t.Fatal("deleting a missing member should report it")
	}
	if _, err := ed.UpdateCell(ctx, mk("greeting"), nil, []driver.Cell{driver.Text("hello")}, 0, driver.Text("bye")); err != nil {
		t.Fatal(err)
	}
	if v, _ := mr.Get("greeting"); v != "bye" {
		t.Fatal("string edit failed")
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := SplitArgs(`SET k "a \"b\" c" 'it\'s' plain`)
	want := []string{"SET", "k", `a "b" c`, "it's", "plain"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := SplitArgs(`GET "oops`); err == nil {
		t.Fatal("unterminated quote should error")
	}
	got, _ = SplitArgs(`SET k ""`)
	if len(got) != 3 || got[2] != "" {
		t.Fatalf("empty quoted arg: %q", got)
	}
}

func TestSearchAcrossUnexpandedKeys(t *testing.T) {
	d, _ := setup(t)
	s := d.(driver.Searcher)
	nodes, err := s.Search(context.Background(), "USER", 100)
	if err != nil || len(nodes) != 2 || nodes[0].Name != "user:1" || nodes[0].Detail != "hash" || nodes[0].Path[1] != "user:1" {
		t.Fatalf("%+v %v", nodes, err)
	}
	if n, _ := s.Search(context.Background(), "zzz", 100); len(n) != 0 {
		t.Fatal("no match expected")
	}
	if n, _ := s.Search(context.Background(), "e", 2); len(n) != 2 {
		t.Fatalf("limit should apply, got %d", len(n))
	}
}
