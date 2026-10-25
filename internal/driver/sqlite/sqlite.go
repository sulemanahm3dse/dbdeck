// Package sqlite registers the SQLite engine (pure Go, no CGO: ncruces/go-sqlite3 on wazero).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	"dbdeck/internal/driver/sqldb"
)

func init() { driver.Register(config.EngineSQLite, open) }

func open(ctx context.Context, p config.Profile) (driver.Driver, error) {
	dsn := p.Path
	if p.Path != ":memory:" {
		abs, err := filepath.Abs(p.Path)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("cannot open %s: %w", abs, err)
		}
		u := url.URL{Scheme: "file", Path: abs, RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}
		dsn = u.String()
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // keeps :memory: coherent and avoids writer contention
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return sqldb.New(config.EngineSQLite, "sqlite · "+p.Path, db, dialect{}), nil
}

// OpenDB wraps an existing database handle (used by tests and --demo).
func OpenDB(title string, db *sql.DB) driver.Driver {
	db.SetMaxOpenConns(1)
	return sqldb.New(config.EngineSQLite, title, db, dialect{})
}

type dialect struct{}

func (dialect) Quote(s string) string           { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (dialect) Placeholder(int) string          { return "?" }
func (dialect) ExplainPrefix() string           { return "EXPLAIN QUERY PLAN " }
func (dialect) DefaultValues(ref string) string { return "INSERT INTO " + ref + " DEFAULT VALUES" }

func (dialect) TableRef(n driver.Node) string {
	return dialect{}.Quote(n.Path[len(n.Path)-1])
}

func (dialect) Roots(ctx context.Context, q sqldb.Queryer) ([]driver.Node, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type FROM sqlite_master
		WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY type DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.Node
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		k := driver.KindTable
		if typ == "view" {
			k = driver.KindView
		}
		out = append(out, driver.Node{ID: "t:" + name, Name: name, Kind: k, Path: []string{name}})
	}
	return out, rows.Err()
}

func (dialect) Children(context.Context, sqldb.Queryer, driver.Node) ([]driver.Node, error) {
	return nil, nil
}

func (dialect) PrimaryKey(ctx context.Context, q sqldb.Queryer, n driver.Node) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, n.Path[len(n.Path)-1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pk []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		pk = append(pk, c)
	}
	return pk, rows.Err()
}
