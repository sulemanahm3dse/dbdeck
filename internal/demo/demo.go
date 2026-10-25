// Package demo seeds a throw-away SQLite database for trying the UI.
package demo

import (
	"context"
	"fmt"
	"strings"

	"dbdeck/internal/driver"
)

// Seed creates sample tables: a small one, a 5,000-row one (to try scrolling),
// a wide one with JSON (to try horizontal scrolling and the inspector), a table
// without a primary key (read-only), and a view.
func Seed(d driver.Driver) error {
	ctx := context.Background()
	var wide []string
	for i := 1; i <= 16; i++ {
		wide = append(wide, fmt.Sprintf("attr_%02d TEXT", i))
	}
	var wideSel []string
	for i := 1; i <= 16; i++ {
		wideSel = append(wideSel, fmt.Sprintf("'value %d for row ' || i", i))
	}
	stmts := []string{
		`CREATE TABLE t_user (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT, vip INTEGER DEFAULT 0)`,
		`INSERT INTO t_user (name, email, vip) VALUES
			('Ann Lee','ann@example.com',1),('Bob Stone','bob@example.com',0),
			('Carol Ng','carol@example.com',0),('Dave Khan','dave@example.com',1),('Erin Cho',NULL,0)`,
		`CREATE TABLE t_order (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES t_user(id), status TEXT, amount REAL, paid INTEGER, note TEXT)`,
		`INSERT INTO t_order (user_id, status, amount, paid, note)
			WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 450)
			SELECT i % 5 + 1, CASE i % 3 WHEN 0 THEN 'done' WHEN 1 THEN 'pending' ELSE 'cancelled' END,
			       round(100 + i * 3.5, 2), i % 2, CASE WHEN i % 7 = 0 THEN 'gift wrap' END FROM n`,
		`CREATE TABLE t_log (id INTEGER PRIMARY KEY, level TEXT, message TEXT, at TEXT)`,
		`INSERT INTO t_log (level, message, at)
			WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 5000)
			SELECT CASE i % 4 WHEN 0 THEN 'ERROR' WHEN 1 THEN 'INFO' WHEN 2 THEN 'WARN' ELSE 'DEBUG' END,
			       'event number ' || i || ' happened in worker ' || (i % 8), datetime('2026-01-01', '+' || i || ' minutes') FROM n`,
		fmt.Sprintf(`CREATE TABLE t_wide (id INTEGER PRIMARY KEY, payload TEXT, %s)`, strings.Join(wide, ", ")),
		fmt.Sprintf(`INSERT INTO t_wide (payload, %s)
			WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 60)
			SELECT '{"id":' || i || ',"tags":["a","b"],"nested":{"ok":true,"score":' || (i * 1.5) || '}}', %s FROM n`,
			attrNames(), strings.Join(wideSel, ", ")),
		`CREATE TABLE t_no_pk (a TEXT, b TEXT)`,
		`INSERT INTO t_no_pk VALUES ('x','y'),('p','q')`,
		`CREATE VIEW v_paid_orders AS SELECT * FROM t_order WHERE paid = 1`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(ctx, s); err != nil {
			return fmt.Errorf("demo seed: %w", err)
		}
	}
	return nil
}

func attrNames() string {
	var out []string
	for i := 1; i <= 16; i++ {
		out = append(out, fmt.Sprintf("attr_%02d", i))
	}
	return strings.Join(out, ", ")
}
