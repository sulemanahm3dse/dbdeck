package widgets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"dbdeck/internal/driver"
	"dbdeck/internal/theme"
)

// Overlay is a modal screen drawn over the whole window.
type Overlay interface {
	Update(msg tea.Msg) (Overlay, tea.Cmd)
	View(w, h int) string
	Resize(w, h int)
}

// Result is sent when an overlay closes. Choice is the option key, or "" if cancelled.
type Result struct {
	ID     string
	Choice string
	Data   any
}

func done(id, choice string, data any) tea.Cmd {
	return func() tea.Msg { return Result{ID: id, Choice: choice, Data: data} }
}

func box(title, body string, bw, bh int) string {
	return Pane(title, body, bw, bh, true)
}

// ---- Menu ------------------------------------------------------------------

type MenuOption struct{ Key, Label string }

// Menu shows scrollable text and a row of single-key options (also used for confirmations).
type Menu struct {
	id, title string
	lines     []string
	opts      []MenuOption
	s         Scroller
	w, h      int
}

func NewMenu(id, title string, lines []string, opts []MenuOption) *Menu {
	m := &Menu{id: id, title: title, lines: lines, opts: opts, s: NewScroller()}
	m.s.SetTotal(len(lines))
	return m
}

func NewConfirm(id, title string, lines ...string) *Menu {
	return NewMenu(id, title, lines, []MenuOption{{"y", "yes"}, {"n", "no"}})
}

func (m *Menu) Resize(w, h int) { m.w, m.h = w, h }

func (m *Menu) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		k := msg.String()
		if k == "esc" || k == "ctrl+c" {
			return m, done(m.id, "", nil)
		}
		for _, o := range m.opts {
			if k == o.Key {
				return m, done(m.id, o.Key, nil)
			}
		}
		if k == "enter" && len(m.opts) > 0 && m.opts[0].Key == "y" {
			return m, done(m.id, "y", nil)
		}
		m.s.HandleKey(msg)
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.s.Wheel(-3)
		case tea.MouseButtonWheelDown:
			m.s.Wheel(3)
		}
	}
	return m, nil
}

func (m *Menu) View(w, h int) string {
	maxW := 0
	for _, l := range m.lines {
		maxW = max(maxW, runewidth.StringWidth(l))
	}
	var keys []string
	for _, o := range m.opts {
		keys = append(keys, theme.Key.Render("["+o.Key+"]")+" "+o.Label)
	}
	keys = append(keys, theme.Dim.Render("[esc] cancel"))
	footer := strings.Join(keys, "   ")
	bw := min(max(maxW+4, runewidth.StringWidth(m.title)+8, lipgloss.Width(footer)+4, 36), max(w-4, 20))
	visible := min(len(m.lines), max(h-8, 1))
	m.s.SetHeight(visible)
	start, end := m.s.Range()
	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(" " + Fit(m.lines[i], bw-4) + "\n")
	}
	if len(m.lines) > visible {
		b.WriteString(theme.Dim.Render(fmt.Sprintf(" … %d–%d of %d (j/k to scroll)", start+1, end, len(m.lines))) + "\n")
	}
	b.WriteString("\n " + footer)
	bh := strings.Count(b.String(), "\n") + 3
	return Center(box(m.title, b.String(), bw, bh), w, h)
}

// ---- Inspector -------------------------------------------------------------

// Inspector shows one long value in a scrollable viewport (JSON is pretty-printed).
type Inspector struct {
	id, title string
	text      string
	canEdit   bool
	vp        viewport.Model
	w, h      int
}

func NewInspector(id, title, text string, canEdit bool) *Inspector {
	var buf bytes.Buffer
	if t := strings.TrimSpace(text); (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		if json.Indent(&buf, []byte(t), "", "  ") == nil {
			text = buf.String()
		}
	}
	return &Inspector{id: id, title: title, text: text, canEdit: canEdit, vp: viewport.New(40, 10)}
}

func (i *Inspector) Resize(w, h int) {
	i.w, i.h = w, h
	i.vp.Width = max(w-8, 20)
	i.vp.Height = max(h-8, 3)
	i.vp.SetContent(lipgloss.NewStyle().Width(i.vp.Width).Render(strings.ReplaceAll(i.text, "\t", "    ")))
}

func (i *Inspector) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "q", "enter":
			return i, done(i.id, "", nil)
		case "y":
			return i, done(i.id, "copy", i.text)
		case "e":
			if i.canEdit {
				return i, done(i.id, "edit", nil)
			}
		case "g", "home":
			i.vp.GotoTop()
			return i, nil
		case "G", "end":
			i.vp.GotoBottom()
			return i, nil
		}
	}
	var cmd tea.Cmd
	i.vp, cmd = i.vp.Update(msg)
	return i, cmd
}

func (i *Inspector) View(w, h int) string {
	if i.w != w || i.h != h {
		i.Resize(w, h)
	}
	hints := "j/k scroll · y copy · esc close"
	if i.canEdit {
		hints = "j/k scroll · y copy · e edit · esc close"
	}
	body := i.vp.View() + "\n" + theme.Dim.Render(fmt.Sprintf(" %3.0f%%  %s", i.vp.ScrollPercent()*100, hints))
	return Center(box(i.title, body, i.vp.Width+2, i.vp.Height+3), w, h)
}

// ---- Form (add row) --------------------------------------------------------

type FormField struct{ Name, Type string }

// Form collects one value per column. Empty fields are left out (database default).
type Form struct {
	id, title string
	fields    []FormField
	vals      []string
	nulls     []bool
	cur       int
	ti        textinput.Model
	s         Scroller
	w, h      int
}

func NewForm(id, title string, fields []FormField) *Form {
	f := &Form{id: id, title: title, fields: fields, vals: make([]string, len(fields)), nulls: make([]bool, len(fields)), s: NewScroller()}
	f.ti = textinput.New()
	f.ti.Prompt = ""
	f.ti.Focus()
	f.s.SetTotal(len(fields))
	return f
}

func (f *Form) Resize(w, h int) { f.w, f.h = w, h }

func (f *Form) save() { f.vals[f.cur] = f.ti.Value() }

func (f *Form) goTo(i int) {
	f.save()
	f.cur = min(max(i, 0), len(f.fields)-1)
	f.s.Select(f.cur)
	f.ti.SetValue(f.vals[f.cur])
	f.ti.CursorEnd()
}

func (f *Form) submit() tea.Cmd {
	f.save()
	out := map[string]driver.Cell{}
	for i, fl := range f.fields {
		switch {
		case f.nulls[i]:
			out[fl.Name] = driver.NullCell()
		case f.vals[i] != "":
			out[fl.Name] = driver.Text(f.vals[i])
		}
	}
	return done(f.id, "ok", out)
}

func (f *Form) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if len(f.fields) == 0 {
		return f, done(f.id, "", nil)
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			return f, done(f.id, "", nil)
		case "ctrl+s":
			return f, f.submit()
		case "enter":
			if f.cur == len(f.fields)-1 {
				return f, f.submit()
			}
			f.goTo(f.cur + 1)
			return f, nil
		case "tab", "down":
			f.goTo(f.cur + 1)
			return f, nil
		case "shift+tab", "up":
			f.goTo(f.cur - 1)
			return f, nil
		case "pgdown":
			f.goTo(f.cur + max(f.s.Height-1, 1))
			return f, nil
		case "pgup":
			f.goTo(f.cur - max(f.s.Height-1, 1))
			return f, nil
		case "ctrl+n":
			f.nulls[f.cur] = !f.nulls[f.cur]
			return f, nil
		}
		f.nulls[f.cur] = false
	}
	if m, ok := msg.(tea.MouseMsg); ok {
		switch m.Button {
		case tea.MouseButtonWheelUp:
			f.goTo(f.cur - 1)
		case tea.MouseButtonWheelDown:
			f.goTo(f.cur + 1)
		}
		return f, nil
	}
	var cmd tea.Cmd
	f.ti, cmd = f.ti.Update(msg)
	return f, cmd
}

func (f *Form) View(w, h int) string {
	nameW := 0
	for _, fl := range f.fields {
		nameW = max(nameW, runewidth.StringWidth(fl.Name))
	}
	nameW = min(nameW, 24)
	bw := min(max(60, nameW+30), max(w-4, 30))
	visible := min(len(f.fields), max(h-9, 3))
	f.s.SetHeight(visible)
	f.ti.Width = max(bw-nameW-16, 8)
	start, end := f.s.Range()
	var b strings.Builder
	for i := start; i < end; i++ {
		fl := f.fields[i]
		label := Fit(fl.Name, nameW) + " " + theme.Dim.Render(Fit(fl.Type, 8))
		var val string
		switch {
		case i == f.cur && f.nulls[i]:
			val = theme.Null.Render("NULL")
		case i == f.cur:
			val = f.ti.View()
		case f.nulls[i]:
			val = theme.Null.Render("NULL")
		case f.vals[i] == "":
			val = theme.Dim.Render("(default)")
		default:
			val = f.vals[i]
		}
		marker := "  "
		if i == f.cur {
			marker = theme.Title.Render("▸ ")
		}
		b.WriteString(marker + label + " " + val + "\n")
	}
	b.WriteString("\n" + theme.Dim.Render(" tab/↓ next · enter next/submit · ctrl+s submit · ctrl+n NULL · esc cancel"))
	bh := strings.Count(b.String(), "\n") + 3
	return Center(box(f.title, b.String(), bw, bh), w, h)
}
