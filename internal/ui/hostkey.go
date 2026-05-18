package ui

import (
	sftpclient "sftpbrowser/internal/sftp"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type hostKeyDecisionMsg struct {
	accept bool
}

type HostPromptModel struct {
	challenge *sftpclient.UnknownHostKeyError
	width     int
	height    int
}

func NewHostPromptModel(challenge *sftpclient.UnknownHostKeyError) HostPromptModel {
	return HostPromptModel{challenge: challenge}
}

func (m HostPromptModel) Init() tea.Cmd { return nil }

func (m HostPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "Y", "enter":
			return m, func() tea.Msg { return hostKeyDecisionMsg{accept: true} }
		case "n", "N", "esc", "ctrl+c", "q":
			return m, func() tea.Msg { return hostKeyDecisionMsg{accept: false} }
		}
	}
	return m, nil
}

func (m HostPromptModel) View() string {
	if m.challenge == nil {
		return ""
	}

	warning := lipgloss.NewStyle().Foreground(colorWarning).Bold(true).
		Render("⚠  Host key verification")

	labelStyle := lipgloss.NewStyle().Foreground(colorMuted).Width(14)
	fpStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	body := []string{
		warning,
		"",
		"The authenticity of host " + stylePath.Render(m.challenge.Hostname) +
			" (" + lipgloss.NewStyle().Foreground(colorMuted).Render(m.challenge.Address) + ")",
		"cannot be established.",
		"",
		labelStyle.Render("Key type:") + m.challenge.KeyType,
		labelStyle.Render("Fingerprint:") + fpStyle.Render(m.challenge.Fingerprint),
		"",
		"Add this key to " + stylePath.Render("~/.ssh/known_hosts") + " and continue?",
		"",
		"  " + keyHint("y/enter", "accept") + "   " + keyHint("n/esc", "reject"),
	}

	content := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))

	if m.width > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}
