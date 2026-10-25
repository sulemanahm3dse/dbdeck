package browser

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"dbdeck/internal/driver"
	"dbdeck/internal/theme"
	"dbdeck/internal/widgets"
)

func (m *Model) tooSmall() bool { return m.w < 60 || m.h < 16 }

func (m *Model) View() string {
	if m.overlay != nil {
		return m.overlay.View(m.w, m.h)
	}
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.tooSmall() {
		return fmt.Sprintf("terminal too small: %d×%d — dbdeck needs at least 60×16", m.w, m.h)
	}
	sp := m.spinner()
	left := widgets.Pane("1 Explorer", m.ex.view(m.treeW-2, m.paneH-2, m.focus == paneExplorer, sp),
		m.treeW, m.paneH, m.focus == paneExplorer)
	top := widgets.Pane("2 Data", m.data.view(m.focus == paneData, sp),
		m.rightW, m.dataH, m.focus == paneData)
	bottom := widgets.Pane("3 Editor", m.query.view(m.rightW-2, m.queryH-2, m.focus == paneQuery),
		m.rightW, m.queryH, m.focus == paneQuery)
	right := lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, left, right), m.statusBar())
}

func (m *Model) statusBar() string {
	inserting := m.focus == paneQuery ||
		(m.focus == paneData && (m.data.editing || m.data.filtering)) ||
		(m.focus == paneExplorer && m.ex.filtering)
	tag, tagStyle := " NORMAL ", theme.ModeTag
	if inserting {
		tag, tagStyle = " INSERT ", theme.InsertTag
	}

	var hints string
	switch {
	case m.focus == paneQuery:
		hints = "ctrl+r run · ctrl+e explain · ctrl+t new tab · alt+↑/↓ history · tab complete · esc back"
	case m.focus == paneData && m.data.editing:
		hints = "enter apply · esc cancel · ctrl+n toggle NULL"
	case m.focus == paneData && m.data.filtering, m.focus == paneExplorer && m.ex.filtering:
		hints = "enter apply · esc cancel"
	case m.focus == paneData:
		hints = "hjkl move · s sort · / filter · enter inspect · e edit · a add · x del · T tx · ? help"
	default:
		hints = "j/k move · enter open · / filter · r reload · tab next pane · T tx · ? help · q quit"
	}

	rightStyle := theme.Bar
	var right string
	txTag := ""
	tx, isTx := m.drv.(driver.Transactor)
	if isTx && tx.InTx() {
		txTag = fmt.Sprintf(" TX %d pending ", len(tx.TxLog()))
	}
	switch {
	case m.status != "":
		right = widgets.Clean(m.status)
		if m.statusErr {
			rightStyle = rightStyle.Foreground(theme.Red)
		} else {
			rightStyle = rightStyle.Foreground(theme.Green)
		}
	default:
		right = m.drv.Title()
		if isTx && txTag == "" {
			right = "tx auto · " + right
		}
	}
	tagW := runewidth.StringWidth(tag)
	txW := runewidth.StringWidth(txTag)
	right = runewidth.Truncate(right, max(m.w/2, 20), "…")
	rightW := runewidth.StringWidth(right)
	leftW := max(m.w-tagW-txW-rightW-2, 0)
	left := runewidth.FillRight(runewidth.Truncate(" "+hints, leftW, "…"), leftW)
	out := tagStyle.Render(tag)
	if txTag != "" {
		out += theme.TxTag.Render(txTag)
	}
	return out + theme.Bar.Render(left+"  ") + rightStyle.Render(right)
}

// ---- mouse ---------------------------------------------------------------------

func (m *Model) region(x, y int) (p pane, lx, ly int, ok bool) {
	switch {
	case y >= m.paneH || x < 0 || y < 0:
		return 0, 0, 0, false
	case x < m.treeW:
		return paneExplorer, x, y, true
	case y < m.dataH:
		return paneData, x - m.treeW, y, true
	default:
		return paneQuery, x - m.treeW, y - m.dataH, true
	}
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.overlay != nil {
		var cmd tea.Cmd
		m.overlay, cmd = m.overlay.Update(msg)
		return cmd
	}
	if m.tooSmall() || msg.Action != tea.MouseActionPress {
		return nil
	}
	p, lx, ly, ok := m.region(msg.X, msg.Y)
	if !ok {
		return nil
	}
	const wheelStep = 3
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		d := wheelStep
		if msg.Button == tea.MouseButtonWheelUp {
			d = -wheelStep
		}
		switch p {
		case paneExplorer:
			m.ex.s.Wheel(d)
		case paneData:
			if msg.Shift {
				m.data.grid.MoveCol(d / wheelStep)
				return nil
			}
			m.data.grid.S.Wheel(d)
			return m.maybeLoadMore()
		case paneQuery:
			t := m.query.tab()
			k := tea.KeyMsg{Type: tea.KeyDown}
			if d < 0 {
				k = tea.KeyMsg{Type: tea.KeyUp}
			}
			for i := 0; i < wheelStep; i++ {
				t.ta, _ = t.ta.Update(k)
			}
		}
	case tea.MouseButtonWheelLeft:
		if p == paneData {
			m.data.grid.MoveCol(-1)
		}
	case tea.MouseButtonWheelRight:
		if p == paneData {
			m.data.grid.MoveCol(1)
		}
	case tea.MouseButtonLeft:
		return m.click(p, lx, ly)
	}
	return nil
}

func (m *Model) click(p pane, lx, ly int) tea.Cmd {
	m.setStatus("", false)
	if m.focus != p && (m.data.editing || m.data.filtering || m.ex.filtering) {
		m.data.editing, m.data.filtering, m.ex.filtering = false, false, false
	}
	m.setFocus(p)
	double := false
	switch p {
	case paneExplorer:
		idx := m.ex.s.Offset + ly - 2
		if idx < 0 || idx >= len(m.ex.vis) {
			return nil
		}
		double = idx == m.lastClickRow && time.Since(m.lastClickAt) < 400*time.Millisecond
		m.lastClickAt, m.lastClickRow = time.Now(), idx
		m.ex.s.Select(idx)
		if double {
			return m.explorerKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
	case paneData:
		row, col, ok := m.data.grid.CellAt(lx-1, ly-2)
		if !ok {
			return nil
		}
		double = row == m.lastClickRow && time.Since(m.lastClickAt) < 400*time.Millisecond
		m.lastClickAt, m.lastClickRow = time.Now(), row
		m.data.grid.S.Select(row)
		m.data.grid.Col = col
		if double {
			return m.dataKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
		return m.maybeLoadMore()
	}
	return nil
}

// Close releases the driver; safe to call once.
func (m *Model) Close() error {
	if m.cancelData != nil {
		m.cancelData()
	}
	if m.cancelQuery != nil {
		m.cancelQuery()
	}
	return m.drv.Close()
}
