// Package theme holds the colours and lipgloss styles shared by every screen.
package theme

import "github.com/charmbracelet/lipgloss"

var (
	Accent = lipgloss.Color("#e89a5b")
	Green  = lipgloss.Color("#b8cc52")
	Yellow = lipgloss.Color("#e5c07b")
	Red    = lipgloss.Color("#e5645a")
	Muted  = lipgloss.Color("#7c7466")
	SelBg  = lipgloss.Color("#4a4538")
	BarBg  = lipgloss.Color("#2b2824")
	Fg     = lipgloss.Color("#d8d2c4")
	Black  = lipgloss.Color("#000000")
)

var (
	Dim       = lipgloss.NewStyle().Foreground(Muted)
	Title     = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	Header    = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	RowSel    = lipgloss.NewStyle().Background(SelBg)
	CellSel   = lipgloss.NewStyle().Background(Accent).Foreground(Black)
	Null      = lipgloss.NewStyle().Foreground(Muted).Italic(true)
	Err       = lipgloss.NewStyle().Foreground(Red)
	Ok        = lipgloss.NewStyle().Foreground(Green)
	Warn      = lipgloss.NewStyle().Foreground(Yellow)
	Bar       = lipgloss.NewStyle().Background(BarBg).Foreground(Fg)
	ModeTag   = lipgloss.NewStyle().Background(Green).Foreground(Black).Bold(true)
	InsertTag = lipgloss.NewStyle().Background(Accent).Foreground(Black).Bold(true)
	TxTag     = lipgloss.NewStyle().Background(Yellow).Foreground(Black).Bold(true)
	Key       = lipgloss.NewStyle().Foreground(Yellow).Bold(true)
	BorderOn  = lipgloss.NewStyle().Foreground(Green)
	BorderOff = lipgloss.NewStyle().Foreground(Muted)
)
