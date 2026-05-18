package ui

import (
	"sftpbrowser/internal/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type bookmarkSelectedMsg struct{ path string }
type backFromBookmarksMsg struct{}

type BookmarksModel struct {
	host    string
	user    string
	items   []config.Bookmark
	cursor  int
	err     string
	width   int
	height  int
}

func NewBookmarksModel(host, user string) BookmarksModel {
	m := BookmarksModel{host: host, user: user}
	m.reload()
	return m
}

func (m *BookmarksModel) reload() {
	items, err := config.BookmarksForHost(m.host, m.user)
	if err != nil {
		m.err = err.Error()
		return
	}
	m.items = items
	if m.cursor >= len(items) {
		m.cursor = max(0, len(items)-1)
	}
}

func (m BookmarksModel) Init() tea.Cmd { return nil }

func (m BookmarksModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c", "B":
			return m, func() tea.Msg { return backFromBookmarksMsg{} }
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.items) - 1
		case "D":
			if len(m.items) == 0 {
				break
			}
			b := m.items[m.cursor]
			if err := config.DeleteBookmark(b.Host, b.User, b.Path); err != nil {
				m.err = err.Error()
			}
			m.reload()
		case "enter", "l", "right":
			if len(m.items) == 0 {
				break
			}
			b := m.items[m.cursor]
			return m, func() tea.Msg { return bookmarkSelectedMsg{path: b.Path} }
		}
	}
	return m, nil
}

func (m BookmarksModel) View() string {
	heading := styleTitle.Render("  Bookmarks  ")
	subtitle := styleSubtitle.Render(m.user + "@" + m.host)

	if len(m.items) == 0 {
		body := []string{
			heading, subtitle, "",
			styleSubtitle.Render("No bookmarks yet — press 'b' in browser to add one."),
			"",
			"  " + keyHint("esc", "back"),
		}
		content := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
		if m.width > 0 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
		}
		return content
	}

	rows := []string{heading, subtitle, ""}
	for i, b := range m.items {
		label := b.Label
		if label == "" {
			label = "—"
		}
		labelStyle := lipgloss.NewStyle().Foreground(colorText).Width(18)
		pathStyle := lipgloss.NewStyle().Foreground(colorWarning)
		row := lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render(truncate(label, 18)),
			"  ",
			pathStyle.Render(b.Path),
		)
		if i == m.cursor {
			row = styleSelected.Width(70).Render(row)
		} else {
			row = styleNormal.Width(70).Render(row)
		}
		rows = append(rows, row)
	}
	if m.err != "" {
		rows = append(rows, "", styleError.Render("  "+m.err))
	}
	rows = append(rows, "",
		"  "+keyHint("↑↓", "nav")+"   "+
			keyHint("↵", "go")+"   "+
			keyHint("D", "delete")+"   "+
			keyHint("esc", "back"))

	content := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	if m.width > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}
