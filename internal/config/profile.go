// Package config holds connection profiles: the URL <-> fields model, and
// persistence (TOML file for everything except passwords, OS keychain for those).
package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineSQLite   = "sqlite"
	EngineRedis    = "redis"
)

// Engines lists the supported engines in display order.
var Engines = []string{EnginePostgres, EngineMySQL, EngineSQLite, EngineRedis}

// Profile is everything needed to open one connection.
// URL and the individual fields are two views of the same thing.
type Profile struct {
	Name     string `toml:"name"`
	Engine   string `toml:"engine"`
	Host     string `toml:"host,omitempty"`
	Port     int    `toml:"port,omitempty"`
	User     string `toml:"user,omitempty"`
	Password string `toml:"-"`                  // never written to the config file
	Database string `toml:"database,omitempty"` // pg/mysql: db name, redis: db index
	TLS      string `toml:"tls,omitempty"`      // pg: sslmode; mysql: tls param; redis: "on"/"off"
	Path     string `toml:"path,omitempty"`     // sqlite file
}

// DefaultPort returns the standard port for an engine.
func DefaultPort(engine string) int {
	switch engine {
	case EnginePostgres:
		return 5432
	case EngineMySQL:
		return 3306
	case EngineRedis:
		return 6379
	}
	return 0
}

// DefaultTLS returns the default TLS setting for an engine.
func DefaultTLS(engine string) string {
	switch engine {
	case EnginePostgres:
		return "prefer"
	case EngineRedis:
		return "off"
	case EngineMySQL:
		return "preferred"
	}
	return ""
}

// WithDefaults fills empty fields with sensible engine defaults.
func (p Profile) WithDefaults() Profile {
	if p.Engine == "" {
		p.Engine = EnginePostgres
	}
	if p.Engine == EngineSQLite {
		return p
	}
	if p.Host == "" {
		p.Host = "localhost"
	}
	if p.Port == 0 {
		p.Port = DefaultPort(p.Engine)
	}
	if p.TLS == "" {
		p.TLS = DefaultTLS(p.Engine)
	}
	return p
}

// Addr returns host:port (IPv6 safe).
func (p Profile) Addr() string {
	q := p.WithDefaults()
	return net.JoinHostPort(q.Host, strconv.Itoa(q.Port))
}

// Title is a short label for lists and the status bar.
func (p Profile) Title() string {
	if p.Name != "" {
		return p.Name
	}
	if p.Engine == EngineSQLite {
		return p.Path
	}
	return p.Addr()
}

// ParseURL fills a profile from a connection URL (or a sqlite path URL).
// Supported: postgres://, postgresql://, mysql://, redis://, rediss://, sqlite://.
func ParseURL(raw string) (Profile, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Profile{}, fmt.Errorf("empty url")
	}
	if !strings.Contains(raw, "://") {
		if strings.HasPrefix(raw, "file:") {
			return Profile{Engine: EngineSQLite, Path: strings.TrimPrefix(raw, "file:")}, nil
		}
		return Profile{}, fmt.Errorf("missing scheme (expected postgres://, mysql://, redis://, sqlite://)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Profile{}, fmt.Errorf("invalid url: %w", err)
	}
	p := Profile{}
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql", "pg":
		p.Engine = EnginePostgres
	case "mysql", "mariadb":
		p.Engine = EngineMySQL
	case "redis", "rediss":
		p.Engine = EngineRedis
		if strings.ToLower(u.Scheme) == "rediss" {
			p.TLS = "on"
		}
	case "sqlite", "sqlite3", "file":
		p.Engine = EngineSQLite
		p.Path = u.Host + u.Path
		if p.Path == "" {
			return Profile{}, fmt.Errorf("sqlite url needs a path")
		}
		return p, nil
	default:
		return Profile{}, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}

	p.Host = u.Hostname()
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Profile{}, fmt.Errorf("invalid port %q", port)
		}
		p.Port = n
	}
	if u.User != nil {
		p.User = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	p.Database = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	switch p.Engine {
	case EnginePostgres:
		if v := q.Get("sslmode"); v != "" {
			p.TLS = v
		}
	case EngineMySQL:
		if v := q.Get("tls"); v != "" {
			p.TLS = v
		}
	}
	return p.WithDefaults(), nil
}

// BuildURL renders the profile as a URL. When mask is true the password is
// replaced by *** so the string is safe to display.
func (p Profile) BuildURL(mask bool) string {
	p = p.WithDefaults()
	if p.Engine == EngineSQLite {
		return "sqlite://" + p.Path
	}
	scheme := p.Engine
	if p.Engine == EngineRedis && p.TLS == "on" {
		scheme = "rediss"
	}
	u := url.URL{Scheme: scheme, Host: p.Addr()}
	pw := p.Password
	if mask && pw != "" {
		pw = "MASKED"
	}
	switch {
	case p.User != "" && pw != "":
		u.User = url.UserPassword(p.User, pw)
	case p.User != "":
		u.User = url.User(p.User)
	case pw != "" && p.Engine == EngineRedis: // redis allows password-only
		u.User = url.UserPassword("", pw)
	}
	if p.Database != "" {
		u.Path = "/" + p.Database
	}
	q := url.Values{}
	switch p.Engine {
	case EnginePostgres:
		if p.TLS != "" {
			q.Set("sslmode", p.TLS)
		}
	case EngineMySQL:
		if p.TLS != "" && p.TLS != "preferred" {
			q.Set("tls", p.TLS)
		}
	}
	u.RawQuery = q.Encode()
	s := u.String()
	if mask {
		s = strings.ReplaceAll(s, "MASKED", "***")
	}
	return s
}

// Validate checks the profile has what its engine needs.
func (p Profile) Validate() error {
	switch p.Engine {
	case EngineSQLite:
		if strings.TrimSpace(p.Path) == "" {
			return fmt.Errorf("sqlite needs a file path (use :memory: for a scratch database)")
		}
	case EnginePostgres, EngineMySQL, EngineRedis:
		q := p.WithDefaults()
		if q.Host == "" {
			return fmt.Errorf("host is required")
		}
		if q.Port < 1 || q.Port > 65535 {
			return fmt.Errorf("port must be 1-65535")
		}
		if q.Engine == EngineRedis && q.Database != "" {
			if n, err := strconv.Atoi(q.Database); err != nil || n < 0 {
				return fmt.Errorf("redis database must be a number (0, 1, ...)")
			}
		}
	default:
		return fmt.Errorf("unknown engine %q", p.Engine)
	}
	return nil
}
