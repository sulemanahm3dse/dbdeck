// Package postgres registers the PostgreSQL engine (pgx via database/sql).
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	"dbdeck/internal/driver/sqldb"
)

func init() { driver.Register(config.EnginePostgres, open) }

func open(ctx context.Context, p config.Profile) (driver.Driver, error) {
	cfg, err := pgx.ParseConfig(p.BuildURL(false))
	if err != nil {
		return nil, fmt.Errorf("bad connection settings: %w", redact(err, p.Password))
	}
	// Simple protocol sends values as text literals and lets the server coerce
	// them to the column type. That makes inline editing of any column type work
	// and keeps pgbouncer-style poolers happy.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.ConnectTimeout = 10 * time.Second
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "dbdeck"

	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, redact(err, p.Password)
	}
	title := fmt.Sprintf("postgres · %s@%s/%s", cfg.User, p.Addr(), cfg.Database)
	return sqldb.New(config.EnginePostgres, title, db, dialect{}), nil
}

func redact(err error, secret string) error {
	if secret == "" || err == nil {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), secret, "***"))
}

// OpenDB wraps an existing handle (tests).
func OpenDB(title string, db *sql.DB) driver.Driver {
	return sqldb.New(config.EnginePostgres, title, db, dialect{})
}

type dialect struct{}

func (dialect) Quote(s string) string           { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (dialect) Placeholder(i int) string        { return fmt.Sprintf("$%d", i) }
func (dialect) ExplainPrefix() string           { return "EXPLAIN " }
func (dialect) DefaultValues(ref string) string { return "INSERT INTO " + ref + " DEFAULT VALUES" }

func (d dialect) TableRef(n driver.Node) string {
	if len(n.Path) >= 2 {
		return d.Quote(n.Path[0]) + "." + d.Quote(n.Path[1])
	}
	return d.Quote(n.Path[0])
}

func (dialect) Roots(ctx context.Context, q sqldb.Queryer) ([]driver.Node, error) {
	rows, err := q.QueryContext(ctx, `SELECT schema_name FROM information_schema.schemata
		WHERE schema_name NOT IN ('pg_catalog','information_schema','pg_toast')
		  AND schema_name NOT LIKE 'pg_temp_%' AND schema_name NOT LIKE 'pg_toast_temp_%'
		ORDER BY (schema_name = 'public') DESC, schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.Node
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, driver.Node{ID: "s:" + s, Name: s, Kind: driver.KindSchema, Path: []string{s}})
	}
	return out, rows.Err()
}

func (dialect) Children(ctx context.Context, q sqldb.Queryer, parent driver.Node) ([]driver.Node, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.relname,
			CASE WHEN c.relkind IN ('v','m') THEN 'view' ELSE 'table' END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f')
		ORDER BY 2 DESC, 1`, parent.Path[0])
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
		out = append(out, driver.Node{ID: "t:" + parent.Path[0] + "." + name, Name: name, Kind: k, Path: []string{parent.Path[0], name}})
	}
	return out, rows.Err()
}

func (d dialect) PrimaryKey(ctx context.Context, q sqldb.Queryer, n driver.Node) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.attname
		FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey::int2[], a.attnum)`, d.TableRef(n))
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
