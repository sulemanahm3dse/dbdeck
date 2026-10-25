package widgets

import "testing"

func sc(total, height int) Scroller {
	s := NewScroller()
	s.Height = height
	s.SetTotal(total)
	return s
}

func TestScrolloffKeepsContext(t *testing.T) {
	s := sc(100, 10)
	for i := 0; i < 7; i++ {
		s.Move(1)
	}
	if s.Cursor != 7 || s.Offset != 0 {
		t.Fatalf("cursor=%d offset=%d", s.Cursor, s.Offset)
	}
	s.Move(1) // cursor 8 is within 2 of the bottom row (9): viewport must start scrolling
	if s.Cursor != 8 || s.Offset != 1 {
		t.Fatalf("cursor=%d offset=%d", s.Cursor, s.Offset)
	}
}

func TestBoundsAndJumps(t *testing.T) {
	s := sc(100, 10)
	s.Bottom()
	if s.Cursor != 99 || s.Offset != 90 {
		t.Fatalf("bottom: %d %d", s.Cursor, s.Offset)
	}
	s.Move(50)
	if s.Cursor != 99 {
		t.Fatal("must clamp at the end")
	}
	s.Top()
	s.Move(-5)
	if s.Cursor != 0 || s.Offset != 0 {
		t.Fatal("must clamp at the start")
	}
	s.Page(1)
	if s.Cursor != 9 {
		t.Fatalf("page: %d", s.Cursor)
	}
	s.Half(1)
	if s.Cursor != 14 {
		t.Fatalf("half: %d", s.Cursor)
	}
}

func TestWheelPullsCursorIntoView(t *testing.T) {
	s := sc(100, 10)
	s.Wheel(30)
	if s.Offset != 30 || s.Cursor < 32 || s.Cursor > 37 {
		t.Fatalf("offset=%d cursor=%d", s.Offset, s.Cursor)
	}
	s.Wheel(1000)
	if s.Offset != 90 || s.Cursor < 92 {
		t.Fatalf("offset=%d cursor=%d", s.Offset, s.Cursor)
	}
	s.Wheel(-1000)
	if s.Offset != 0 || s.Cursor > 7 {
		t.Fatalf("offset=%d cursor=%d", s.Offset, s.Cursor)
	}
}

func TestShortAndEmptyLists(t *testing.T) {
	s := sc(3, 10)
	s.Bottom()
	if s.Cursor != 2 || s.Offset != 0 {
		t.Fatalf("%d %d", s.Cursor, s.Offset)
	}
	if a, b := s.Range(); a != 0 || b != 3 {
		t.Fatalf("range %d-%d", a, b)
	}
	e := sc(0, 10)
	e.Move(5)
	e.Bottom()
	e.Wheel(3)
	if e.Cursor != 0 || e.Offset != 0 {
		t.Fatal("empty list must stay at 0")
	}
}

func TestShrinkingTotalReclamps(t *testing.T) {
	s := sc(100, 10)
	s.Bottom()
	s.SetTotal(20)
	if s.Cursor != 19 || s.Offset != 10 {
		t.Fatalf("%d %d", s.Cursor, s.Offset)
	}
}

func TestNearEnd(t *testing.T) {
	s := sc(100, 10)
	if s.NearEnd(30) {
		t.Fatal("not near end at top")
	}
	s.Select(75)
	if !s.NearEnd(30) {
		t.Fatal("expected near end")
	}
}

func TestScrollbar(t *testing.T) {
	bar := Scrollbar(0, 100, 10)
	if bar[0] != "█" || bar[9] != "│" {
		t.Fatalf("%v", bar)
	}
	bar = Scrollbar(90, 100, 10)
	if bar[9] != "█" || bar[0] != "│" {
		t.Fatalf("%v", bar)
	}
	for _, c := range Scrollbar(0, 5, 10) {
		if c != " " {
			t.Fatal("no bar when everything fits")
		}
	}
}
