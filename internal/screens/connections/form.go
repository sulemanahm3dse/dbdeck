package connections

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dbdeck/internal/config"
	"dbdeck/internal/theme"
	"dbdeck/internal/widgets"
)

type fieldID int

const (
	fName fieldID = iota
	fEngine
	fURL
	fHost
	fPort
	fUser
	fPass
	fDB
	fTLS
	fPath
)

var fieldLabels = map[fieldID]string{
	fName: "Name", fEngine: "Engine", fURL: "URL", fHost: "Host", fPort: "Port",
	fUser: "User", fPass: "Password", fDB: "Database", fTLS: "TLS", fPath: "File",
}

var tlsOptions = map[string][]string{
	config.EnginePostgres: {"disable", "allow", "prefer", "require", "verify-ca", "verify-full"},
	config.EngineMySQL:    {"preferred", "required", "disable", "skip-verify"},
	config.EngineRedis:    {"off", "on"},
}

// form edits one profile. The URL and the individual fields are two views of it:
// editing either one updates the other.
type form struct {
	in      map[fieldID]*textinput.Model
	engine  int
	tls     int
	focus   int // index into visible()
	urlErr  string
	oldName string // name of the profile being edited ("" when new)
}

func newForm() *form {
	f := &form{in: map[fieldID]*textinput.Model{}}
	for _, id := range []fieldID{fName, fURL, fHost, fPort, fUser, fPass, fDB, fPath} {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 512
		f.in[id] = &ti
	}
	f.in[fPass].EchoMode = textinput.EchoPassword
	f.in[fPass].EchoCharacter = '•'
	f.in[fName].Placeholder = "optional — name it to save the connection"
	f.in[fURL].Placeholder = "paste postgres://user:pass@host:5432/db  ·  mysql://…  ·  redis://…  ·  sqlite:///path"
	f.in[fHost].Placeholder = "localhost"
	f.in[fPort].Placeholder = "default"
	f.in[fPath].Placeholder = "/path/to/file.db  or  :memory:"
	f.setFocus(0)
	return f
}

func (f *form) engineName() string { return config.Engines[f.engine] }

func (f *form) visible() []fieldID {
	if f.engineName() == config.EngineSQLite {
		return []fieldID{fName, fEngine, fURL, fPath}
	}
	v := []fieldID{fName, fEngine, fURL, fHost, fPort, fUser, fPass, fDB, fTLS}
	return v
}

func (f *form) current() fieldID { return f.visible()[f.focus] }

func (f *form) setFocus(i int) {
	vis := f.visible()
	f.focus = (i + len(vis)) % len(vis)
	for id, ti := range f.in {
		if id == vis[f.focus] {
			ti.Focus()
			ti.CursorEnd()
		} else {
			ti.Blur()
		}
	}
}

func (f *form) tlsChoices() []string { return tlsOptions[f.engineName()] }

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// profile builds a Profile from the fields.
func (f *form) profile() config.Profile {
	p := config.Profile{
		Name:     strings.TrimSpace(f.in[fName].Value()),
		Engine:   f.engineName(),
		Host:     strings.TrimSpace(f.in[fHost].Value()),
		User:     f.in[fUser].Value(),
		Password: f.in[fPass].Value(),
		Database: strings.TrimSpace(f.in[fDB].Value()),
		Path:     strings.TrimSpace(f.in[fPath].Value()),
	}
	if n, err := strconv.Atoi(strings.TrimSpace(f.in[fPort].Value())); err == nil {
		p.Port = n
	}
	if ch := f.tlsChoices(); len(ch) > 0 && f.tls < len(ch) {
		p.TLS = ch[f.tls]
	}
	if p.Engine == config.EngineSQLite {
		p.Host, p.User, p.Password, p.Database, p.TLS, p.Port = "", "", "", "", "", 0
	}
	return p
}

// portText is "" when the port is the engine default, so the URL stays short.
func (f *form) load(p config.Profile) {
	p = p.WithDefaults()
	if i := indexOf(config.Engines, p.Engine); i >= 0 {
		f.engine = i
	}
	f.in[fName].SetValue(p.Name)
	f.in[fHost].SetValue(p.Host)
	if p.Port != 0 {
		f.in[fPort].SetValue(strconv.Itoa(p.Port))
	} else {
		f.in[fPort].SetValue("")
	}
	f.in[fUser].SetValue(p.User)
	f.in[fPass].SetValue(p.Password)
	f.in[fDB].SetValue(p.Database)
	f.in[fPath].SetValue(p.Path)
	if i := indexOf(f.tlsChoices(), p.TLS); i >= 0 {
		f.tls = i
	} else {
		f.tls = 0
		if d := indexOf(f.tlsChoices(), config.DefaultTLS(p.Engine)); d >= 0 {
			f.tls = d
		}
	}
	f.urlErr = ""
	f.syncURLFromFields()
	f.setFocus(f.focus)
}

func (f *form) syncURLFromFields() {
	p := f.profile()
	if p.Engine == config.EngineSQLite && p.Path == "" {
		f.in[fURL].SetValue("")
		return
	}
	f.in[fURL].SetValue(p.BuildURL(true))
	f.urlErr = ""
}

func (f *form) syncFieldsFromURL() {
	raw := strings.TrimSpace(f.in[fURL].Value())
	if raw == "" {
		f.urlErr = ""
		return
	}
	p, err := config.ParseURL(raw)
	if err != nil {
		f.urlErr = err.Error()
		return
	}
	keepPass := f.in[fPass].Value()
	name := f.in[fName].Value()
	if p.Password == "***" || p.Password == "" && keepPass != "" && strings.Contains(raw, "***") {
		p.Password = keepPass
	}
	p.Name = name
	url := f.in[fURL].Value()
	f.load(p)
	f.in[fURL].SetValue(url) // never rewrite what the user is typing
	f.urlErr = ""
}

// changeEngine switches engine and resets engine-specific defaults.
func (f *form) changeEngine(d int) {
	old := f.engineName()
	f.engine = (f.engine + d + len(config.Engines)) % len(config.Engines)
	nu := f.engineName()
	if port := strings.TrimSpace(f.in[fPort].Value()); port == "" || port == strconv.Itoa(config.DefaultPort(old)) {
		if dp := config.DefaultPort(nu); dp != 0 {
			f.in[fPort].SetValue(strconv.Itoa(dp))
		} else {
			f.in[fPort].SetValue("")
		}
	}
	f.tls = 0
	if d := indexOf(f.tlsChoices(), config.DefaultTLS(nu)); d >= 0 {
		f.tls = d
	}
	if nu == config.EngineRedis && f.in[fDB].Value() != "" {
		if _, err := strconv.Atoi(f.in[fDB].Value()); err != nil {
			f.in[fDB].SetValue("")
		}
	}
	f.setFocus(f.focus)
	f.syncURLFromFields()
}

// handleKey edits the focused field. It reports whether the key was consumed.
func (f *form) handleKey(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return nil, true
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return nil, true
	}
	switch f.current() {
	case fEngine:
		switch k.String() {
		case "left", "h":
			f.changeEngine(-1)
			return nil, true
		case "right", "l", " ":
			f.changeEngine(1)
			return nil, true
		}
		return nil, true
	case fTLS:
		n := len(f.tlsChoices())
		switch k.String() {
		case "left", "h":
			f.tls = (f.tls - 1 + n) % n
			f.syncURLFromFields()
			return nil, true
		case "right", "l", " ":
			f.tls = (f.tls + 1) % n
			f.syncURLFromFields()
			return nil, true
		}
		return nil, true
	}
	id := f.current()
	ti := f.in[id]
	var cmd tea.Cmd
	*ti, cmd = ti.Update(k)
	if id == fURL {
		f.syncFieldsFromURL()
	} else {
		f.syncURLFromFields()
	}
	return cmd, true
}

func (f *form) view(w int) string {
	labelW := 10
	inW := max(w-labelW-4, 10)
	var b strings.Builder
	for i, id := range f.visible() {
		focused := i == f.focus
		label := fieldLabels[id]
		ls := theme.Dim
		marker := "  "
		if focused {
			ls = theme.Title
			marker = theme.Title.Render("▸ ")
		}
		b.WriteString(marker + ls.Render(widgets.Fit(label, labelW)))
		switch id {
		case fEngine:
			var opts []string
			for j, e := range config.Engines {
				if j == f.engine {
					opts = append(opts, theme.CellSel.Render(" "+e+" "))
				} else {
					opts = append(opts, theme.Dim.Render(" "+e+" "))
				}
			}
			b.WriteString(strings.Join(opts, ""))
			if focused {
				b.WriteString(theme.Dim.Render("  ←/→"))
			}
		case fTLS:
			var opts []string
			for j, o := range f.tlsChoices() {
				if j == f.tls {
					opts = append(opts, theme.CellSel.Render(" "+o+" "))
				} else {
					opts = append(opts, theme.Dim.Render(" "+o+" "))
				}
			}
			b.WriteString(strings.Join(opts, ""))
		default:
			ti := f.in[id]
			ti.Width = inW
			b.WriteString(ti.View())
		}
		b.WriteString("\n")
		if id == fURL && f.urlErr != "" {
			b.WriteString("  " + strings.Repeat(" ", labelW) + theme.Err.Render(widgets.Fit("✖ "+f.urlErr, inW)) + "\n")
		}
	}
	return b.String()
}
