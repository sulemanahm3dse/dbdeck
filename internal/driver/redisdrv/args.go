package redisdrv

import (
	"fmt"
	"strings"
)

// SplitArgs splits a console line into arguments, like redis-cli:
// whitespace separated, with "double" (backslash escapes) and 'single' quotes.
func SplitArgs(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	has := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			if has {
				args = append(args, cur.String())
				cur.Reset()
				has = false
			}
		case c == '"':
			has = true
			i++
			closed := false
			for ; i < len(rs); i++ {
				if rs[i] == '\\' && i+1 < len(rs) {
					i++
					switch rs[i] {
					case 'n':
						cur.WriteRune('\n')
					case 't':
						cur.WriteRune('\t')
					case 'r':
						cur.WriteRune('\r')
					default:
						cur.WriteRune(rs[i])
					}
					continue
				}
				if rs[i] == '"' {
					closed = true
					break
				}
				cur.WriteRune(rs[i])
			}
			if !closed {
				return nil, fmt.Errorf("unterminated double quote")
			}
		case c == '\'':
			has = true
			i++
			closed := false
			for ; i < len(rs); i++ {
				if rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '\'' {
					i++
					cur.WriteRune('\'')
					continue
				}
				if rs[i] == '\'' {
					closed = true
					break
				}
				cur.WriteRune(rs[i])
			}
			if !closed {
				return nil, fmt.Errorf("unterminated single quote")
			}
		default:
			has = true
			cur.WriteRune(c)
		}
	}
	if has {
		args = append(args, cur.String())
	}
	return args, nil
}

// commands is used for console completion hints.
var commands = []string{
	"APPEND", "AUTH", "BITCOUNT", "BLPOP", "BRPOP", "CLIENT", "CONFIG GET", "DBSIZE", "DECR", "DECRBY",
	"DEL", "DUMP", "ECHO", "EVAL", "EXISTS", "EXPIRE", "EXPIREAT", "FLUSHDB", "GET", "GETDEL", "GETEX",
	"GETSET", "HDEL", "HEXISTS", "HGET", "HGETALL", "HINCRBY", "HKEYS", "HLEN", "HMGET", "HMSET", "HSCAN",
	"HSET", "HSETNX", "HVALS", "INCR", "INCRBY", "INCRBYFLOAT", "INFO", "KEYS", "LINDEX", "LINSERT", "LLEN",
	"LPOP", "LPUSH", "LRANGE", "LREM", "LSET", "LTRIM", "MGET", "MSET", "PERSIST", "PEXPIRE", "PING", "PTTL",
	"PUBLISH", "RENAME", "RPOP", "RPUSH", "SADD", "SCAN", "SCARD", "SELECT", "SET", "SETEX", "SETNX",
	"SISMEMBER", "SMEMBERS", "SPOP", "SREM", "SSCAN", "STRLEN", "TTL", "TYPE", "UNLINK", "XADD", "XLEN",
	"XRANGE", "ZADD", "ZCARD", "ZCOUNT", "ZINCRBY", "ZRANGE", "ZRANGEBYSCORE", "ZRANK", "ZREM", "ZREVRANGE",
	"ZSCAN", "ZSCORE", "COMMAND DOCS", "MEMORY USAGE", "OBJECT ENCODING",
}
