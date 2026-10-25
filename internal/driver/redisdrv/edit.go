package redisdrv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"dbdeck/internal/driver"
)

var errNull = errors.New("redis has no NULL — enter a value")

func (s *DB) keyType(ctx context.Context, n driver.Node) (*redis.Client, string, string, error) {
	db, err := strconv.Atoi(n.Path[0])
	if err != nil {
		return nil, "", "", err
	}
	c := s.client(db)
	t, err := c.Type(ctx, n.Path[1]).Result()
	return c, n.Path[1], t, err
}

func (s *DB) CanEdit(ctx context.Context, n driver.Node) (bool, string) {
	if n.Kind != driver.KindKey {
		return false, "select a key first"
	}
	_, _, t, err := s.keyType(ctx, n)
	if err != nil {
		return false, err.Error()
	}
	switch t {
	case "string", "hash", "list", "set", "zset":
		return true, ""
	case "none":
		return false, "key no longer exists"
	}
	return false, fmt.Sprintf("%s keys are read-only here", t)
}

func (s *DB) UpdateCell(ctx context.Context, n driver.Node, cols []driver.Column, row []driver.Cell, col int, v driver.Cell) (string, error) {
	if v.Null {
		return "", errNull
	}
	c, key, t, err := s.keyType(ctx, n)
	if err != nil {
		return "", err
	}
	switch t {
	case "string":
		if err := c.Do(ctx, "SET", key, v.V, "KEEPTTL").Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("SET %s %q KEEPTTL", key, v.V), nil
	case "hash":
		if col != 1 {
			return "", errors.New("field names cannot be edited — delete the row and add a new one")
		}
		if err := c.HSet(ctx, key, row[0].V, v.V).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("HSET %s %s %q", key, row[0].V, v.V), nil
	case "list":
		if col != 1 {
			return "", errors.New("list indexes cannot be edited")
		}
		idx, err := strconv.ParseInt(row[0].V, 10, 64)
		if err != nil {
			return "", err
		}
		if err := c.LSet(ctx, key, idx, v.V).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("LSET %s %d %q", key, idx, v.V), nil
	case "set":
		if _, err := c.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.SRem(ctx, key, row[0].V)
			p.SAdd(ctx, key, v.V)
			return nil
		}); err != nil {
			return "", err
		}
		return fmt.Sprintf("SREM %s %q; SADD %s %q", key, row[0].V, key, v.V), nil
	case "zset":
		if col != 1 {
			return "", errors.New("members cannot be renamed — delete the row and add a new one")
		}
		score, err := strconv.ParseFloat(v.V, 64)
		if err != nil {
			return "", fmt.Errorf("score must be a number: %w", err)
		}
		if err := c.ZAddXX(ctx, key, redis.Z{Score: score, Member: row[0].V}).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("ZADD %s XX %s %q", key, v.V, row[0].V), nil
	}
	return "", fmt.Errorf("%s keys are read-only here", t)
}

func (s *DB) DeleteRow(ctx context.Context, n driver.Node, cols []driver.Column, row []driver.Cell) (string, error) {
	c, key, t, err := s.keyType(ctx, n)
	if err != nil {
		return "", err
	}
	var cmd *redis.IntCmd
	var show string
	switch t {
	case "string":
		cmd, show = c.Del(ctx, key), "DEL "+key
	case "hash":
		cmd, show = c.HDel(ctx, key, row[0].V), fmt.Sprintf("HDEL %s %s", key, row[0].V)
	case "list":
		cmd, show = c.LRem(ctx, key, 1, row[1].V), fmt.Sprintf("LREM %s 1 %q", key, row[1].V)
	case "set":
		cmd, show = c.SRem(ctx, key, row[0].V), fmt.Sprintf("SREM %s %q", key, row[0].V)
	case "zset":
		cmd, show = c.ZRem(ctx, key, row[0].V), fmt.Sprintf("ZREM %s %q", key, row[0].V)
	default:
		return "", fmt.Errorf("%s keys are read-only here", t)
	}
	if err := cmd.Err(); err != nil {
		return "", err
	}
	if cmd.Val() == 0 {
		return "", errors.New("nothing was removed (already gone?)")
	}
	return show, nil
}

func (s *DB) InsertRow(ctx context.Context, n driver.Node, cols []driver.Column, vals map[string]driver.Cell) (string, error) {
	c, key, t, err := s.keyType(ctx, n)
	if err != nil {
		return "", err
	}
	need := func(name string) (string, error) {
		v, ok := vals[name]
		if !ok || strings.TrimSpace(v.V) == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return v.V, nil
	}
	switch t {
	case "hash":
		f, err := need("field")
		if err != nil {
			return "", err
		}
		v := vals["value"].V
		if err := c.HSet(ctx, key, f, v).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("HSET %s %s %q", key, f, v), nil
	case "list":
		v, err := need("value")
		if err != nil {
			return "", err
		}
		if err := c.RPush(ctx, key, v).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("RPUSH %s %q", key, v), nil
	case "set":
		m, err := need("member")
		if err != nil {
			return "", err
		}
		if err := c.SAdd(ctx, key, m).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("SADD %s %q", key, m), nil
	case "zset":
		m, err := need("member")
		if err != nil {
			return "", err
		}
		sc, err := need("score")
		if err != nil {
			return "", err
		}
		score, err := strconv.ParseFloat(sc, 64)
		if err != nil {
			return "", fmt.Errorf("score must be a number: %w", err)
		}
		if err := c.ZAdd(ctx, key, redis.Z{Score: score, Member: m}).Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("ZADD %s %s %q", key, sc, m), nil
	}
	return "", fmt.Errorf("cannot add rows to a %s key — edit its value instead", t)
}
