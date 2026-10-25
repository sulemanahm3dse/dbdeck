package widgets

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"dbdeck/internal/keymap"
)

// Scroller is the one scrolling model every list, tree and grid shares.
// It is pure state: no rendering, so it is easy to test.
type Scroller struct {
	Total     int // number of items
	Height    int // visible rows
	Cursor    int // selected item
	Offset    int // first visible item
	ScrollOff int // rows kept between cursor and viewport edge
}

// NewScroller returns a scroller with the default scrolloff of 2.
func NewScroller() Scroller { return Scroller{ScrollOff: 2} }

func (s *Scroller) so() int {
	so := s.ScrollOff
	if m := (s.Height - 1) / 2; so > m {
		so = m
	}
	if so < 0 {
		so = 0
	}
	return so
}

func (s *Scroller) maxOffset() int {
	if m := s.Total - s.Height; m > 0 {
		return m
	}
	return 0
}

func (s *Scroller) clamp() {
	if s.Total <= 0 {
		s.Total, s.Cursor, s.Offset = 0, 0, 0
		return
	}
	if s.Cursor < 0 {
		s.Cursor = 0
	}
	if s.Cursor > s.Total-1 {
		s.Cursor = s.Total - 1
	}
	if s.Offset < 0 {
		s.Offset = 0
	}
	if m := s.maxOffset(); s.Offset > m {
		s.Offset = m
	}
}

// follow scrolls the viewport just enough to keep the cursor visible with scrolloff.
func (s *Scroller) follow() {
	s.clamp()
	if s.Height <= 0 {
		return
	}
	so := s.so()
	if s.Cursor < s.Offset+so {
		s.Offset = s.Cursor - so
	}
	if last := s.Offset + s.Height - 1 - so; s.Cursor > last {
		s.Offset = s.Cursor - (s.Height - 1 - so)
	}
	s.clamp()
}

func (s *Scroller) SetTotal(n int)  { s.Total = n; s.follow() }
func (s *Scroller) SetHeight(h int) { s.Height = h; s.follow() }
func (s *Scroller) Move(d int)      { s.Cursor += d; s.follow() }
func (s *Scroller) Top()            { s.Cursor = 0; s.follow() }
func (s *Scroller) Bottom()         { s.Cursor = s.Total - 1; s.follow() }
func (s *Scroller) Select(i int)    { s.Cursor = i; s.follow() }

// Page moves by a full page (dir = ±1); Half by half a page.
func (s *Scroller) Page(dir int) { s.Move(dir * max(s.Height-1, 1)) }
func (s *Scroller) Half(dir int) { s.Move(dir * max(s.Height/2, 1)) }

// Wheel scrolls the viewport by d rows (mouse wheel) and pulls the cursor
// back inside it, like a pager would.
func (s *Scroller) Wheel(d int) {
	s.Offset += d
	s.clamp()
	so := s.so()
	lo, hi := s.Offset+so, s.Offset+s.Height-1-so
	if s.Offset == 0 {
		lo = 0
	}
	if s.Offset >= s.maxOffset() {
		hi = s.Total - 1
	}
	if lo > hi {
		lo = hi
	}
	if s.Cursor < lo {
		s.Cursor = lo
	}
	if s.Cursor > hi {
		s.Cursor = hi
	}
	s.clamp()
}

// Range returns the half-open interval of visible items.
func (s *Scroller) Range() (start, end int) {
	start = s.Offset
	end = s.Offset + s.Height
	if end > s.Total {
		end = s.Total
	}
	if start > end {
		start = end
	}
	return
}

// NearEnd reports whether the cursor is within n items of the last one.
func (s *Scroller) NearEnd(n int) bool { return s.Total > 0 && s.Cursor >= s.Total-1-n }

// HandleKey applies the shared vertical-scroll bindings; it reports whether it consumed the key.
func (s *Scroller) HandleKey(msg tea.KeyMsg) bool {
	switch {
	case key.Matches(msg, keymap.Scroll.Down):
		s.Move(1)
	case key.Matches(msg, keymap.Scroll.Up):
		s.Move(-1)
	case key.Matches(msg, keymap.Scroll.HalfDown):
		s.Half(1)
	case key.Matches(msg, keymap.Scroll.HalfUp):
		s.Half(-1)
	case key.Matches(msg, keymap.Scroll.PageDown):
		s.Page(1)
	case key.Matches(msg, keymap.Scroll.PageUp):
		s.Page(-1)
	case key.Matches(msg, keymap.Scroll.Top):
		s.Top()
	case key.Matches(msg, keymap.Scroll.Bottom):
		s.Bottom()
	default:
		return false
	}
	return true
}

// Scrollbar returns height strings ("█" thumb / "│" track, blank when everything fits).
func Scrollbar(offset, total, height int) []string {
	out := make([]string, max(height, 0))
	if height <= 0 {
		return out
	}
	if total <= height {
		for i := range out {
			out[i] = " "
		}
		return out
	}
	thumb := height * height / total
	if thumb < 1 {
		thumb = 1
	}
	pos := 0
	if span := total - height; span > 0 {
		pos = offset * (height - thumb) / span
	}
	for i := range out {
		if i >= pos && i < pos+thumb {
			out[i] = "█"
		} else {
			out[i] = "│"
		}
	}
	return out
}

// SplitRunes breaks a multi-rune key event (several keys that arrived in one
// terminal read, e.g. key repeat under load) into single-key events. Pasted
// text is left alone. Use it everywhere except while typing into an input.
func SplitRunes(k tea.KeyMsg) []tea.KeyMsg {
	if k.Type != tea.KeyRunes || len(k.Runes) <= 1 || k.Paste {
		return []tea.KeyMsg{k}
	}
	out := make([]tea.KeyMsg, len(k.Runes))
	for i, r := range k.Runes {
		out[i] = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: k.Alt}
	}
	return out
}
