package ui

import (
	"strings"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const logo = `
 ███████╗███████╗████████╗██████╗
 ██╔════╝██╔════╝╚══██╔══╝██╔══██╗
 ███████╗█████╗     ██║   ██████╔╝
 ╚════██║██╔══╝     ██║   ██╔═══╝
 ███████║██║        ██║   ██║
 ╚══════╝╚═╝        ╚═╝   ╚═╝
`

type ConnectField int

const (
	fieldHost ConnectField = iota
	fieldPort
	fieldUser
	fieldPassword
	fieldKeyPath
	fieldCount
)

type ConnectedMsg struct {
	Host          string
	Port          string
	User          string
	Password      string
	KeyPath       string
	KeyPassphrase string
	ProxyJump     string
}

type sshConfigMsg struct {
	entry sftpclient.SSHConfigEntry
}

type ConnectModel struct {
	inputs    [fieldCount]textinput.Model
	focused   ConnectField
	err       string
	width     int
	height    int
	sshFilled [fieldCount]bool // true when a field was set from ~/.ssh/config
	proxyJump string           // captured from ssh_config lookup; not user-editable
}

func NewConnectModel() ConnectModel {
	labels := []string{"Host", "Port", "Username", "Password", "SSH Key Path (optional)"}
	placeholders := []string{"192.168.1.1", "22", "admin", "••••••••", "~/.ssh/id_rsa"}

	var inputs [fieldCount]textinput.Model
	for i := range inputs {
		t := textinput.New()
		t.Placeholder = placeholders[i]
		t.CharLimit = 256
		t.Prompt = ""
		if ConnectField(i) == fieldPassword {
			t.EchoMode = textinput.EchoPassword
			t.EchoCharacter = '•'
		}
		_ = labels[i]
		inputs[i] = t
	}
	inputs[fieldPort].SetValue("22")
	inputs[fieldHost].Focus()

	return ConnectModel{
		inputs:  inputs,
		focused: fieldHost,
	}
}

func (m ConnectModel) Init() tea.Cmd {
	return textinput.Blink
}

// Prefill loads connection details into the form. Used when the user selects
// a saved/ssh-config entry from the connections list. KeyPath is included;
// password and passphrase are left blank deliberately — saved-connection
// records do not store secrets.
func (m *ConnectModel) Prefill(c ConnectedMsg) {
	m.inputs[fieldHost].SetValue(c.Host)
	if c.Port != "" {
		m.inputs[fieldPort].SetValue(c.Port)
	}
	m.inputs[fieldUser].SetValue(c.User)
	m.inputs[fieldKeyPath].SetValue(c.KeyPath)
	m.inputs[fieldPassword].SetValue("")
	m.proxyJump = c.ProxyJump
	for i := range m.sshFilled {
		m.sshFilled[i] = false
	}
	// Position cursor on Password — it's the most likely thing the user still
	// needs to provide (keys are usually in the agent or already configured).
	for i := range m.inputs {
		m.inputs[i].Blur()
	}
	m.focused = fieldPassword
	m.inputs[fieldPassword].Focus()
}

func lookupSSHConfig(host string) tea.Cmd {
	return func() tea.Msg {
		return sshConfigMsg{entry: sftpclient.LookupSSHConfig(host)}
	}
}

func (m ConnectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case sshConfigMsg:
		e := msg.entry
		m.proxyJump = e.ProxyJump
		if e.HostName != "" {
			m.inputs[fieldHost].SetValue(e.HostName)
		}
		if e.Port != "" && !m.sshFilled[fieldPort] &&
			(m.inputs[fieldPort].Value() == "" || m.inputs[fieldPort].Value() == "22") {
			m.inputs[fieldPort].SetValue(e.Port)
			m.sshFilled[fieldPort] = true
		}
		if e.User != "" && !m.sshFilled[fieldUser] && m.inputs[fieldUser].Value() == "" {
			m.inputs[fieldUser].SetValue(e.User)
			m.sshFilled[fieldUser] = true
		}
		if e.IdentityFile != "" && !m.sshFilled[fieldKeyPath] && m.inputs[fieldKeyPath].Value() == "" {
			m.inputs[fieldKeyPath].SetValue(e.IdentityFile)
			m.sshFilled[fieldKeyPath] = true
		}
		return m, nil

	case tea.KeyMsg:
		m.err = ""

		// When user types into a non-host field, clear its ssh-filled marker.
		if m.focused != fieldHost {
			switch msg.String() {
			case "tab", "shift+tab", "up", "down", "enter", "ctrl+c", "esc":
			default:
				m.sshFilled[m.focused] = false
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, func() tea.Msg { return openConnListMsg{} }

		case "tab", "down":
			wasOnHost := m.focused == fieldHost
			hostVal := m.inputs[fieldHost].Value()
			m.inputs[m.focused].Blur()
			m.focused = (m.focused + 1) % fieldCount
			m.inputs[m.focused].Focus()
			cmds := []tea.Cmd{textinput.Blink}
			if wasOnHost && hostVal != "" {
				cmds = append(cmds, lookupSSHConfig(hostVal))
			}
			return m, tea.Batch(cmds...)

		case "shift+tab", "up":
			m.inputs[m.focused].Blur()
			m.focused = (m.focused + fieldCount - 1) % fieldCount
			m.inputs[m.focused].Focus()
			return m, textinput.Blink

		case "enter":
			if m.focused < fieldCount-1 {
				wasOnHost := m.focused == fieldHost
				hostVal := m.inputs[fieldHost].Value()
				m.inputs[m.focused].Blur()
				m.focused++
				m.inputs[m.focused].Focus()
				cmds := []tea.Cmd{textinput.Blink}
				if wasOnHost && hostVal != "" {
					cmds = append(cmds, lookupSSHConfig(hostVal))
				}
				return m, tea.Batch(cmds...)
			}
			host := strings.TrimSpace(m.inputs[fieldHost].Value())
			port := strings.TrimSpace(m.inputs[fieldPort].Value())
			user := strings.TrimSpace(m.inputs[fieldUser].Value())
			if host == "" || user == "" {
				m.err = "Host and username are required"
				return m, nil
			}
			if port == "" {
				port = "22"
			}
			proxy := m.proxyJump
			return m, func() tea.Msg {
				return ConnectedMsg{
					Host:      host,
					Port:      port,
					User:      user,
					Password:  m.inputs[fieldPassword].Value(),
					KeyPath:   strings.TrimSpace(m.inputs[fieldKeyPath].Value()),
					ProxyJump: proxy,
				}
			}
		}
	}

	var cmd tea.Cmd
	m.inputs[m.focused], cmd = m.inputs[m.focused].Update(msg)
	return m, cmd
}

func (m ConnectModel) View() string {
	labels := []string{"Host", "Port", "Username", "Password", "SSH Key Path (optional)"}
	sshTag := lipgloss.NewStyle().Foreground(colorAccent).Render("[ssh]")

	var fields []string
	for i := range m.inputs {
		label := styleSubtitle.Render(labels[i])
		if m.sshFilled[ConnectField(i)] {
			label += "  " + sshTag
		}
		focused := ConnectField(i) == m.focused

		inputStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1).
			Width(36)

		if focused {
			inputStyle = inputStyle.BorderForeground(colorPrimary)
		} else {
			inputStyle = inputStyle.BorderForeground(colorBorder)
		}

		fields = append(fields, label+"\n"+inputStyle.Render(m.inputs[i].View()))
	}

	form := lipgloss.JoinVertical(lipgloss.Left, fields...)

	hint := lipgloss.JoinHorizontal(lipgloss.Left,
		keyHint("tab/↓", "next")+"  "+
			keyHint("↑", "prev")+"  "+
			keyHint("enter", "connect")+"  "+
			keyHint("esc", "quit"),
	)

	var errStr string
	if m.err != "" {
		errStr = "\n" + styleError.Render("  "+m.err+"  ")
	}

	panel := stylePanel.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			form,
			errStr,
			"\n"+hint,
		),
	)

	content := lipgloss.JoinVertical(lipgloss.Center,
		styleLogo.Render(logo),
		styleTitle.Render("  SFTP Commander  "),
		"\n",
		panel,
	)

	if m.width > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}
