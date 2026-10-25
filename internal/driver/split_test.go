package driver

import (
	"reflect"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"select 1; select 2;", []string{"select 1", "select 2"}},
		{"select 'a;b'; select 2", []string{"select 'a;b'", "select 2"}},
		{`select "x;y" ; -- c; d` + "\nselect 3", []string{`select "x;y"`, "-- c; d\nselect 3"}},
		{"select 1 /* ; */ ; select 2", []string{"select 1 /* ; */", "select 2"}},
		{"create function f() returns int as $$ select 1; $$ language sql; select 2",
			[]string{"create function f() returns int as $$ select 1; $$ language sql", "select 2"}},
		{"select 'it''s;ok'; x", []string{"select 'it''s;ok'", "x"}},
		{"  ;; ", nil},
		{"-- only comment", nil},
	}
	for _, c := range cases {
		got := SplitStatements(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitStatements(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}
