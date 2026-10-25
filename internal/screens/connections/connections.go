// Package connections is the start screen: saved connections plus a form that
// accepts either a URL or individual fields.
package connections

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"dbdeck/internal/brand"
	"dbdeck/internal/config"
	"dbdeck/internal/demo"
	"dbdeck/internal/driver"
	"dbdeck/internal/events"
	"dbdeck/internal/theme"
	"dbdeck/internal/widgets"
)

type mode int

const (
	modeList mode = iota
	modeForm
	modeConnecting
)

type connectResult struct {
	attempt int
	prof    config.Profile
	drv     driver.Driver
	err     error
	test    bool
}
type tickMsg struct{}

type Model struct {
	profiles []config.Profile
	s        widgets.Scroller
	mode     mode
	form     *form
	w, h     int

	status    string
	statusErr bool

	cancel   context.CancelFunc
	attempt  int
	target   config.Profile
	fromForm bool
	ticking  bool
	spin     int

	overlay widgets.Overlay

	lastClick time.Time
	lastRow   int
}

func New() *Model {
	m := &Model{s: widgets.NewScroller(), form: newForm()}
	m.Reload()
	return m
}

// Reload re-reads the saved profiles from disk.
func (m *Model) Reload() {
	list, err := config.Load()
	if err != nil {
		m.setStatus(err.Error(), true)
	}
	m.profiles = list
	m.s.SetTotal(len(list))
	m.mode = modeList
}

func (m *Model) setStatus(s string, isErr bool) { m.status, m.statusErr = s, isErr }

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) layout() (leftW, rightW, paneH int) {
	leftW = min(max(m.w/3, 28), 46)
	return leftW, m.w - leftW, m.h - 2
}

func (m *Model) selected() (config.Profile, bool) {
	if m.s.Cursor < 0 || m.s.Cursor >= len(m.profiles) {
		return config.Profile{}, false
	}
	return m.profiles[m.s.Cursor], true
}

// ---- connecting ------------------------------------------------------------------

func tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// Connect opens a connection for p (also used for the command-line shortcut).
func (m *Model) Connect(p config.Profile) tea.Cmd { return m.startConnect(p, false, false) }

func (m *Model) startConnect(p config.Profile, test, demoDB bool) tea.Cmd {
	m.fromForm = m.mode == modeForm
	m.attempt++
	attempt := m.attempt
	m.mode = modeConnecting
	m.target = p
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.setStatus("", false)
	connect := func() tea.Msg {
		drv, err := driver.Open(ctx, p)
		if err == nil && demoDB {
			if err = demo.Seed(drv); err != nil {
				drv.Close()
				drv = nil
			}
		}
		return connectResult{attempt: attempt, prof: p, drv: drv, err: err, test: test}
	}
	if m.ticking {
		return connect
	}
	m.ticking = true
	return tea.Batch(connect, tick())
}

func (m *Model) onConnected(res connectResult) tea.Cmd {
	if res.attempt != m.attempt || m.mode != modeConnecting { // cancelled meanwhile
		if res.drv != nil {
			res.drv.Close()
		}
		return nil
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if res.err != nil {
		m.mode = modeList
		if m.fromForm {
			m.mode = modeForm
		} else if p := res.prof; p.Engine != config.EngineSQLite && p.Name != "" && p.Password == "" {
			// a saved connection whose password could not be read (no keychain): ask for it
			m.openForm(p, p.Name)
			for i, id := range m.form.visible() {
				if id == fPass {
					m.form.setFocus(i)
				}
			}
			m.setStatus("connection failed: "+res.err.Error()+" — type the password and press enter", true)
			return nil
		}
		m.setStatus("connection failed: "+res.err.Error(), true)
		return nil
	}
	if res.test {
		res.drv.Close()
		m.mode = modeForm
		m.setStatus("✔ connection works", false)
		return nil
	}
	p := res.prof
	if p.Name != "" && m.fromForm {
		stored := p
		stored.Password = ""
		if err := config.Upsert(m.form.oldName, stored); err != nil {
			m.setStatus("could not save connection: "+err.Error(), true)
		} else if err := config.SavePassword(p.Name, p.Password); err != nil {
			m.setStatus("saved, but the password was not stored (no keychain): "+err.Error(), true)
		}
		m.Reload()
	}
	m.mode = modeList
	return func() tea.Msg { return events.ConnectedMsg{Driver: res.drv, Profile: p} }
}

// ---- Update ------------------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		_, _, ph := m.layout()
		m.s.SetHeight(max(ph-3, 1))
		if m.overlay != nil {
			m.overlay.Resize(m.w, m.h)
		}
		return nil
	case tickMsg:
		m.spin++
		if m.mode == modeConnecting {
			return tick()
		}
		m.ticking = false
		return nil
	case connectResult:
		return m.onConnected(msg)
	case widgets.Result:
		m.overlay = nil
		if msg.ID == "del" && msg.Choice == "y" {
			if p, ok := m.selected(); ok {
				if err := config.Remove(p.Name); err != nil {
					m.setStatus(err.Error(), true)
				} else {
					m.setStatus("deleted "+p.Name, false)
				}
				m.Reload()
			}
		}
		return nil
	case tea.KeyMsg:
		if m.mode != modeForm || m.overlay != nil {
			if parts := widgets.SplitRunes(msg); len(parts) > 1 {
				cmds := make([]tea.Cmd, 0, len(parts))
				for _, k := range parts {
					cmds = append(cmds, m.Update(k))
				}
				return tea.Batch(cmds...)
			}
		}
		if m.overlay != nil {
			var cmd tea.Cmd
			m.overlay, cmd = m.overlay.Update(msg)
			return cmd
		}
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	}
	return nil
}

func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	switch m.mode {
	case modeConnecting:
		if k.String() == "esc" || k.String() == "ctrl+c" {
			if m.cancel != nil {
				m.cancel()
			}
			m.mode = modeList
			if m.fromForm {
				m.mode = modeForm
			}
			m.setStatus("cancelled", false)
		}
		return nil
	case modeForm:
		return m.formKey(k)
	}
	return m.listKey(k)
}

func (m *Model) listKey(k tea.KeyMsg) tea.Cmd {
	m.setStatus("", false)
	if m.s.HandleKey(k) {
		return nil
	}
	switch k.String() {
	case "q", "ctrl+c", "esc":
		return tea.Quit
	case "enter":
		p, ok := m.selected()
		if !ok {
			m.setStatus("no saved connections — press n to add one", false)
			return nil
		}
		p.Password = config.LoadPassword(p.Name)
		return m.startConnect(p, false, false)
	case "n":
		m.openForm(config.Profile{}, "")
		m.form.in[fURL].SetValue("") // empty, so a pasted URL is not appended to a default one
		m.form.setFocus(2)           // URL first: paste and go
	case "e":
		p, ok := m.selected()
		if !ok {
			return nil
		}
		p.Password = config.LoadPassword(p.Name)
		m.openForm(p, p.Name)
	case "d":
		if p, ok := m.selected(); ok {
			o := widgets.NewConfirm("del", "Delete connection?", p.Name+"  ("+p.Title()+")", "", "The saved password is removed from the keychain too.")
			o.Resize(m.w, m.h)
			m.overlay = o
		}
	case "D":
		return m.startConnect(config.Profile{Engine: config.EngineSQLite, Path: ":memory:"}, false, true)
	}
	return nil
}

func (m *Model) openForm(p config.Profile, oldName string) {
	m.form = newForm()
	m.form.oldName = oldName
	if p.Engine == "" {
		p = config.Profile{Engine: config.EnginePostgres}
	}
	m.form.load(p)
	m.form.setFocus(0)
	m.mode = modeForm
}

func (m *Model) formKey(k tea.KeyMsg) tea.Cmd {
	m.setStatus("", false)
	switch k.String() {
	case "esc":
		m.mode = modeList
		return nil
	case "ctrl+c":
		return tea.Quit
	case "enter":
		p := m.form.profile()
		if m.form.urlErr != "" {
			m.setStatus("fix the URL first: "+m.form.urlErr, true)
			return nil
		}
		if err := p.Validate(); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		return m.startConnect(p, false, false)
	case "ctrl+t":
		p := m.form.profile()
		if err := p.Validate(); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		return m.startConnect(p, true, false)
	case "ctrl+s":
		p := m.form.profile()
		if p.Name == "" {
			m.setStatus("give the connection a name to save it", true)
			return nil
		}
		if err := p.Validate(); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		stored := p
		stored.Password = ""
		if err := config.Upsert(m.form.oldName, stored); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		msg := "saved " + p.Name
		if err := config.SavePassword(p.Name, p.Password); err != nil {
			msg += " (password not stored: no keychain available)"
		}
		m.Reload()
		for i, q := range m.profiles {
			if q.Name == p.Name {
				m.s.Select(i)
			}
		}
		m.setStatus(msg, false)
		return nil
	}
	cmd, _ := m.form.handleKey(k)
	return cmd
}

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.overlay != nil {
		var cmd tea.Cmd
		m.overlay, cmd = m.overlay.Update(msg)
		return cmd
	}
	if m.mode != modeList || msg.Action != tea.MouseActionPress {
		return nil
	}
	leftW, _, _ := m.layout()
	if msg.X >= leftW {
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.s.Wheel(-3)
	case tea.MouseButtonWheelDown:
		m.s.Wheel(3)
	case tea.MouseButtonLeft:
		idx := m.s.Offset + msg.Y - 1
		if idx < 0 || idx >= len(m.profiles) {
			return nil
		}
		double := idx == m.lastRow && time.Since(m.lastClick) < 400*time.Millisecond
		m.lastClick, m.lastRow = time.Now(), idx
		m.s.Select(idx)
		if double {
			return m.listKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
	}
	return nil
}

// ---- View --------------------------------------------------------------------------

// func (m *Model) View() string {
// 	if m.w == 0 || m.h == 0 {
// 		return ""
// 	}
// 	if m.overlay != nil {
// 		return m.overlay.View(m.w, m.h)
// 	}
// 	if m.w < 60 || m.h < 14 {
// 		return fmt.Sprintf("terminal too small: %d×%d — dbdeck needs at least 60×14", m.w, m.h)
// 	}
// 	if m.mode == modeConnecting {
// 		body := fmt.Sprintf("\n  %s  Connecting to %s …\n\n  %s", widgets.SpinnerFrames[m.spin%len(widgets.SpinnerFrames)],
// 			theme.Title.Render(m.target.Title()), theme.Dim.Render("esc to cancel"))
// 		return widgets.Center(widgets.Pane("Connecting", body, min(60, m.w-4), 7, true), m.w, m.h)
// 	}
// 	leftW, rightW, paneH := m.layout()

// 	// (Feel free to adjust the colors to match your app's theme)
// 	headerStyle := lipgloss.NewStyle().
// 		Bold(true).
// 		Foreground(lipgloss.Color("#FAFAFA")).
// 		Background(lipgloss.Color("#7D56F4")). // A nice purple/blue
// 		Padding(0, 1)

// 	// Formats to: " dbdeck v0.1.0 · a deck of panes for PostgreSQL · MySQL · SQLite · Redis "
// 	header := headerStyle.Render(fmt.Sprintf(" %s v%s · %s ", brand.Name, brand.Version, brand.Tagline))

//		left := widgets.Pane("Connections", m.listView(leftW-2, paneH-2), leftW, paneH, m.mode == modeList)
//		title, body := "Details", m.detailView(rightW-2)
//		if m.mode == modeForm {
//			title = "New connection"
//			if m.form.oldName != "" {
//				title = "Edit connection · " + m.form.oldName
//			}
//			body = m.form.view(rightW-2) + "\n" + m.formHelp()
//		}
//		right := widgets.Pane(title, body, rightW, paneH, m.mode == modeForm)
//		return lipgloss.JoinVertical(lipgloss.Left,
//			header, // Full width header at top
//			lipgloss.JoinHorizontal(lipgloss.Top, left, right), // Panes side-by-side
//			m.statusBar(), // Status bar at bottom
//		)
//	}
func (m *Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.overlay != nil {
		return m.overlay.View(m.w, m.h)
	}
	if m.w < 60 || m.h < 14 {
		return fmt.Sprintf("terminal too small: %d×%d — dbdeck needs at least 60×14", m.w, m.h)
	}
	if m.mode == modeConnecting {
		body := fmt.Sprintf("\n  %s  Connecting to %s …\n\n  %s", widgets.SpinnerFrames[m.spin%len(widgets.SpinnerFrames)],
			theme.Title.Render(m.target.Title()), theme.Dim.Render("esc to cancel"))
		return widgets.Center(widgets.Pane("Connecting", body, min(60, m.w-4), 7, true), m.w, m.h)
	}
	leftW, rightW, paneH := m.layout()

	// Clean, minimal header - no borders
	header := m.renderHeader()

	left := widgets.Pane("Connections", m.listView(leftW-2, paneH-2), leftW, paneH, m.mode == modeList)
	title, body := "Details", m.detailView(rightW-2)
	if m.mode == modeForm {
		title = "New connection"
		if m.form.oldName != "" {
			title = "Edit connection · " + m.form.oldName
		}
		body = m.form.view(rightW-2) + "\n" + m.formHelp()
	}
	right := widgets.Pane(title, body, rightW, paneH, m.mode == modeForm)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		lipgloss.JoinHorizontal(lipgloss.Top, left, right),
		m.statusBar(),
	)
}

func (m *Model) renderHeader() string {
	// Simple, clean header text
	headerText := fmt.Sprintf("%s v%s  •  %s", brand.Name, brand.Version, brand.Tagline)

	// Style it with full width background
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#7D56F4")).
		Width(m.w).
		Align(lipgloss.Center).
		Padding(0, 1).
		Render(headerText)
}

func (m *Model) listView(w, h int) string {
	m.s.SetHeight(max(h-1, 1))
	if len(m.profiles) == 0 {
		return strings.Join([]string{
			"",
			theme.Dim.Render(" No saved connections yet."),
			"",
			theme.Dim.Render(" n  add one (you can paste a URL)"),
			theme.Dim.Render(" D  try the demo database"),
		}, "\n")
	}
	lines := []string{theme.Dim.Render(fmt.Sprintf(" %d saved", len(m.profiles)))}
	start, end := m.s.Range()
	bar := widgets.Scrollbar(m.s.Offset, m.s.Total, max(h-1, 1))
	for i := start; i < end; i++ {
		p := m.profiles[i]
		right := p.Engine
		left := " " + widgets.Clean(p.Title())
		pad := w - 1 - runewidth.StringWidth(left) - runewidth.StringWidth(right) - 1
		text := left
		if pad >= 1 {
			text += strings.Repeat(" ", pad) + right
		}
		text = widgets.Fit(text, w-1)
		if i == m.s.Cursor && m.mode == modeList {
			text = theme.CellSel.Render(text)
		} else if i == m.s.Cursor {
			text = theme.RowSel.Render(text)
		}
		lines = append(lines, text+theme.Dim.Render(bar[i-start]))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) detailView(w int) string {
	var b strings.Builder
	row := func(k, v string) {
		if v != "" {
			b.WriteString("  " + theme.Dim.Render(widgets.Fit(k, 10)) + widgets.Clean(v) + "\n")
		}
	}
	if p, ok := m.selected(); ok {
		q := p.WithDefaults()
		b.WriteString("\n  " + theme.Title.Render(p.Title()) + "\n\n")
		row("engine", q.Engine)
		if q.Engine == config.EngineSQLite {
			row("file", q.Path)
		} else {
			row("host", q.Addr())
			row("user", q.User)
			row("database", q.Database)
			row("tls", q.TLS)
		}
		row("url", q.BuildURL(true))
		b.WriteString("\n")
	}
	b.WriteString("  " + theme.Dim.Render("Passwords live in your OS keychain, never in the config file.") + "\n\n")
	for _, l := range [][2]string{
		{"enter", "connect"}, {"n", "new connection (paste a URL or fill the fields)"},
		{"e", "edit"}, {"d", "delete"}, {"D", "open the demo database"}, {"q", "quit"},
	} {
		b.WriteString("  " + theme.Key.Render(widgets.Fit(l[0], 7)) + l[1] + "\n")
	}
	return b.String()
}

func (m *Model) formHelp() string {
	return "  " + theme.Dim.Render("tab/↓ next · enter connect · ctrl+t test · ctrl+s save · esc back") + "\n" +
		"  " + theme.Dim.Render("Editing the URL fills the fields, and editing a field rewrites the URL.")
}

func (m *Model) statusBar() string {
	hints := " j/k move · enter connect · n new · e edit · d delete · D demo · q quit"
	tag := theme.ModeTag.Render(" CONNECT ")
	if m.mode == modeForm {
		hints = " tab next · enter connect · ctrl+t test · ctrl+s save · esc back"
		tag = theme.InsertTag.Render(" INSERT ")
	}
	right := ""
	style := theme.Bar
	if m.status != "" {
		right = widgets.Clean(m.status)
		if m.statusErr {
			style = style.Foreground(theme.Red)
		} else {
			style = style.Foreground(theme.Green)
		}
	}
	right = runewidth.Truncate(right, max(m.w*2/3, 20), "…")
	tagW := 9
	leftW := max(m.w-tagW-runewidth.StringWidth(right)-2, 0)
	left := runewidth.FillRight(runewidth.Truncate(hints, leftW, "…"), leftW)
	return tag + theme.Bar.Render(left+"  ") + style.Render(right)
}
