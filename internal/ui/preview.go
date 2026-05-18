package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	sftpclient "sftpbrowser/internal/sftp"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const previewMaxBytes = 256 * 1024

type previewLoadedMsg struct {
	path      string
	content   string
	truncated bool
	binary    bool
	size      int64
	err       error
}

type backFromPreviewMsg struct{}

type PreviewModel struct {
	fs        FileSystem
	entry     sftpclient.FileEntry
	vp        viewport.Model
	loading   bool
	err       string
	truncated bool
	binary    bool
	width     int
	height    int
}

func NewPreviewModel(fs FileSystem, entry sftpclient.FileEntry) PreviewModel {
	return PreviewModel{
		fs:      fs,
		entry:   entry,
		loading: true,
	}
}

func (m PreviewModel) Init() tea.Cmd {
	return m.load()
}

func (m PreviewModel) load() tea.Cmd {
	fs := m.fs
	p := m.entry.Path
	return func() tea.Msg {
		data, truncated, err := fs.ReadFileChunk(p, previewMaxBytes)
		if err != nil {
			return previewLoadedMsg{path: p, err: err}
		}
		binary := looksBinary(data)
		content := ""
		if binary {
			content = fmt.Sprintf("(binary file, %d bytes shown)", len(data))
		} else {
			content = string(data)
		}
		return previewLoadedMsg{
			path:      p,
			content:   content,
			truncated: truncated,
			binary:    binary,
			size:      int64(len(data)),
		}
	}
}

func looksBinary(b []byte) bool {
	// First 8KB sample. NUL or >10% non-printable → treat as binary.
	sample := b
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if len(sample) == 0 {
		return false
	}
	if !utf8.Valid(sample) {
		return true
	}
	bad := 0
	for _, c := range sample {
		if c == 0 {
			return true
		}
		if c < 9 || (c > 13 && c < 32) {
			bad++
		}
	}
	return bad*10 > len(sample)
}

func (m PreviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()

	case previewLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.truncated = msg.truncated
		m.binary = msg.binary
		if m.width > 0 && m.height > 0 {
			m.resizeViewport()
		} else {
			m.vp = viewport.New(80, 20)
		}
		m.vp.SetContent(msg.content)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c", "h", "left", "backspace":
			return m, func() tea.Msg { return backFromPreviewMsg{} }
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m *PreviewModel) resizeViewport() {
	// Reserve: header(1) + path(1) + border(2) + status(1) + hints(1)
	w := m.width - 4
	h := m.height - 6
	if w < 20 {
		w = 20
	}
	if h < 5 {
		h = 5
	}
	if m.vp.Width == 0 {
		m.vp = viewport.New(w, h)
	} else {
		m.vp.Width = w
		m.vp.Height = h
	}
}

func (m PreviewModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}
	heading := styleTitle.Render("  Preview  ")
	pathLine := stylePath.Render(m.entry.Path)

	var body string
	switch {
	case m.loading:
		body = lipgloss.Place(m.width-4, m.height-6, lipgloss.Center, lipgloss.Center,
			styleSecondary("Loading..."))
	case m.err != "":
		body = lipgloss.Place(m.width-4, m.height-6, lipgloss.Center, lipgloss.Center,
			styleError.Render("  Error: "+m.err+"  "))
	default:
		body = m.vp.View()
	}

	bodyBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBorder).
		Padding(0, 1).
		Render(body)

	notes := []string{}
	if m.truncated {
		notes = append(notes, fmt.Sprintf("truncated to %d KB", previewMaxBytes/1024))
	}
	if m.binary {
		notes = append(notes, "binary file")
	}
	noteLine := ""
	if len(notes) > 0 {
		noteLine = lipgloss.NewStyle().Foreground(colorWarning).Render("  " + strings.Join(notes, " • "))
	}

	hints := lipgloss.NewStyle().
		Background(colorBgAlt).
		Width(m.width).
		Padding(0, 1).
		Render(keyHint("↑↓/jk", "scroll") + "  " +
			keyHint("pgup/pgdn", "page") + "  " +
			keyHint("g/G", "top/bot") + "  " +
			keyHint("esc/q", "back"))

	return lipgloss.JoinVertical(lipgloss.Left,
		heading,
		pathLine,
		bodyBox,
		noteLine,
		hints,
	)
}
