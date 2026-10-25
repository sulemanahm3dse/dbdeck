package browser

import (
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	tea "github.com/charmbracelet/bubbletea"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/redisdrv"
)

func TestRedisKeySearchOpensAndEdits(t *testing.T) {
	mr := miniredis.RunT(t)
	for i := 0; i < 30; i++ {
		mr.Set("session:"+string(rune('a'+i%26))+string(rune('a'+i/26)), "v")
	}
	mr.HSet("user:7", "name", "Ann", "age", "30")
	p, _ := config.ParseURL("redis://" + mr.Addr())
	drv, err := driver.Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, m: New(drv, p)}
	t.Cleanup(func() { drv.Close() })
	h.send(tea.WindowSizeMsg{Width: 120, Height: 36})
	for _, msg := range collect(h.m.Init()) {
		h.send(msg)
	}
	h.mustContain("db0")
	h.mustContain("session")

	h.key("/")
	h.typ("user:")
	h.key("enter")
	h.mustContain("1 keys") // “user:” · 1 keys
	h.mustContain("user:7")
	if strings.Contains(h.plain(), "session") {
		t.Fatal("search results replace the tree")
	}
	h.key("enter") // open the found key
	h.mustContain("TYPE hash")
	h.mustContain("Ann")

	h.key("j") // fields are sorted: age, then name
	h.key("l")
	h.key("e")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typ("Annie")
	h.key("enter")
	if v := mr.HGet("user:7", "name"); v != "Annie" {
		t.Fatalf("redis edit not written: %q (status %q)", v, h.m.status)
	}

	// console, with completion hint
	h.key("alt+3")
	h.typ("hgetal")
	if len(h.m.query.tab().hints) == 0 || h.m.query.tab().hints[0] != "HGETALL" {
		t.Fatalf("hints: %v", h.m.query.tab().hints)
	}
	h.key("tab")
	h.typ(" user:7")
	h.key("ctrl+r")
	h.mustContain("Annie")
	h.key("esc")
	h.key("alt+1")
	h.key("esc") // clears the search, tree is back
	h.mustContain("session")
}
