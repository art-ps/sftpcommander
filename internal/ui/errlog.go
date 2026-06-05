package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const errLogCap = 50

type errLogEntry struct {
	ts  time.Time
	src string
	msg string
}

type errReportMsg struct {
	src string
	msg string
}

type openErrLogMsg struct{}
type backFromErrLogMsg struct{}

type ErrLogModel struct {
	entries []errLogEntry
	cursor  int
	offset  int
	width   int
	height  int
}

func NewErrLogModel(entries []errLogEntry) ErrLogModel {
	m := ErrLogModel{entries: entries}
	if len(entries) > 0 {
		m.cursor = len(entries) - 1
	}
	return m
}

func (m ErrLogModel) Init() tea.Cmd { return nil }

func (m ErrLogModel) visibleRows() int {
	// chrome: title(1) + footer(2) + panel border(2)
	r := m.height - 6
	if r < 3 {
		r = 3
	}
	return r
}

func (m ErrLogModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "enter", "E", "ctrl+c":
			return m, func() tea.Msg { return backFromErrLogMsg{} }
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
				vis := m.visibleRows()
				if m.cursor >= m.offset+vis {
					m.offset = m.cursor - vis + 1
				}
			}
		case "g":
			m.cursor = 0
			m.offset = 0
		case "G":
			m.cursor = max(0, len(m.entries)-1)
			vis := m.visibleRows()
			if m.cursor >= vis {
				m.offset = m.cursor - vis + 1
			}
		case "pgup":
			vis := m.visibleRows()
			m.cursor -= vis
			if m.cursor < 0 {
				m.cursor = 0
			}
			if m.cursor < m.offset {
				m.offset = m.cursor
			}
		case "pgdown":
			vis := m.visibleRows()
			m.cursor += vis
			if m.cursor > len(m.entries)-1 {
				m.cursor = len(m.entries) - 1
			}
			if m.cursor >= m.offset+vis {
				m.offset = m.cursor - vis + 1
			}
		}
	}
	return m, nil
}

func (m ErrLogModel) View() string {
	if m.width == 0 {
		return ""
	}
	title := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(
		fmt.Sprintf("Error log (%d)", len(m.entries)),
	)
	var lines []string
	if len(m.entries) == 0 {
		lines = append(lines, styleSubtitle.Render("  no errors recorded"))
	} else {
		vis := m.visibleRows()
		end := m.offset + vis
		if end > len(m.entries) {
			end = len(m.entries)
		}
		for i := m.offset; i < end; i++ {
			e := m.entries[i]
			ts := e.ts.Format("15:04:05")
			src := e.src
			if src == "" {
				src = "?"
			}
			row := fmt.Sprintf("%s  %-10s  %s", ts, src, oneLine(e.msg))
			row = truncate(row, max(20, m.width-8))
			if i == m.cursor {
				lines = append(lines, styleSelected.Render(row))
			} else {
				lines = append(lines, styleNormal.Render(row))
			}
		}
	}
	body := lipgloss.JoinVertical(lipgloss.Left, lines...)
	footer := "  " + keyHint("↑↓/jk", "scroll") + "   " + keyHint("g/G", "top/end") +
		"   " + keyHint("esc/q", "back")
	panel := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", footer))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panel)
}

// oneLine flattens a multi-line error message so each entry occupies a single
// row in the log view. Long errors get truncated by the caller with truncate().
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
