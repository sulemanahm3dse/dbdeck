// Package mysql registers the MySQL / MariaDB engine.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	"dbdeck/internal/driver/sqldb"
)

func init() { driver.Register(config.EngineMySQL, open) }

func open(ctx context.Context, p config.Profile) (driver.Driver, error) {
	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = p.Addr()
	cfg.User = p.User
	cfg.Passwd = p.Password
	cfg.DBName = p.Database
	cfg.ParseTime = true
	cfg.ClientFoundRows = true // RowsAffected = matched rows, so no-op updates are not "0 rows"
	cfg.Timeout = 10 * time.Second
	switch strings.ToLower(p.TLS) {
	case "", "preferred":
		cfg.TLSConfig = "preferred"
	case "disable", "false", "off":
		cfg.TLSConfig = "false"
	case "skip-verify":
		cfg.TLSConfig = "skip-verify"
	default:
		cfg.TLSConfig = "true"
	}
	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if p.Password != "" {
			return nil, fmt.Errorf("%s", strings.ReplaceAll(err.Error(), p.Password, "***"))
		}
		return nil, err
	}
	return sqldb.New(config.EngineMySQL, fmt.Sprintf("mysql · %s@%s/%s", p.User, p.Addr(), p.Database), db, dialect{}), nil
}

type dialect struct{}

func (dialect) Quote(s string) string           { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func (dialect) Placeholder(int) string          { return "?" }
func (dialect) ExplainPrefix() string           { return "EXPLAIN " }
func (dialect) DefaultValues(ref string) string { return "INSERT INTO " + ref + " () VALUES ()" }

func (d dialect) TableRef(n driver.Node) string {
	if len(n.Path) >= 2 {
		return d.Quote(n.Path[0]) + "." + d.Quote(n.Path[1])
	}
	return d.Quote(n.Path[0])
}

func (dialect) Roots(ctx context.Context, q sqldb.Queryer) ([]driver.Node, error) {
	rows, err := q.QueryContext(ctx, `SELECT schema_name FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema','performance_schema','mysql','sys') ORDER BY 1`)
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
		out = append(out, driver.Node{ID: "d:" + s, Name: s, Kind: driver.KindDatabase, Path: []string{s}})
	}
	return out, rows.Err()
}

func (dialect) Children(ctx context.Context, q sqldb.Queryer, parent driver.Node) ([]driver.Node, error) {
	rows, err := q.QueryContext(ctx, `SELECT table_name, table_type FROM information_schema.tables
		WHERE table_schema = ? ORDER BY table_type DESC, table_name`, parent.Path[0])
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
		if strings.Contains(strings.ToUpper(typ), "VIEW") {
			k = driver.KindView
		}
		out = append(out, driver.Node{ID: "t:" + parent.Path[0] + "." + name, Name: name, Kind: k, Path: []string{parent.Path[0], name}})
	}
	return out, rows.Err()
}

func (dialect) PrimaryKey(ctx context.Context, q sqldb.Queryer, n driver.Node) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT column_name FROM information_schema.key_column_usage
		WHERE table_schema = ? AND table_name = ? AND constraint_name = 'PRIMARY'
		ORDER BY ordinal_position`, n.Path[0], n.Path[1])
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
