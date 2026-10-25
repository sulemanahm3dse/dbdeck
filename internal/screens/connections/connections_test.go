package connections

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"dbdeck/internal/config"
	_ "dbdeck/internal/driver/sqlite"
	"dbdeck/internal/events"
)

func TestFormURLToFieldsAndBack(t *testing.T) {
	f := newForm()
	f.in[fURL].SetValue("postgres://bob:secret@db.local:5433/shop?sslmode=disable")
	f.syncFieldsFromURL()
	p := f.profile()
	if p.Engine != "postgres" || p.Host != "db.local" || p.Port != 5433 || p.User != "bob" ||
		p.Password != "secret" || p.Database != "shop" || p.TLS != "disable" {
		t.Fatalf("fields from url: %+v", p)
	}
	// editing a field rewrites the URL, masking the password
	f.setFocus(3) // host
	f.in[fHost].SetValue("other.host")
	f.syncURLFromFields()
	if u := f.in[fURL].Value(); !strings.Contains(u, "other.host") || strings.Contains(u, "secret") || !strings.Contains(u, "***") {
		t.Fatalf("url should be rewritten and masked: %s", u)
	}
	// ... and editing the masked URL again must not clobber the real password
	f.in[fURL].SetValue(strings.Replace(f.in[fURL].Value(), "shop", "billing", 1))
	f.syncFieldsFromURL()
	if p := f.profile(); p.Password != "secret" || p.Database != "billing" {
		t.Fatalf("password lost or db not updated: %+v", p)
	}
}

func TestFormBadURLKeepsFields(t *testing.T) {
	f := newForm()
	f.in[fHost].SetValue("keep.me")
	f.in[fURL].SetValue("ftp://nope")
	f.syncFieldsFromURL()
	if f.urlErr == "" || f.in[fHost].Value() != "keep.me" {
		t.Fatalf("err=%q host=%q", f.urlErr, f.in[fHost].Value())
	}
}

func TestEngineSwitchResetsDefaults(t *testing.T) {
	f := newForm()
	f.load(config.Profile{Engine: "postgres"})
	if f.in[fPort].Value() != "5432" {
		t.Fatalf("port %q", f.in[fPort].Value())
	}
	f.changeEngine(1) // mysql
	if f.engineName() != "mysql" || f.in[fPort].Value() != "3306" {
		t.Fatalf("%s %q", f.engineName(), f.in[fPort].Value())
	}
	f.changeEngine(1) // sqlite
	if f.engineName() != "sqlite" || len(f.visible()) != 4 {
		t.Fatalf("sqlite should show only name/engine/url/file, got %v", f.visible())
	}
	f.changeEngine(1) // redis
	if f.engineName() != "redis" || f.in[fPort].Value() != "6379" || f.tlsChoices()[f.tls] != "off" {
		t.Fatalf("redis defaults: port=%q tls=%v", f.in[fPort].Value(), f.tlsChoices()[f.tls])
	}
	f.in[fPort].SetValue("7000") // custom port survives engine switch
	f.changeEngine(1)
	if f.in[fPort].Value() != "7000" {
		t.Fatal("custom port should be kept")
	}
}

type h struct {
	t         *testing.T
	m         *Model
	connected *events.ConnectedMsg
}

func (x *h) send(msg tea.Msg) {
	x.t.Helper()
	queue := []tea.Msg{msg}
	for i := 0; i < 100 && len(queue) > 0; i++ {
		msg := queue[0]
		queue = queue[1:]
		if c, ok := msg.(events.ConnectedMsg); ok {
			x.connected = &c
			continue
		}
		queue = append(queue, run(x.m.Update(msg))...)
	}
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			out = append(out, run(c)...)
		}
		return out
	case tickMsg:
		return nil
	default:
		return []tea.Msg{v}
	}
}

func (x *h) typ(s string)      { x.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }
func (x *h) key(k tea.KeyType) { x.send(tea.KeyMsg{Type: k}) }
func (x *h) plain() string     { return ansi.Strip(x.m.View()) }

func TestSaveListAndConnectFlow(t *testing.T) {
	t.Setenv("dbdeck_CONFIG_DIR", t.TempDir())
	db := filepath.Join(t.TempDir(), "x.db")
	if err := os.WriteFile(db, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	x := &h{t: t, m: New()}
	x.send(tea.WindowSizeMsg{Width: 110, Height: 30})
	if !strings.Contains(x.plain(), "No saved connections") {
		t.Fatalf("empty state missing:\n%s", x.plain())
	}

	x.typ("n") // new form, focus on URL
	x.typ("sqlite://" + db)
	if p := x.m.form.profile(); p.Engine != "sqlite" || p.Path != db {
		t.Fatalf("URL should select sqlite: %+v", p)
	}
	x.key(tea.KeyCtrlS) // no name → refused
	if !x.m.statusErr {
		t.Fatal("saving without a name must fail")
	}
	x.m.form.setFocus(0)
	x.typ("scratch")
	x.key(tea.KeyCtrlS)
	if !strings.Contains(x.plain(), "scratch") {
		t.Fatalf("saved connection should be listed:\n%s", x.plain())
	}
	list, _ := config.Load()
	if len(list) != 1 || list[0].Name != "scratch" || list[0].Path != db {
		t.Fatalf("persisted: %+v", list)
	}

	// back on the list: enter connects and announces the driver
	x.m.mode = modeList
	x.key(tea.KeyEnter)
	if x.connected == nil || x.connected.Profile.Name != "scratch" {
		t.Fatalf("expected a ConnectedMsg, status=%q", x.m.status)
	}
	x.connected.Driver.Close()
}

func TestTestConnectionAndFailure(t *testing.T) {
	t.Setenv("dbdeck_CONFIG_DIR", t.TempDir())
	x := &h{t: t, m: New()}
	x.send(tea.WindowSizeMsg{Width: 110, Height: 30})
	x.typ("n")
	x.typ("sqlite:///definitely/not/here.db")
	x.key(tea.KeyCtrlT)
	if !x.m.statusErr || x.m.mode != modeForm {
		t.Fatalf("failed test should return to the form with an error: mode=%v status=%q", x.m.mode, x.m.status)
	}
	if x.connected != nil {
		t.Fatal("must not connect")
	}
	x.key(tea.KeyEsc)
	if x.m.mode != modeList {
		t.Fatal("esc leaves the form")
	}
}

func TestDemoAndRendering(t *testing.T) {
	t.Setenv("dbdeck_CONFIG_DIR", t.TempDir())
	for _, sz := range [][2]int{{60, 14}, {80, 24}, {140, 40}} {
		x := &h{t: t, m: New()}
		x.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		check := func(label string) {
			t.Helper()
			lines := strings.Split(x.m.View(), "\n")
			if len(lines) != sz[1] {
				t.Fatalf("%v %s: %d lines", sz, label, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != sz[0] {
					t.Fatalf("%v %s: line %d is %d wide\n%s", sz, label, i, w, ansi.Strip(x.m.View()))
				}
			}
		}
		check("list")
		x.typ("n")
		check("form")
		x.typ("postgres://u:p@h:1/d")
		check("form filled")
		x.key(tea.KeyEsc)
		x.typ("D")
		if x.connected == nil {
			t.Fatalf("demo should connect, status=%q", x.m.status)
		}
		x.connected.Driver.Close()
	}
}
