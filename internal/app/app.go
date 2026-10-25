// Package app is the shell: it owns the two screens and moves between them.
package app

import (
	tea "github.com/charmbracelet/bubbletea"

	"dbdeck/internal/config"
	"dbdeck/internal/driver"
	"dbdeck/internal/events"
	"dbdeck/internal/screens/browser"
	"dbdeck/internal/screens/connections"
)

// Options lets the command line skip the connection screen.
type Options struct {
	Profile *config.Profile // connect to this profile at start
	Driver  driver.Driver   // or: an already-open driver (demo)
	DriverP config.Profile  // profile describing Driver
}

type Model struct {
	opts Options
	conn *connections.Model
	br   *browser.Model
	w, h int
}

func New(o Options) *Model { return &Model{opts: o, conn: connections.New()} }

func (m *Model) Init() tea.Cmd {
	switch {
	case m.opts.Driver != nil:
		d, p := m.opts.Driver, m.opts.DriverP
		return func() tea.Msg { return events.ConnectedMsg{Driver: d, Profile: p} }
	case m.opts.Profile != nil:
		return m.conn.Connect(*m.opts.Profile)
	}
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		cmds := []tea.Cmd{m.conn.Update(msg)}
		if m.br != nil {
			cmds = append(cmds, m.br.Update(msg))
		}
		return m, tea.Batch(cmds...)

	case events.ConnectedMsg:
		m.br = browser.New(msg.Driver, msg.Profile)
		cmds := []tea.Cmd{m.br.Init()}
		if m.w > 0 {
			cmds = append(cmds, m.br.Update(tea.WindowSizeMsg{Width: m.w, Height: m.h}))
		}
		return m, tea.Batch(cmds...)

	case events.DisconnectMsg:
		if m.br != nil {
			m.br.Close()
			m.br = nil
		}
		m.conn.Reload()
		return m, nil
	}
	if m.br != nil {
		return m, m.br.Update(msg)
	}
	return m, m.conn.Update(msg)
}

func (m *Model) View() string {
	if m.br != nil {
		return m.br.View()
	}
	return m.conn.View()
}

// Close releases an open connection (call after the program exits).
func (m *Model) Close() {
	if m.br != nil {
		m.br.Close()
		m.br = nil
	}
}
