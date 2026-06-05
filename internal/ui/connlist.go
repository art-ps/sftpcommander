package ui

import (
	"fmt"

	"github.com/art-ps/sftpcommander/internal/config"
	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type connEntry struct {
	name        string
	host        string
	port        string
	user        string
	keyPath     string
	fromSSH     bool
	fromSaved   bool
}

type openConnectFormMsg struct {
	prefill *ConnectedMsg // nil = empty form
}

type deleteSavedConnMsg struct{ name string }

type ConnListModel struct {
	entries []connEntry
	cursor  int
	offset  int
	width   int
	height  int
	err     string
}

func NewConnListModel() ConnListModel {
	m := ConnListModel{}
	m.reload()
	return m
}

func (m *ConnListModel) reload() {
	var entries []connEntry
	if saved, err := config.LoadConnections(); err == nil {
		for _, c := range saved {
			entries = append(entries, connEntry{
				name: c.Name, host: c.Host, port: c.Port,
				user: c.User, keyPath: c.KeyPath, fromSaved: true,
			})
		}
	} else {
		m.err = "load connections: " + err.Error()
	}
	for _, alias := range sftpclient.ListSSHHosts() {
		e := sftpclient.LookupSSHConfig(alias)
		entries = append(entries, connEntry{
			name: alias, host: e.HostName, port: e.Port,
			user: e.User, keyPath: e.IdentityFile, fromSSH: true,
		})
	}
	m.entries = entries
	if m.cursor >= len(entries) {
		m.cursor = max(0, len(entries)-1)
	}
}

func (m ConnListModel) Init() tea.Cmd { return nil }

func (m ConnListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "n", "+":
			return m, func() tea.Msg { return openConnectFormMsg{} }
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.entries) - 1
		case "R":
			m.reload()
		case "D":
			if len(m.entries) == 0 {
				break
			}
			e := m.entries[m.cursor]
			if !e.fromSaved {
				m.err = "cannot delete ssh-config aliases here"
				return m, nil
			}
			return m, func() tea.Msg { return deleteSavedConnMsg{name: e.name} }
		case "enter", "l", "right":
			if len(m.entries) == 0 {
				// no entries → go to empty form
				return m, func() tea.Msg { return openConnectFormMsg{} }
			}
			e := m.entries[m.cursor]
			prefill := &ConnectedMsg{
				Host:    e.host,
				Port:    e.port,
				User:    e.user,
				KeyPath: e.keyPath,
			}
			if e.fromSSH {
				prefill.ProxyJump = sftpclient.LookupSSHConfig(e.name).ProxyJump
			}
			if prefill.Port == "" {
				prefill.Port = "22"
			}
			return m, func() tea.Msg { return openConnectFormMsg{prefill: prefill} }
		}
	}
	return m, nil
}

func (m ConnListModel) View() string {
	heading := styleTitle.Render("  SFTP Browser — connections  ")

	if len(m.entries) == 0 {
		body := []string{
			heading,
			"",
			styleSubtitle.Render("No saved connections or ~/.ssh/config aliases."),
			"",
			"  " + keyHint("n", "new") + "   " + keyHint("q", "quit"),
		}
		content := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
		if m.width > 0 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
		}
		return content
	}

	rows := []string{heading, ""}
	for i, e := range m.entries {
		tag := ""
		if e.fromSSH {
			tag = lipgloss.NewStyle().Foreground(colorAccent).Render("[ssh]")
		} else if e.fromSaved {
			tag = lipgloss.NewStyle().Foreground(colorSecondary).Render("[saved]")
		}
		nameStyle := lipgloss.NewStyle().Foreground(colorText).Bold(true).Width(24)
		hostStyle := lipgloss.NewStyle().Foreground(colorMuted).Width(36)
		userStyle := lipgloss.NewStyle().Foreground(colorMuted)
		row := lipgloss.JoinHorizontal(lipgloss.Left,
			nameStyle.Render(e.name),
			"  ",
			hostStyle.Render(fmt.Sprintf("%s@%s:%s", emptyDash(e.user), emptyDash(e.host), emptyDash(e.port))),
			"  ",
			userStyle.Render(tag),
		)
		if i == m.cursor {
			row = styleSelected.Width(82).Render(row)
		} else {
			row = styleNormal.Width(82).Render(row)
		}
		rows = append(rows, row)
	}
	if m.err != "" {
		rows = append(rows, "", styleError.Render("  "+m.err))
	}
	rows = append(rows, "",
		"  "+keyHint("↑↓/jk", "nav")+"   "+
			keyHint("↵", "connect")+"   "+
			keyHint("n", "new")+"   "+
			keyHint("D", "delete saved")+"   "+
			keyHint("R", "reload")+"   "+
			keyHint("q", "quit"))

	content := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	if m.width > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
