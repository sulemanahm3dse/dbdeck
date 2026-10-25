package app

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"dbdeck/internal/config"
	"dbdeck/internal/demo"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/sqlite"
)

func run(m *Model, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for i := 0; i < 300 && len(queue) > 0; i++ {
		msg := queue[0]
		queue = queue[1:]
		_, cmd := m.Update(msg)
		queue = append(queue, expand(cmd)...)
	}
}

func expand(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			out = append(out, expand(c)...)
		}
		return out
	case tea.QuitMsg:
		return nil
	default:
		if strings.HasSuffix(strings.ToLower(typeName(v)), "tickmsg") {
			return nil
		}
		return []tea.Msg{v}
	}
}

func typeName(v any) string { return strings.TrimPrefix(strings.TrimPrefix(sprintT(v), "*"), "main.") }
func sprintT(v any) string  { return fmt.Sprintf("%T", v) }

func TestDemoToBrowserAndBack(t *testing.T) {
	t.Setenv("dbdeck_CONFIG_DIR", t.TempDir())
	p := config.Profile{Engine: config.EngineSQLite, Path: ":memory:"}
	d, err := driver.Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := demo.Seed(d); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Driver: d, DriverP: p})
	run(m, tea.WindowSizeMsg{Width: 120, Height: 36})
	for _, msg := range expand(m.Init()) {
		run(m, msg)
	}
	screen := ansi.Strip(m.View())
	for _, want := range []string{"t_order", "t_log", "t_wide", "v_paid_orders", "Explorer"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("browser should list %s:\n%s", want, screen)
		}
	}
	// open the 5,000-row log table and scroll to its end
	run(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	run(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t_log")})
	run(m, tea.KeyMsg{Type: tea.KeyEnter})
	run(m, tea.KeyMsg{Type: tea.KeyEnter})
	run(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if s := ansi.Strip(m.View()); !strings.Contains(s, "5,000") {
		t.Fatalf("expected the last of 5,000 rows:\n%s", s)
	}
	// disconnect returns to the connection screen
	run(m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if s := ansi.Strip(m.View()); !strings.Contains(s, "Connections") {
		t.Fatalf("expected the connections screen:\n%s", s)
	}
}
