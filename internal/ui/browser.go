package ui

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	sftpclient "sftpbrowser/internal/sftp"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// path package is kept solely for path.Ext in fileIcon — it operates on
// basenames so the platform-specific filepath isn't needed there.
var _ = path.Ext

type entriesLoadedMsg struct {
	path    string
	entries []sftpclient.FileEntry
	err     error
	side    int // 0 = single-pane / left, 1 = right (two-pane only)
}

type downloadStartMsg struct {
	entries  []sftpclient.FileEntry
	localDir string // empty = use App default
}

type uploadStartMsg struct {
	remoteDir string
	sources   []string // when set, NewUploadModelMulti path; else prompt input
}

type previewStartMsg struct {
	entry sftpclient.FileEntry
	fs    FileSystem
}

type openBookmarksMsg struct{}
type addBookmarkMsg struct{ path string }
type openTwoPaneMsg struct{}

type browserMode int

const (
	modeBrowse browserMode = iota
	modeFilter
	modePrompt
	modeConfirm
	modeHelp
)

type promptKind int

const (
	promptRename promptKind = iota
	promptMkdir
	promptChmod
)

type sortMode int

const (
	sortByName sortMode = iota
	sortBySize
	sortByMTime
)

func (s sortMode) String() string {
	switch s {
	case sortBySize:
		return "size"
	case sortByMTime:
		return "mtime"
	default:
		return "name"
	}
}

type cursorState struct {
	cursor int
	offset int
}

type opDoneMsg struct {
	op     string
	target string
	err    error
	side   int
}

type BrowserModel struct {
	fs        FileSystem
	side      int // identifier so two-pane mode can route async msgs back
	path      string
	entries   []sftpclient.FileEntry // raw from fs
	visible   []sftpclient.FileEntry // filtered+sorted view
	cursor    int
	offset    int
	loading   bool
	err       string
	status    string
	width     int
	height    int
	selection map[string]bool

	showHidden bool
	sortBy     sortMode
	sortDesc   bool
	cursorMem  map[string]cursorState

	mode        browserMode
	filterInput textinput.Model

	promptKind   promptKind
	promptInput  textinput.Model
	promptTarget string
	promptLabel  string

	confirmTargets []string
	confirmBody    string

	// When set, the browser renders with a highlighted border instead of the
	// default chrome. Used by TwoPane to mark the focused panel.
	twoPane bool
	focused bool
}

func NewBrowserModel(fs FileSystem) BrowserModel {
	return NewBrowserModelSide(fs, 0)
}

// NewBrowserModelSide constructs a browser tagged with a side identifier so
// the parent TwoPane can route async messages back to the panel that issued
// them. Single-pane callers should use NewBrowserModel.
func NewBrowserModelSide(fs FileSystem, side int) BrowserModel {
	filter := textinput.New()
	filter.Prompt = "/"
	filter.CharLimit = 128
	filter.Width = 32

	prompt := textinput.New()
	prompt.CharLimit = 256
	prompt.Prompt = ""
	prompt.Width = 48

	return BrowserModel{
		fs:          fs,
		side:        side,
		path:        fs.Home(),
		loading:     true,
		selection:   make(map[string]bool),
		cursorMem:   make(map[string]cursorState),
		filterInput: filter,
		promptInput: prompt,
	}
}

func (m BrowserModel) Init() tea.Cmd {
	return m.loadDir(m.path)
}

func (m BrowserModel) FS() FileSystem      { return m.fs }
func (m BrowserModel) Side() int           { return m.side }
func (m BrowserModel) CurrentPath() string { return m.path }
func (m BrowserModel) ConnInfo() string    { return m.fs.Label() }

func (m BrowserModel) loadDir(p string) tea.Cmd {
	side := m.side
	fs := m.fs
	return func() tea.Msg {
		entries, err := fs.List(p)
		return entriesLoadedMsg{path: p, entries: entries, err: err, side: side}
	}
}

func (m BrowserModel) Refresh() tea.Cmd {
	return m.loadDir(m.path)
}

// LoadPath navigates the browser to p on its next tick. Used by the App when
// the user picks a bookmark — the BrowserModel itself remembers cursorMem,
// so backing out to the previous path with `h` still feels natural.
func (m BrowserModel) LoadPath(p string) tea.Cmd {
	return m.loadDir(p)
}

func (m BrowserModel) visibleRows() int {
	// header(1) + pathBar(1) + statusBar(1) + hints(1) = 4 chrome lines.
	// In two-pane the header and hints belong to TwoPane, so chrome shrinks
	// to pathBar + statusBar.
	reserved := 4
	if m.twoPane {
		reserved = 2
	}
	if m.height > reserved+3 {
		return m.height - reserved
	}
	return 3
}

func (m *BrowserModel) applyView() {
	visible := make([]sftpclient.FileEntry, 0, len(m.entries))
	filter := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
	for _, e := range m.entries {
		if !m.showHidden && strings.HasPrefix(e.Name, ".") {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(e.Name), filter) {
			continue
		}
		visible = append(visible, e)
	}
	sortEntries(visible, m.sortBy, m.sortDesc)
	m.visible = visible
	if m.cursor >= len(m.visible) {
		m.cursor = 0
		if len(m.visible) > 0 {
			m.cursor = len(m.visible) - 1
		}
	}
	if m.offset > m.cursor {
		m.offset = m.cursor
	}
}

func sortEntries(entries []sftpclient.FileEntry, mode sortMode, desc bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		var less bool
		switch mode {
		case sortBySize:
			less = a.Size < b.Size
		case sortByMTime:
			less = a.ModTime.Before(b.ModTime)
		default:
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		if desc {
			return !less
		}
		return less
	})
}

func (m BrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case refreshSignalMsg:
		if msg.side != m.side {
			return m, nil
		}
		m.loading = true
		return m, m.loadDir(m.path)

	case entriesLoadedMsg:
		if msg.side != m.side {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.path = msg.path
			m.entries = msg.entries
			m.err = ""
			m.applyView()
			if cs, ok := m.cursorMem[m.path]; ok && cs.cursor < len(m.visible) {
				m.cursor = cs.cursor
				m.offset = min(cs.offset, m.cursor)
			} else {
				m.cursor = 0
				m.offset = 0
			}
		}
		return m, nil

	case opDoneMsg:
		if msg.side != m.side {
			return m, nil
		}
		if msg.err != nil {
			m.err = fmt.Sprintf("%s failed: %s", msg.op, msg.err.Error())
			return m, nil
		}
		m.status = fmt.Sprintf("%s ok", msg.op)
		m.selection = make(map[string]bool)
		m.loading = true
		return m, m.loadDir(m.path)

	case tea.KeyMsg:
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modePrompt:
			return m.updatePrompt(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		case modeHelp:
			return m.updateHelp(msg)
		}
		return m.updateBrowse(msg)
	}
	return m, nil
}

func (m BrowserModel) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.err = ""
	m.status = ""
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			if m.cursor < m.offset {
				m.offset = m.cursor
			}
		}

	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			visible := m.visibleRows()
			if m.cursor >= m.offset+visible {
				m.offset = m.cursor - visible + 1
			}
		}

	case "pgup":
		visible := m.visibleRows()
		m.cursor -= visible
		if m.cursor < 0 {
			m.cursor = 0
		}
		if m.cursor < m.offset {
			m.offset = m.cursor
		}

	case "pgdown":
		visible := m.visibleRows()
		m.cursor += visible
		if m.cursor > len(m.visible)-1 {
			m.cursor = len(m.visible) - 1
		}
		if m.cursor >= m.offset+visible {
			m.offset = m.cursor - visible + 1
		}

	case "g":
		m.cursor = 0
		m.offset = 0

	case "G":
		m.cursor = len(m.visible) - 1
		visible := m.visibleRows()
		if m.cursor >= visible {
			m.offset = m.cursor - visible + 1
		}

	case "enter", "l", "right":
		if len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		if entry.IsDir {
			m.cursorMem[m.path] = cursorState{m.cursor, m.offset}
			m.loading = true
			return m, m.loadDir(entry.Path)
		}
		fs := m.fs
			return m, func() tea.Msg { return previewStartMsg{entry: entry, fs: fs} }

	case "v":
		if len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		if !entry.IsDir {
			fs := m.fs
			return m, func() tea.Msg { return previewStartMsg{entry: entry, fs: fs} }
		}

	case "h", "left", "backspace":
		parent := m.fs.Dir(m.path)
		if parent != m.path {
			m.cursorMem[m.path] = cursorState{m.cursor, m.offset}
			m.loading = true
			return m, m.loadDir(parent)
		}

	case "R":
		m.loading = true
		return m, m.loadDir(m.path)

	case " ":
		if len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		if m.selection[entry.Path] {
			delete(m.selection, entry.Path)
		} else {
			m.selection[entry.Path] = true
		}
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			vis := m.visibleRows()
			if m.cursor >= m.offset+vis {
				m.offset = m.cursor - vis + 1
			}
		}

	case "esc":
		if len(m.selection) > 0 {
			m.selection = make(map[string]bool)
		}

	case "d":
		if m.fs.Kind() != "remote" {
			break
		}
		if len(m.visible) == 0 && len(m.selection) == 0 {
			break
		}
		toDownload := m.targets()
		return m, func() tea.Msg {
			return downloadStartMsg{entries: toDownload}
		}

	case "u":
		if m.fs.Kind() != "remote" {
			break
		}
		return m, func() tea.Msg { return uploadStartMsg{remoteDir: m.path} }

	case "T":
		if !m.twoPane {
			return m, func() tea.Msg { return openTwoPaneMsg{} }
		}

	case "a":
		if len(m.selection) == len(m.visible) {
			m.selection = make(map[string]bool)
		} else {
			for _, e := range m.visible {
				m.selection[e.Path] = true
			}
		}

	case "/":
		m.mode = modeFilter
		m.filterInput.Focus()
		return m, textinput.Blink

	case ".":
		m.showHidden = !m.showHidden
		m.applyView()

	case "s":
		m.sortBy = (m.sortBy + 1) % 3
		m.applyView()

	case "S":
		m.sortDesc = !m.sortDesc
		m.applyView()

	case "?":
		m.mode = modeHelp
		return m, nil

	case "b":
		if m.fs.Kind() != "remote" {
			break
		}
		return m, func() tea.Msg { return addBookmarkMsg{path: m.path} }

	case "B":
		if m.fs.Kind() != "remote" {
			break
		}
		return m, func() tea.Msg { return openBookmarksMsg{} }

	case "D":
		targets := m.selectedPaths()
		if len(targets) == 0 {
			break
		}
		m.confirmTargets = targets
		if len(targets) == 1 {
			m.confirmBody = "Delete " + targets[0] + " ?"
		} else {
			m.confirmBody = fmt.Sprintf("Delete %d entries (recursive) ?", len(targets))
		}
		m.mode = modeConfirm
		return m, nil

	case "r":
		if len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		m.openPrompt(promptRename, entry.Path, "New name:", entry.Name)
		return m, textinput.Blink

	case "M":
		m.openPrompt(promptMkdir, m.path, "New directory name:", "")
		return m, textinput.Blink

	case "c":
		if len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		mode := fmt.Sprintf("%o", entry.Mode.Perm())
		m.openPrompt(promptChmod, entry.Path, "Mode (octal):", mode)
		return m, textinput.Blink
	}
	return m, nil
}

func (m *BrowserModel) openPrompt(kind promptKind, target, label, initial string) {
	m.mode = modePrompt
	m.promptKind = kind
	m.promptTarget = target
	m.promptLabel = label
	m.promptInput.SetValue(initial)
	m.promptInput.CursorEnd()
	m.promptInput.Focus()
}

func (m BrowserModel) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterInput.SetValue("")
		m.filterInput.Blur()
		m.mode = modeBrowse
		m.applyView()
		return m, nil
	case "enter":
		m.filterInput.Blur()
		m.mode = modeBrowse
		m.applyView()
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.applyView()
	return m, cmd
}

func (m BrowserModel) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeBrowse
		m.promptInput.Blur()
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.promptInput.Value())
		m.mode = modeBrowse
		m.promptInput.Blur()
		if value == "" {
			m.err = "empty input"
			return m, nil
		}
		return m, m.runPrompt(value)
	}
	var cmd tea.Cmd
	m.promptInput, cmd = m.promptInput.Update(msg)
	return m, cmd
}

func (m BrowserModel) runPrompt(value string) tea.Cmd {
	fs := m.fs
	side := m.side
	target := m.promptTarget
	kind := m.promptKind
	return func() tea.Msg {
		switch kind {
		case promptRename:
			newPath := fs.Join(fs.Dir(target), value)
			return opDoneMsg{op: "rename", target: target, err: fs.Rename(target, newPath), side: side}
		case promptMkdir:
			newPath := fs.Join(target, value)
			return opDoneMsg{op: "mkdir", target: newPath, err: fs.Mkdir(newPath), side: side}
		case promptChmod:
			n, err := strconv.ParseUint(value, 8, 32)
			if err != nil {
				return opDoneMsg{op: "chmod", target: target, err: fmt.Errorf("invalid octal mode: %w", err), side: side}
			}
			return opDoneMsg{op: "chmod", target: target, err: fs.Chmod(target, os.FileMode(n)), side: side}
		}
		return opDoneMsg{op: "unknown", side: side}
	}
}

func (m BrowserModel) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		targets := m.confirmTargets
		m.confirmTargets = nil
		m.confirmBody = ""
		m.mode = modeBrowse
		fs := m.fs
		side := m.side
		return m, func() tea.Msg {
			for _, t := range targets {
				if err := fs.RemoveAll(t); err != nil {
					return opDoneMsg{op: "delete", target: t, err: err, side: side}
				}
			}
			return opDoneMsg{op: fmt.Sprintf("deleted %d", len(targets)), side: side}
		}
	case "n", "N", "esc", "ctrl+c":
		m.confirmTargets = nil
		m.confirmBody = ""
		m.mode = modeBrowse
	}
	return m, nil
}

func (m BrowserModel) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "?", "esc", "q", "enter":
		m.mode = modeBrowse
	}
	return m, nil
}

func (m BrowserModel) selectedPaths() []string {
	if len(m.selection) > 0 {
		out := make([]string, 0, len(m.selection))
		for _, e := range m.visible {
			if m.selection[e.Path] {
				out = append(out, e.Path)
			}
		}
		return out
	}
	if len(m.visible) == 0 {
		return nil
	}
	return []string{m.visible[m.cursor].Path}
}

func (m BrowserModel) targets() []sftpclient.FileEntry {
	if len(m.selection) > 0 {
		var out []sftpclient.FileEntry
		for _, e := range m.visible {
			if m.selection[e.Path] {
				out = append(out, e)
			}
		}
		return out
	}
	if len(m.visible) == 0 {
		return nil
	}
	return []sftpclient.FileEntry{m.visible[m.cursor]}
}

// View

func (m BrowserModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	visible := m.visibleRows()

	// Header bar
	header := lipgloss.NewStyle().Width(m.width).MaxHeight(1).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			styleTitle.Render("  SFTP Browser  "),
			"  ",
			styleSubtitle.Render(truncate(m.fs.Label(), max(0, m.width-20))),
		),
	)

	// Path bar with sort + hidden indicators
	sortInd := "[" + m.sortBy.String()
	if m.sortDesc {
		sortInd += "↓"
	} else {
		sortInd += "↑"
	}
	sortInd += "]"
	if m.showHidden {
		sortInd += " [.]"
	}
	if m.filterInput.Value() != "" {
		sortInd += " /" + m.filterInput.Value()
	}
	indWidth := runewidth.StringWidth(sortInd) + 2
	pathText := truncate(m.path, max(0, m.width-4-indWidth))
	pathLine := lipgloss.JoinHorizontal(lipgloss.Left,
		pathText,
		strings.Repeat(" ", max(1, m.width-2-runewidth.StringWidth(pathText)-runewidth.StringWidth(sortInd))),
		lipgloss.NewStyle().Foreground(colorMuted).Render(sortInd),
	)
	pathBar := lipgloss.NewStyle().
		Background(colorBgAlt).
		Foreground(colorWarning).
		Width(m.width).
		MaxHeight(1).
		Padding(0, 1).
		Render(pathLine)

	// Column widths.
	// Layout: selMark(2) + name(*) + size(10) + spacer(2) + mtime(12) + spacer(2) + mode(10)
	const (
		selMarkW = 2
		sizeW    = 10
		spacerW  = 2
		mtimeW   = 12
		modeW    = 10
	)
	showMTime := m.width >= 76
	showMode := m.width >= 56
	nameWidth := m.width - 2 - selMarkW - sizeW - spacerW
	if showMTime {
		nameWidth -= spacerW + mtimeW
	}
	if showMode {
		nameWidth -= spacerW + modeW
	}
	if nameWidth < 8 {
		nameWidth = 8
	}

	var content string
	switch {
	case m.loading:
		content = lipgloss.Place(m.width, visible,
			lipgloss.Center, lipgloss.Center,
			styleSecondary("Loading..."))
	case m.err != "":
		content = lipgloss.Place(m.width, visible,
			lipgloss.Center, lipgloss.Center,
			styleError.Render("  Error: "+m.err+"  "))
	case len(m.visible) == 0:
		hint := "(empty directory)"
		if m.filterInput.Value() != "" {
			hint = "(no matches for /" + m.filterInput.Value() + ")"
		}
		content = lipgloss.Place(m.width, visible,
			lipgloss.Center, lipgloss.Center,
			styleSubtitle.Render(hint))
	default:
		end := min(m.offset+visible, len(m.visible))
		rows := make([]string, 0, visible)
		mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
		nameCol := lipgloss.NewStyle().Width(nameWidth)
		modeCol := mutedStyle.Width(modeW)
		mtimeCol := mutedStyle.Width(mtimeW)

		for i, entry := range m.visible[m.offset:end] {
			idx := i + m.offset
			selected := m.selection[entry.Path]
			isCursor := idx == m.cursor

			icon := fileIcon(entry)
			trailing := 0
			if entry.IsDir {
				trailing = 1
			}
			name := truncate(entry.Name, nameWidth-2-trailing)
			var nameStyled string
			if entry.IsDir {
				nameStyled = styleDir.Render(icon + " " + name + "/")
			} else {
				nameStyled = styleFile.Render(icon + " " + name)
			}

			sizeStr := styleSize.Render("")
			if !entry.IsDir {
				sizeStr = styleSize.Render(formatSize(entry.Size))
			}

			selMark := "  "
			if selected {
				selMark = lipgloss.NewStyle().Foreground(colorAccent).Render("✓ ")
			}

			cols := []string{
				selMark,
				nameCol.Render(nameStyled),
				sizeStr,
			}
			if showMTime {
				cols = append(cols, "  ", mtimeCol.Render(formatMTime(entry.ModTime)))
			}
			if showMode {
				cols = append(cols, "  ", modeCol.Render(entry.Mode.String()))
			}
			row := lipgloss.JoinHorizontal(lipgloss.Left, cols...)
			if isCursor {
				row = styleSelected.Width(m.width).Render(row)
			} else {
				row = styleNormal.Width(m.width).Render(row)
			}
			rows = append(rows, row)
		}
		blank := lipgloss.NewStyle().Width(m.width).Render("")
		for len(rows) < visible {
			rows = append(rows, blank)
		}
		content = strings.Join(rows, "\n")
	}

	// Scroll / selection indicator
	scrollInfo := ""
	if len(m.visible) > 0 {
		scrollInfo = fmt.Sprintf("%d/%d", m.cursor+1, len(m.visible))
	}
	if len(m.selection) > 0 {
		scrollInfo += fmt.Sprintf("  %s selected", styleStatusOk.Render(fmt.Sprintf("%d", len(m.selection))))
	}

	statusMsg := ""
	if m.status != "" {
		statusMsg = "  " + styleStatusOk.Render(m.status)
	}

	statusBar := styleStatusBar.Width(m.width).MaxHeight(1).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			statusMsg,
			lipgloss.NewStyle().Foreground(colorMuted).Render(scrollInfo),
		),
	)

	hintText := keyHint("↑↓", "nav") + "  " +
		keyHint("↵", "open") + "  " +
		keyHint("d/u", "dl/up") + "  " +
		keyHint("D/r/M/c", "ops") + "  " +
		keyHint("/", "filter") + "  " +
		keyHint("s", "sort") + "  " +
		keyHint("?", "help") + "  " +
		keyHint("q", "quit")
	hints := lipgloss.NewStyle().
		Background(colorBgAlt).
		Width(m.width).
		MaxHeight(1).
		Padding(0, 1).
		Render(hintText)

	var base string
	if m.twoPane {
		base = lipgloss.JoinVertical(lipgloss.Left, pathBar, content, statusBar)
	} else {
		base = lipgloss.JoinVertical(lipgloss.Left, header, pathBar, content, statusBar, hints)
	}

	switch m.mode {
	case modeFilter:
		return overlay(base, m.viewFilter(), m.width, m.height)
	case modePrompt:
		return overlay(base, m.viewPrompt(), m.width, m.height)
	case modeConfirm:
		return overlay(base, m.viewConfirm(), m.width, m.height)
	case modeHelp:
		return overlay(base, m.viewHelp(), m.width, m.height)
	}
	return base
}

func (m BrowserModel) viewFilter() string {
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1)
	body := []string{
		lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Filter"),
		"",
		inputStyle.Render(m.filterInput.View()),
		"",
		"  " + keyHint("enter", "apply") + "   " + keyHint("esc", "clear"),
	}
	return stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}

func (m BrowserModel) viewPrompt() string {
	var title string
	switch m.promptKind {
	case promptRename:
		title = "Rename"
	case promptMkdir:
		title = "Make directory"
	case promptChmod:
		title = "Change mode"
	}
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1)
	body := []string{
		lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(title),
		"",
		styleSubtitle.Render(m.promptTarget),
		"",
		styleKeyHint.Render(m.promptLabel),
		inputStyle.Render(m.promptInput.View()),
		"",
		"  " + keyHint("enter", "ok") + "   " + keyHint("esc", "cancel"),
	}
	return stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}

func (m BrowserModel) viewConfirm() string {
	body := []string{
		lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("⚠  Confirm delete"),
		"",
		m.confirmBody,
		"",
		"  " + keyHint("y", "yes") + "   " + keyHint("n/esc", "no"),
	}
	return stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}

func (m BrowserModel) viewHelp() string {
	heading := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Keyboard shortcuts")
	rows := [][2]string{
		{"↑↓ / jk", "navigate"},
		{"→ / ↵ / l", "open dir or preview file"},
		{"← / h / bs", "parent dir"},
		{"g / G", "top / bottom"},
		{"pgup / pgdn", "page"},
		{"R", "refresh"},
		{"", ""},
		{"space", "toggle select"},
		{"a", "select / clear all"},
		{"esc", "clear selection"},
		{"", ""},
		{"d", "download"},
		{"u", "upload"},
		{"v / ↵", "preview text file"},
		{"D", "delete (recursive)"},
		{"r", "rename"},
		{"M", "mkdir"},
		{"c", "chmod (octal)"},
		{"", ""},
		{"/", "filter"},
		{".", "toggle hidden"},
		{"s / S", "sort mode / direction"},
		{"", ""},
		{"b", "bookmark this path"},
		{"B", "open bookmarks"},
		{"", ""},
		{"T", "open two-pane (mc-style)"},
		{"", ""},
		{"?", "this help"},
		{"q", "quit"},
	}
	lines := []string{heading, ""}
	keyStyle := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Width(14)
	descStyle := lipgloss.NewStyle().Foreground(colorText)
	for _, r := range rows {
		if r[0] == "" && r[1] == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, keyStyle.Render(r[0])+descStyle.Render(r[1]))
	}
	lines = append(lines, "", "  "+keyHint("?/esc/q", "close"))
	return stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// overlay replaces the base view with a centered panel. Bubble Tea has no
// true overlay primitive, so any active modal takes over the whole screen
// instead of compositing on top.
func overlay(_, panel string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel)
}

func styleSecondary(s string) string {
	return lipgloss.NewStyle().Foreground(colorSecondary).Render(s)
}

func fileIcon(e sftpclient.FileEntry) string {
	if e.IsDir {
		return ""
	}
	ext := strings.ToLower(path.Ext(e.Name))
	switch ext {
	case ".go":
		return ""
	case ".py":
		return ""
	case ".js", ".ts", ".jsx", ".tsx":
		return ""
	case ".json", ".yaml", ".yml", ".toml":
		return ""
	case ".md", ".txt", ".rst":
		return ""
	case ".sh", ".bash", ".zsh":
		return ""
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z":
		return ""
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp":
		return ""
	case ".mp4", ".mkv", ".avi", ".mov":
		return ""
	case ".mp3", ".flac", ".wav", ".ogg":
		return ""
	case ".pdf":
		return ""
	case ".html", ".css":
		return ""
	case ".c", ".cpp", ".h", ".rs", ".java":
		return ""
	case ".db", ".sqlite", ".sql":
		return ""
	case ".env", ".cfg", ".conf", ".ini":
		return ""
	default:
		return ""
	}
}

// truncate shortens s to fit a terminal-column width of max, appending "…"
// when truncation happens. Uses display widths (handles multi-byte runes and
// wide CJK/emoji), unlike a naive byte-slice which can split a UTF-8 codepoint.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	return runewidth.Truncate(s, max, "…")
}

func formatMTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := time.Now()
	if t.Year() == now.Year() {
		return t.Format("Jan _2 15:04")
	}
	return t.Format("Jan _2  2006")
}

