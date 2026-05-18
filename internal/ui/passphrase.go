package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type passphraseEnteredMsg struct {
	passphrase string
}

type passphraseCanceledMsg struct{}

type PassphraseModel struct {
	input   textinput.Model
	keyPath string
	bad     bool
	width   int
	height  int
}

func NewPassphraseModel(keyPath string, bad bool) PassphraseModel {
	in := textinput.New()
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.CharLimit = 512
	in.Width = 40
	in.Prompt = ""
	in.Focus()
	return PassphraseModel{
		input:   in,
		keyPath: keyPath,
		bad:     bad,
	}
}

func (m PassphraseModel) Init() tea.Cmd { return textinput.Blink }

func (m PassphraseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			pass := m.input.Value()
			return m, func() tea.Msg { return passphraseEnteredMsg{passphrase: pass} }
		case "esc", "ctrl+c":
			return m, func() tea.Msg { return passphraseCanceledMsg{} }
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m PassphraseModel) View() string {
	heading := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).
		Render("🔑  Encrypted private key")

	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1)

	body := []string{
		heading,
		"",
		styleSubtitle.Render("Key:") + " " + stylePath.Render(m.keyPath),
		"",
		styleSubtitle.Render("Passphrase:"),
		inputStyle.Render(m.input.View()),
	}
	if m.bad {
		body = append(body, "",
			lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("  Incorrect passphrase — try again"),
		)
	}
	body = append(body, "",
		"  "+keyHint("enter", "unlock")+"   "+keyHint("esc", "cancel"),
	)

	content := stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
	if m.width > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}
