package ui

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const previewChunkSize = 256 * 1024

type previewLoadedMsg struct {
	path   string
	data   []byte
	offset int64
	total  int64
	err    error
}

type previewSymlinkMsg struct {
	path   string
	target string
}

type backFromPreviewMsg struct{}

type PreviewModel struct {
	fs            FileSystem
	entry         sftpclient.FileEntry
	vp            viewport.Model
	loading       bool
	loadingMore   bool
	err           string
	binary        bool
	width         int
	height        int
	symlinkTarget string

	raw       []byte
	totalSize int64
}

func NewPreviewModel(fs FileSystem, entry sftpclient.FileEntry) PreviewModel {
	return PreviewModel{
		fs:      fs,
		entry:   entry,
		loading: true,
	}
}

func (m PreviewModel) Init() tea.Cmd {
	if m.entry.IsSymlink {
		return tea.Batch(m.loadAt(0), m.resolveSymlink())
	}
	return m.loadAt(0)
}

func (m PreviewModel) resolveSymlink() tea.Cmd {
	fs := m.fs
	p := m.entry.Path
	return func() tea.Msg {
		target, err := fs.Readlink(p)
		if err != nil {
			return previewSymlinkMsg{path: p, target: ""}
		}
		return previewSymlinkMsg{path: p, target: target}
	}
}

func (m PreviewModel) loadAt(offset int64) tea.Cmd {
	fs := m.fs
	p := m.entry.Path
	return func() tea.Msg {
		data, total, err := fs.ReadFileRange(p, offset, previewChunkSize)
		if err != nil {
			return previewLoadedMsg{path: p, err: err, offset: offset}
		}
		return previewLoadedMsg{path: p, data: data, total: total, offset: offset}
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
		if !m.loading && m.err == "" {
			m.vp.SetContent(m.renderBody())
		}

	case previewSymlinkMsg:
		m.symlinkTarget = msg.target
		return m, nil

	case previewLoadedMsg:
		m.loading = false
		m.loadingMore = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		// First chunk seeds binary detection and total size; subsequent
		// chunks only extend the buffer.
		if msg.offset == 0 {
			m.raw = msg.data
			m.binary = looksBinary(msg.data)
		} else {
			m.raw = append(m.raw, msg.data...)
		}
		m.totalSize = msg.total
		if m.width > 0 && m.height > 0 {
			m.resizeViewport()
		} else {
			m.vp = viewport.New(80, 20)
		}
		m.vp.SetContent(m.renderBody())
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c", "h", "left", "backspace":
			return m, func() tea.Msg { return backFromPreviewMsg{} }
		case "m":
			if !m.loadingMore && !m.binary && int64(len(m.raw)) < m.totalSize {
				m.loadingMore = true
				return m, m.loadAt(int64(len(m.raw)))
			}
			return m, nil
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

// renderBody produces the body string with chroma highlighting and line
// numbers when the content is text. Binary files get a placeholder line.
func (m PreviewModel) renderBody() string {
	if m.binary {
		return fmt.Sprintf("(binary file, %d bytes loaded of %d)", len(m.raw), m.totalSize)
	}
	highlighted := highlightCode(string(m.raw), m.entry.Name)
	return addLineNumbers(highlighted)
}

// highlightCode runs chroma over text using a lexer chosen from filename;
// returns the text unchanged when no lexer matches and the analyser bails.
func highlightCode(text, filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(text)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}

	iter, err := lexer.Tokenise(nil, text)
	if err != nil {
		return text
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iter); err != nil {
		return text
	}
	return buf.String()
}

// addLineNumbers prefixes each line with a right-aligned line number followed
// by a separator. Works on already-ANSI-coloured text since it splits on \n
// (color codes don't contain newlines).
func addLineNumbers(text string) string {
	lines := strings.Split(text, "\n")
	// Drop trailing empty line that comes from a final \n so we don't show
	// a phantom numbered blank line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	width := len(fmt.Sprintf("%d", len(lines)))
	if width < 2 {
		width = 2
	}
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(" │ ")
	out := make([]string, len(lines))
	for i, line := range lines {
		num := fmt.Sprintf("%*d", width, i+1)
		out[i] = lipgloss.NewStyle().Foreground(colorMuted).Render(num) + sep + line
	}
	return strings.Join(out, "\n")
}

func (m PreviewModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}
	heading := styleTitle.Render("  Preview  ")
	pathStr := m.entry.Path
	if m.entry.IsSymlink && m.symlinkTarget != "" {
		pathStr += "  → " + m.symlinkTarget
	}
	pathLine := stylePath.Render(pathStr)

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
	if !m.binary && int64(len(m.raw)) < m.totalSize {
		remaining := m.totalSize - int64(len(m.raw))
		notes = append(notes, fmt.Sprintf("%d KB loaded / %d KB total (press m for more, %d KB left)",
			len(m.raw)/1024, m.totalSize/1024, remaining/1024))
	} else if m.binary {
		notes = append(notes, "binary file")
	}
	if m.loadingMore {
		notes = append(notes, "loading...")
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
			keyHint("m", "more") + "  " +
			keyHint("esc/q", "back"))

	return lipgloss.JoinVertical(lipgloss.Left,
		heading,
		pathLine,
		bodyBox,
		noteLine,
		hints,
	)
}
