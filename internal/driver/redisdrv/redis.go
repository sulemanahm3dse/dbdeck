// Package redisdrv registers the Redis engine. Keys are shown as a tree
// (grouped on ':'), and every key type is presented as a table.
package redisdrv

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
)

const (
	maxKeys     = 20000 // keys loaded per database for the tree
	maxElements = 10000 // collection members loaded for client-side paging
	maxTypes    = 2000  // TYPE lookups per tree expansion
)

func init() { driver.Register(config.EngineRedis, open) }

type keyCache struct {
	keys      []string // sorted
	truncated bool
}

// DB is a Redis connection. One client is created lazily per logical database.
type DB struct {
	title string
	base  redis.Options
	baseN int

	mu      sync.Mutex
	clients map[int]*redis.Client
	caches  map[int]*keyCache
	cur     int // database the console talks to
}

func open(ctx context.Context, p config.Profile) (driver.Driver, error) {
	n := 0
	if p.Database != "" {
		v, err := strconv.Atoi(p.Database)
		if err != nil {
			return nil, fmt.Errorf("redis database must be a number")
		}
		n = v
	}
	opt := redis.Options{
		Addr:        p.Addr(),
		Username:    p.User,
		Password:    p.Password,
		DB:          n,
		Protocol:    2,
		DialTimeout: 10 * time.Second,
	}
	if p.TLS == "on" {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host}
	}
	d := &DB{
		title:   fmt.Sprintf("redis · %s/%d", p.Addr(), n),
		base:    opt,
		baseN:   n,
		clients: map[int]*redis.Client{},
		caches:  map[int]*keyCache{},
		cur:     n,
	}
	if err := d.client(n).Ping(ctx).Err(); err != nil {
		d.Close()
		if p.Password != "" {
			return nil, errors.New(strings.ReplaceAll(err.Error(), p.Password, "***"))
		}
		return nil, err
	}
	return d, nil
}

// New wraps an address directly (tests).
func New(addr string) (driver.Driver, error) {
	return open(context.Background(), config.Profile{Engine: config.EngineRedis, Host: hostOf(addr), Port: portOf(addr)})
}

func hostOf(a string) string { i := strings.LastIndex(a, ":"); return a[:i] }
func portOf(a string) int    { i := strings.LastIndex(a, ":"); n, _ := strconv.Atoi(a[i+1:]); return n }

func (s *DB) Engine() string { return config.EngineRedis }
func (s *DB) Title() string  { return s.title }

func (s *DB) client(n int) *redis.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[n]; ok {
		return c
	}
	o := s.base
	o.DB = n
	c := redis.NewClient(&o)
	s.clients[n] = c
	return c
}

func (s *DB) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		c.Close()
	}
	s.clients = map[int]*redis.Client{}
	return nil
}

// ---- tree ------------------------------------------------------------------

func (s *DB) Tree(ctx context.Context, parent *driver.Node) ([]driver.Node, error) {
	if parent == nil {
		return s.databases(ctx)
	}
	n, err := strconv.Atoi(parent.Path[0])
	if err != nil {
		return nil, err
	}
	prefix := ""
	if parent.Kind == driver.KindFolder {
		prefix = parent.Path[1]
	} else if _, err := s.loadKeys(ctx, n); err != nil { // expanding a db (re)loads its keys
		return nil, err
	}
	s.mu.Lock()
	cache := s.caches[n]
	s.mu.Unlock()
	if cache == nil {
		if cache, err = s.loadKeys(ctx, n); err != nil {
			return nil, err
		}
	}
	return s.children(ctx, n, cache, prefix)
}

func (s *DB) databases(ctx context.Context) ([]driver.Node, error) {
	counts := map[int]int{s.baseN: 0}
	if info, err := s.client(s.baseN).Info(ctx, "keyspace").Result(); err == nil {
		for _, line := range strings.Split(info, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "db") {
				continue
			}
			name, rest, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			idx, err := strconv.Atoi(strings.TrimPrefix(name, "db"))
			if err != nil {
				continue
			}
			k := 0
			for _, kv := range strings.Split(rest, ",") {
				if v, ok := strings.CutPrefix(kv, "keys="); ok {
					k, _ = strconv.Atoi(v)
				}
			}
			counts[idx] = k
		}
	}
	idxs := make([]int, 0, len(counts))
	for i := range counts {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := make([]driver.Node, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, driver.Node{
			ID: fmt.Sprintf("db%d", i), Name: fmt.Sprintf("db%d", i), Kind: driver.KindDatabase,
			Path: []string{strconv.Itoa(i)}, Detail: fmt.Sprintf("%d keys", counts[i]),
		})
	}
	return out, nil
}

func (s *DB) loadKeys(ctx context.Context, db int) (*keyCache, error) {
	c := s.client(db)
	cache := &keyCache{}
	var cursor uint64
	for {
		keys, next, err := c.Scan(ctx, cursor, "*", 1000).Result()
		if err != nil {
			return nil, err
		}
		cache.keys = append(cache.keys, keys...)
		if len(cache.keys) >= maxKeys {
			cache.keys = cache.keys[:maxKeys]
			cache.truncated = true
			break
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	sort.Strings(cache.keys)
	// SCAN may return duplicates
	out := cache.keys[:0]
	for i, k := range cache.keys {
		if i == 0 || k != cache.keys[i-1] {
			out = append(out, k)
		}
	}
	cache.keys = out
	s.mu.Lock()
	s.caches[db] = cache
	s.mu.Unlock()
	return cache, nil
}

func (s *DB) children(ctx context.Context, db int, cache *keyCache, prefix string) ([]driver.Node, error) {
	keys := cache.keys
	lo := sort.SearchStrings(keys, prefix)
	var folders []driver.Node
	var leaves []driver.Node
	folderIdx := map[string]int{}
	counts := map[string]int{}
	for i := lo; i < len(keys) && strings.HasPrefix(keys[i], prefix); i++ {
		rest := keys[i][len(prefix):]
		if j := strings.Index(rest, ":"); j > 0 {
			seg := rest[:j]
			if _, ok := folderIdx[seg]; !ok {
				folderIdx[seg] = len(folders)
				folders = append(folders, driver.Node{
					ID: fmt.Sprintf("f:%d:%s%s:", db, prefix, seg), Name: seg, Kind: driver.KindFolder,
					Path: []string{strconv.Itoa(db), prefix + seg + ":"},
				})
			}
			counts[seg]++
			continue
		}
		name := rest
		if name == "" {
			name = "(empty)"
		}
		leaves = append(leaves, driver.Node{
			ID: fmt.Sprintf("k:%d:%s", db, keys[i]), Name: name, Kind: driver.KindKey,
			Path: []string{strconv.Itoa(db), keys[i]},
		})
	}
	for i := range folders {
		folders[i].Detail = fmt.Sprintf("%d", counts[folders[i].Name])
	}
	// key types in one round trip (bounded)
	if len(leaves) > 0 {
		lim := len(leaves)
		if lim > maxTypes {
			lim = maxTypes
		}
		cmds := make([]*redis.StatusCmd, lim)
		_, _ = s.client(db).Pipelined(ctx, func(p redis.Pipeliner) error {
			for i := 0; i < lim; i++ {
				cmds[i] = p.Type(ctx, leaves[i].Path[1])
			}
			return nil
		})
		for i, c := range cmds {
			if c != nil {
				leaves[i].Detail = c.Val()
			}
		}
	}
	return append(folders, leaves...), nil
}

// ---- browse ----------------------------------------------------------------

func ttlText(d time.Duration) string {
	switch {
	case d == -1:
		return "no expiry"
	case d < 0:
		return "gone"
	}
	return d.Round(time.Second).String()
}

func contains(s, sub string) bool {
	return sub == "" || strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func window(total, off, lim int) (int, int) {
	if off > total {
		off = total
	}
	end := off + lim
	if end > total {
		end = total
	}
	return off, end
}

func (s *DB) Browse(ctx context.Context, n driver.Node, o driver.BrowseOptions) (driver.Result, error) {
	db, err := strconv.Atoi(n.Path[0])
	if err != nil {
		return driver.Result{}, err
	}
	key := n.Path[1]
	c := s.client(db)
	start := time.Now()
	if o.Limit <= 0 {
		o.Limit = 200
	}

	typ, err := c.Type(ctx, key).Result()
	if err != nil {
		return driver.Result{}, err
	}
	if typ == "none" {
		return driver.Result{}, fmt.Errorf("key %q no longer exists", key)
	}
	ttl := c.TTL(ctx, key).Val()
	meta := fmt.Sprintf("TYPE %s · TTL %s", typ, ttlText(ttl))

	res := driver.Result{Total: -1}
	var total int
	switch typ {
	case "string":
		v, err := c.Get(ctx, key).Result()
		if err != nil {
			return res, err
		}
		res.Columns = []driver.Column{{Name: "value", Type: "string"}}
		res.Rows = [][]driver.Cell{{driver.Text(v)}}
		total = 1
		meta += fmt.Sprintf(" · LEN %d", len(v))

	case "hash":
		m, trunc, err := s.hashAll(ctx, c, key)
		if err != nil {
			return res, err
		}
		fields := make([]string, 0, len(m))
		for f, v := range m {
			if contains(f, o.Where) || contains(v, o.Where) {
				fields = append(fields, f)
			}
		}
		sort.Strings(fields)
		if o.SortCol == "value" {
			sort.SliceStable(fields, func(i, j int) bool { return m[fields[i]] < m[fields[j]] })
		}
		if o.SortDesc {
			for i, j := 0, len(fields)-1; i < j; i, j = i+1, j-1 {
				fields[i], fields[j] = fields[j], fields[i]
			}
		}
		total = len(fields)
		lo, hi := window(total, o.Offset, o.Limit)
		res.Columns = []driver.Column{{Name: "field", Type: "string"}, {Name: "value", Type: "string"}}
		for _, f := range fields[lo:hi] {
			res.Rows = append(res.Rows, []driver.Cell{driver.Text(f), driver.Text(m[f])})
		}
		meta += fmt.Sprintf(" · LEN %d", total)
		if trunc {
			meta += fmt.Sprintf(" (first %d loaded)", maxElements)
		}

	case "list":
		ln, err := c.LLen(ctx, key).Result()
		if err != nil {
			return res, err
		}
		total = int(ln)
		vals, err := c.LRange(ctx, key, int64(o.Offset), int64(o.Offset+o.Limit-1)).Result()
		if err != nil {
			return res, err
		}
		res.Columns = []driver.Column{{Name: "index", Type: "int"}, {Name: "value", Type: "string"}}
		for i, v := range vals {
			res.Rows = append(res.Rows, []driver.Cell{driver.Text(strconv.Itoa(o.Offset + i)), driver.Text(v)})
		}
		meta += fmt.Sprintf(" · LEN %d", total)

	case "set":
		members, trunc, err := s.setAll(ctx, c, key)
		if err != nil {
			return res, err
		}
		var f []string
		for _, m := range members {
			if contains(m, o.Where) {
				f = append(f, m)
			}
		}
		sort.Strings(f)
		if o.SortDesc {
			for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
				f[i], f[j] = f[j], f[i]
			}
		}
		total = len(f)
		lo, hi := window(total, o.Offset, o.Limit)
		res.Columns = []driver.Column{{Name: "member", Type: "string"}}
		for _, m := range f[lo:hi] {
			res.Rows = append(res.Rows, []driver.Cell{driver.Text(m)})
		}
		meta += fmt.Sprintf(" · LEN %d", total)
		if trunc {
			meta += fmt.Sprintf(" (first %d loaded)", maxElements)
		}

	case "zset":
		card, err := c.ZCard(ctx, key).Result()
		if err != nil {
			return res, err
		}
		res.Columns = []driver.Column{{Name: "member", Type: "string"}, {Name: "score", Type: "float"}}
		if strings.TrimSpace(o.Where) != "" {
			zs, err := c.ZRangeWithScores(ctx, key, 0, maxElements-1).Result()
			if err != nil {
				return res, err
			}
			var f []redis.Z
			for _, z := range zs {
				if contains(fmt.Sprint(z.Member), o.Where) {
					f = append(f, z)
				}
			}
			total = len(f)
			lo, hi := window(total, o.Offset, o.Limit)
			for _, z := range f[lo:hi] {
				res.Rows = append(res.Rows, zrow(z))
			}
		} else {
			total = int(card)
			var zs []redis.Z
			if o.SortDesc {
				zs, err = c.ZRevRangeWithScores(ctx, key, int64(o.Offset), int64(o.Offset+o.Limit-1)).Result()
			} else {
				zs, err = c.ZRangeWithScores(ctx, key, int64(o.Offset), int64(o.Offset+o.Limit-1)).Result()
			}
			if err != nil {
				return res, err
			}
			for _, z := range zs {
				res.Rows = append(res.Rows, zrow(z))
			}
		}
		meta += fmt.Sprintf(" · LEN %d", card)

	case "stream":
		ln, _ := c.XLen(ctx, key).Result()
		msgs, err := c.XRangeN(ctx, key, "-", "+", int64(o.Offset+o.Limit)).Result()
		if err != nil {
			return res, err
		}
		total = int(ln)
		res.Columns = []driver.Column{{Name: "id", Type: "string"}, {Name: "fields", Type: "string"}}
		lo, hi := window(len(msgs), o.Offset, o.Limit)
		for _, m := range msgs[lo:hi] {
			keys := make([]string, 0, len(m.Values))
			for k := range m.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%v", k, m.Values[k]))
			}
			res.Rows = append(res.Rows, []driver.Cell{driver.Text(m.ID), driver.Text(strings.Join(parts, " "))})
		}
		meta += fmt.Sprintf(" · LEN %d (read-only)", ln)

	default:
		res.Message = fmt.Sprintf("type %q cannot be displayed yet", typ)
	}

	res.Total = total
	res.Truncated = o.Offset+len(res.Rows) < total
	res.Meta = meta
	res.Elapsed = time.Since(start)
	return res, nil
}

func zrow(z redis.Z) []driver.Cell {
	return []driver.Cell{driver.Text(fmt.Sprint(z.Member)), driver.Text(strconv.FormatFloat(z.Score, 'g', -1, 64))}
}

func (s *DB) hashAll(ctx context.Context, c *redis.Client, key string) (map[string]string, bool, error) {
	n, err := c.HLen(ctx, key).Result()
	if err != nil {
		return nil, false, err
	}
	if n <= maxElements {
		m, err := c.HGetAll(ctx, key).Result()
		return m, false, err
	}
	m := map[string]string{}
	var cursor uint64
	for len(m) < maxElements {
		kv, next, err := c.HScan(ctx, key, cursor, "*", 1000).Result()
		if err != nil {
			return nil, false, err
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		if next == 0 {
			return m, false, nil
		}
		cursor = next
	}
	return m, true, nil
}

func (s *DB) setAll(ctx context.Context, c *redis.Client, key string) ([]string, bool, error) {
	n, err := c.SCard(ctx, key).Result()
	if err != nil {
		return nil, false, err
	}
	if n <= maxElements {
		m, err := c.SMembers(ctx, key).Result()
		return m, false, err
	}
	var all []string
	var cursor uint64
	for len(all) < maxElements {
		ms, next, err := c.SScan(ctx, key, cursor, "*", 1000).Result()
		if err != nil {
			return nil, false, err
		}
		all = append(all, ms...)
		if next == 0 {
			return all, false, nil
		}
		cursor = next
	}
	return all, true, nil
}

// ---- console ---------------------------------------------------------------

func (s *DB) Exec(ctx context.Context, input string) (driver.Result, error) {
	var lines []string
	for _, l := range strings.Split(input, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return driver.Result{}, errors.New("nothing to run")
	}
	start := time.Now()
	var last driver.Result
	for i, l := range lines {
		r, err := s.execLine(ctx, l)
		if err != nil {
			if len(lines) > 1 {
				return last, fmt.Errorf("line %d: %w", i+1, err)
			}
			return last, err
		}
		last = r
	}
	last.Elapsed = time.Since(start)
	if len(lines) > 1 {
		last.Message = fmt.Sprintf("%d commands ok · last: %s", len(lines), last.Message)
	}
	return last, nil
}

func (s *DB) execLine(ctx context.Context, line string) (driver.Result, error) {
	args, err := SplitArgs(line)
	if err != nil {
		return driver.Result{}, err
	}
	switch strings.ToUpper(args[0]) {
	case "SELECT":
		if len(args) != 2 {
			return driver.Result{}, errors.New("usage: SELECT <db index>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 {
			return driver.Result{}, errors.New("db index must be a number")
		}
		if err := s.client(n).Ping(ctx).Err(); err != nil {
			return driver.Result{}, err
		}
		s.mu.Lock()
		s.cur = n
		s.mu.Unlock()
		return driver.Result{Total: -1, Message: fmt.Sprintf("console now uses db%d", n)}, nil
	case "SUBSCRIBE", "PSUBSCRIBE", "MONITOR", "SSUBSCRIBE":
		return driver.Result{}, fmt.Errorf("%s is not supported in the console", strings.ToUpper(args[0]))
	}
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	iargs := make([]any, len(args))
	for i, a := range args {
		iargs[i] = a
	}
	v, err := s.client(cur).Do(ctx, iargs...).Result()
	if errors.Is(err, redis.Nil) {
		return driver.Result{Total: -1, Message: "(nil)"}, nil
	}
	if err != nil {
		return driver.Result{}, err
	}
	return reply(v), nil
}

func reply(v any) driver.Result {
	res := driver.Result{Total: -1}
	switch x := v.(type) {
	case nil:
		res.Message = "(nil)"
	case []any:
		res.Columns = []driver.Column{{Name: "#", Type: "int"}, {Name: "value", Type: "string"}}
		for i, e := range x {
			res.Rows = append(res.Rows, []driver.Cell{driver.Text(strconv.Itoa(i + 1)), driver.Text(render(e))})
		}
		res.Message = fmt.Sprintf("%d items", len(x))
	case map[any]any:
		res.Columns = []driver.Column{{Name: "key", Type: "string"}, {Name: "value", Type: "string"}}
		keys := make([]string, 0, len(x))
		byKey := map[string]any{}
		for k, e := range x {
			ks := render(k)
			keys = append(keys, ks)
			byKey[ks] = e
		}
		sort.Strings(keys)
		for _, k := range keys {
			res.Rows = append(res.Rows, []driver.Cell{driver.Text(k), driver.Text(render(byKey[k]))})
		}
		res.Message = fmt.Sprintf("%d entries", len(x))
	default:
		res.Columns = []driver.Column{{Name: "value", Type: "string"}}
		res.Rows = [][]driver.Cell{{driver.Text(render(v))}}
		res.Message = render(v)
	}
	return res
}

func render(v any) string {
	switch x := v.(type) {
	case nil:
		return "(nil)"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = render(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// Hints completes console command names.
func (s *DB) Hints(prefix string) []string {
	if prefix == "" {
		return nil
	}
	up := strings.ToUpper(prefix)
	var out []string
	for _, c := range commands {
		if strings.HasPrefix(c, up) && c != up {
			out = append(out, c)
		}
	}
	return out
}

// Search returns keys whose name contains text (case-insensitive), across every
// database that has been loaded (at least the connection's own database).
func (s *DB) Search(ctx context.Context, text string, limit int) ([]driver.Node, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	s.mu.Lock()
	var dbs []int
	for n := range s.caches {
		dbs = append(dbs, n)
	}
	s.mu.Unlock()
	if len(dbs) == 0 {
		dbs = []int{s.baseN}
	}
	sort.Ints(dbs)
	var out []driver.Node
	for _, db := range dbs {
		s.mu.Lock()
		cache := s.caches[db]
		s.mu.Unlock()
		if cache == nil {
			var err error
			if cache, err = s.loadKeys(ctx, db); err != nil {
				return nil, err
			}
		}
		start := len(out)
		for _, k := range cache.keys {
			if contains(k, text) {
				out = append(out, driver.Node{
					ID: fmt.Sprintf("k:%d:%s", db, k), Name: k, Kind: driver.KindKey,
					Path: []string{strconv.Itoa(db), k}, Detail: "db" + strconv.Itoa(db),
				})
				if len(out) >= limit {
					break
				}
			}
		}
		found := out[start:]
		if n := min(len(found), maxTypes); n > 0 {
			cmds := make([]*redis.StatusCmd, n)
			_, _ = s.client(db).Pipelined(ctx, func(p redis.Pipeliner) error {
				for i := 0; i < n; i++ {
					cmds[i] = p.Type(ctx, found[i].Path[1])
				}
				return nil
			})
			for i, c := range cmds {
				if c != nil && c.Val() != "" {
					found[i].Detail = c.Val()
				}
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
