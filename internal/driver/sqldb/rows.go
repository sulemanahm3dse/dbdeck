package sqldb

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"dbdeck/internal/driver"
)

func readRows(rows *sql.Rows, limit int) (driver.Result, error) {
	names, err := rows.Columns()
	if err != nil {
		return driver.Result{}, err
	}
	types, _ := rows.ColumnTypes()
	res := driver.Result{Total: -1}
	for i, n := range names {
		t := ""
		if i < len(types) && types[i] != nil {
			t = types[i].DatabaseTypeName()
		}
		res.Columns = append(res.Columns, driver.Column{Name: n, Type: t})
	}
	vals := make([]any, len(names))
	ptrs := make([]any, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if limit > 0 && len(res.Rows) >= limit {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return res, err
		}
		row := make([]driver.Cell, len(names))
		for i, v := range vals {
			row[i] = toCell(v, res.Columns[i].Type)
		}
		res.Rows = append(res.Rows, row)
	}
	return res, rows.Err()
}

func toCell(v any, typ string) driver.Cell {
	switch x := v.(type) {
	case nil:
		return driver.NullCell()
	case string:
		return driver.Text(x)
	case []byte:
		if utf8.Valid(x) {
			return driver.Text(string(x))
		}
		if len(x) > 64 {
			return driver.Text(fmt.Sprintf("\\x%x…(%d bytes)", x[:64], len(x)))
		}
		return driver.Text(fmt.Sprintf("\\x%x", x))
	case bool:
		return driver.Text(strconv.FormatBool(x))
	case int64:
		return driver.Text(strconv.FormatInt(x, 10))
	case float64:
		return driver.Text(strconv.FormatFloat(x, 'g', -1, 64))
	case float32:
		return driver.Text(strconv.FormatFloat(float64(x), 'g', -1, 32))
	case interface{ Format(string) string }: // time.Time
		if strings.EqualFold(typ, "DATE") {
			return driver.Text(x.Format("2006-01-02"))
		}
		return driver.Text(x.Format("2006-01-02 15:04:05.999999Z07:00"))
	default:
		return driver.Text(fmt.Sprint(x))
	}
}
