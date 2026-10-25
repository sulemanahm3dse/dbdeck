package config

import (
	"strings"
	"testing"
)

func TestParsePostgresURL(t *testing.T) {
	p, err := ParseURL("postgres://bob:p%40ss%2Fword@db.example.com:5433/erd_forge?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != EnginePostgres || p.Host != "db.example.com" || p.Port != 5433 ||
		p.User != "bob" || p.Password != "p@ss/word" || p.Database != "erd_forge" || p.TLS != "disable" {
		t.Fatalf("unexpected profile: %+v", p)
	}
}

func TestRoundTripSpecialPassword(t *testing.T) {
	p := Profile{Engine: EnginePostgres, Host: "h", Port: 5432, User: "u", Password: "a@b/c:d?e#f", Database: "d", TLS: "disable"}
	back, err := ParseURL(p.BuildURL(false))
	if err != nil {
		t.Fatal(err)
	}
	if back.Password != p.Password || back.User != "u" || back.Database != "d" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestMaskedURLHidesPassword(t *testing.T) {
	p := Profile{Engine: EnginePostgres, Host: "h", User: "u", Password: "secret"}
	s := p.BuildURL(true)
	if strings.Contains(s, "secret") {
		t.Fatalf("password leaked: %s", s)
	}
	if !strings.Contains(s, "***") {
		t.Fatalf("expected mask in %s", s)
	}
}

func TestRedisURLs(t *testing.T) {
	p, err := ParseURL("rediss://:pw@cache:6380/3")
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != EngineRedis || p.TLS != "on" || p.Password != "pw" || p.Database != "3" || p.Port != 6380 {
		t.Fatalf("unexpected: %+v", p)
	}
	if got := p.BuildURL(false); got != "rediss://:pw@cache:6380/3" {
		t.Fatalf("got %s", got)
	}
}

func TestSQLiteAndErrors(t *testing.T) {
	p, err := ParseURL("sqlite:///tmp/x.db")
	if err != nil || p.Path != "/tmp/x.db" {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"", "localhost:5432", "ftp://x", "postgres://h:99999/db"} {
		if _, err := ParseURL(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	if (Profile{Engine: EngineSQLite}).Validate() == nil {
		t.Error("sqlite without path should fail")
	}
	if (Profile{Engine: EngineRedis, Database: "x"}).Validate() == nil {
		t.Error("redis non-numeric db should fail")
	}
	if err := (Profile{Engine: EnginePostgres}).Validate(); err != nil {
		t.Errorf("defaults should validate: %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("dbdeck_CONFIG_DIR", t.TempDir())
	if err := Upsert("", Profile{Name: "a", Engine: EnginePostgres, Host: "h", Password: "nope"}); err != nil {
		t.Fatal(err)
	}
	if err := Upsert("a", Profile{Name: "b", Engine: EngineRedis, Host: "r"}); err != nil {
		t.Fatal(err)
	}
	list, err := Load()
	if err != nil || len(list) != 1 || list[0].Name != "b" || list[0].Password != "" {
		t.Fatalf("%+v %v", list, err)
	}
}
