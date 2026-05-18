package ui

import (
	sftpclient "sftpbrowser/internal/sftp"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Messages emitted by TwoPane (handled by App).
type backToSinglePaneMsg struct{}
type refreshTwoPaneMsg struct{}

type TwoPaneModel struct {
	left   BrowserModel
	right  BrowserModel
	active int // 0 = left, 1 = right
	client *sftpclient.Client
	width  int
	height int
}

func NewTwoPaneModel(client *sftpclient.Client, leftFS, rightFS FileSystem) TwoPaneModel {
	left := NewBrowserModelSide(leftFS, 0)
	left.twoPane = true
	left.focused = true
	right := NewBrowserModelSide(rightFS, 1)
	right.twoPane = true
	return TwoPaneModel{
		left:   left,
		right:  right,
		active: 0,
		client: client,
	}
}

func (m TwoPaneModel) Init() tea.Cmd {
	return tea.Batch(m.left.Init(), m.right.Init())
}

func (m TwoPaneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		half := (msg.Width - 1) / 2
		// Reserve 1 row for the unified hint bar.
		bodyH := msg.Height - 1
		if bodyH < 3 {
			bodyH = 3
		}
		// Each panel sits inside a border (2 cols, 2 rows).
		leftSize := tea.WindowSizeMsg{Width: half - 2, Height: bodyH - 2}
		rightSize := tea.WindowSizeMsg{Width: msg.Width - half - 1 - 2, Height: bodyH - 2}
		m.left, _ = updateAs[BrowserModel](m.left, leftSize)
		m.right, _ = updateAs[BrowserModel](m.right, rightSize)
		return m, nil

	case entriesLoadedMsg:
		if msg.side == 0 {
			m.left, _ = updateAs[BrowserModel](m.left, msg)
		} else {
			m.right, _ = updateAs[BrowserModel](m.right, msg)
		}
		return m, nil

	case opDoneMsg:
		if msg.side == 0 {
			m.left, _ = updateAs[BrowserModel](m.left, msg)
		} else {
			m.right, _ = updateAs[BrowserModel](m.right, msg)
		}
		return m, nil

	case refreshTwoPaneMsg:
		var cmdL, cmdR tea.Cmd
		m.left, cmdL = updateAs[BrowserModel](m.left, refreshSignalMsg{side: 0})
		m.right, cmdR = updateAs[BrowserModel](m.right, refreshSignalMsg{side: 1})
		return m, tea.Batch(cmdL, cmdR)

	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			m.swap()
			return m, nil
		case "f5":
			return m, m.copyToOther()
		case "f2", "ctrl+w":
			return m, func() tea.Msg { return backToSinglePaneMsg{} }
		case "ctrl+c":
			return m, tea.Quit
		}
		// Route the key to the focused panel.
		if m.active == 0 {
			var cmd tea.Cmd
			m.left, cmd = updateAs[BrowserModel](m.left, msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.right, cmd = updateAs[BrowserModel](m.right, msg)
		return m, cmd
	}
	return m, nil
}

func (m *TwoPaneModel) swap() {
	m.left.focused = !m.left.focused
	m.right.focused = !m.right.focused
	m.active = 1 - m.active
}

func (m TwoPaneModel) activePanel() *BrowserModel {
	if m.active == 0 {
		return &m.left
	}
	return &m.right
}

func (m TwoPaneModel) inactivePanel() *BrowserModel {
	if m.active == 0 {
		return &m.right
	}
	return &m.left
}

// copyToOther dispatches a cross-panel transfer. Direction is inferred from
// the panels' FS kinds; same-kind copies (local→local, remote→remote) are
// silently ignored because they don't fit either upload or download.
func (m TwoPaneModel) copyToOther() tea.Cmd {
	src := m.activePanel()
	dst := m.inactivePanel()
	items := src.targets()
	if len(items) == 0 {
		return nil
	}
	srcKind := src.fs.Kind()
	dstKind := dst.fs.Kind()
	switch {
	case srcKind == "remote" && dstKind == "local":
		entries := items
		localDir := dst.path
		return func() tea.Msg {
			return downloadStartMsg{entries: entries, localDir: localDir}
		}
	case srcKind == "local" && dstKind == "remote":
		sources := make([]string, 0, len(items))
		for _, e := range items {
			sources = append(sources, e.Path)
		}
		remoteDir := dst.path
		return func() tea.Msg {
			return uploadStartMsg{remoteDir: remoteDir, sources: sources}
		}
	default:
		return nil
	}
}

func (m TwoPaneModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}
	half := (m.width - 1) / 2
	leftW := half
	rightW := m.width - half - 1
	bodyH := m.height - 1
	if bodyH < 3 {
		bodyH = 3
	}

	leftView := boxPanel(m.left.View(), m.left.fs.Label(), leftW, bodyH, m.active == 0)
	rightView := boxPanel(m.right.View(), m.right.fs.Label(), rightW, bodyH, m.active == 1)

	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftView, " ", rightView)

	hints := lipgloss.NewStyle().
		Background(colorBgAlt).
		Width(m.width).
		MaxHeight(1).
		Padding(0, 1).
		Render(keyHint("tab", "switch") + "  " +
			keyHint("F5", "copy →") + "  " +
			keyHint("f2/^w", "single-pane") + "  " +
			keyHint("?", "help") + "  " +
			keyHint("q", "quit"))

	return lipgloss.JoinVertical(lipgloss.Left, panes, hints)
}

func boxPanel(inner, title string, w, h int, focused bool) string {
	borderColor := colorBorder
	if focused {
		borderColor = colorPrimary
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(w - 2).
		Height(h - 2)
	header := lipgloss.NewStyle().
		Foreground(colorText).Bold(true).
		Render(truncate(title, w-4))
	body := lipgloss.JoinVertical(lipgloss.Left, header, inner)
	return box.Render(body)
}

// refreshSignalMsg is sent to a specific browser side asking it to reload its
// current path. Compared to BrowserModel.Refresh() (a Cmd builder), this can
// be batched in TwoPane.Update via the standard message path.
type refreshSignalMsg struct{ side int }
