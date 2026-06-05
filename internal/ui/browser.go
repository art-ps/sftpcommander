package ui

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

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

type editStartMsg struct {
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
	modeFindInput
)

type promptKind int

const (
	promptRename promptKind = iota
	promptMkdir
	promptChmod
	promptChmodRecursive
	promptCopy
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

type symlinkResolvedMsg struct {
	path   string
	target string
	side   int
}

// findResultsMsg delivers the result of an async recursive walk back to the
// originating browser side. err is set on the first I/O error encountered;
// partial results are still returned so the user sees what was found.
type findResultsMsg struct {
	root    string
	pattern string
	entries []sftpclient.FileEntry
	err     error
	side    int
}

type BrowserModel struct {
	fs            FileSystem
	side          int // identifier so two-pane mode can route async msgs back
	path          string
	entries       []sftpclient.FileEntry // raw from fs
	sortedEntries []sftpclient.FileEntry // entries sorted by current sortBy/sortDesc (cache)
	visible       []sftpclient.FileEntry // filtered subset of sortedEntries (buffer reused)
	cursor        int
	offset        int
	loading       bool
	err           string
	status        string
	width         int
	height        int
	selection     map[string]bool

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

	// Recursive find state. findActive replaces m.entries with results
	// collected by a tree walk; findRoot remembers the directory we walked
	// from so esc/R can return to that listing. findInput is the modal input
	// shown while modeFindInput is active.
	findActive   bool
	findRoot     string
	findInput    textinput.Model
	pendingFocus string // basename to focus after next loadDir completes

	helpOffset int

	// symlinkTargets caches Readlink results so cursor movement doesn't fire
	// a network round-trip on every keystroke. Empty value means "tried,
	// failed" — used as a negative cache so retries don't loop.
	symlinkTargets map[string]string
	symlinkPending map[string]bool
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

	find := textinput.New()
	find.CharLimit = 256
	find.Prompt = ""
	find.Width = 48

	return BrowserModel{
		fs:             fs,
		side:           side,
		path:           fs.Home(),
		loading:        true,
		selection:      make(map[string]bool),
		cursorMem:      make(map[string]cursorState),
		filterInput:    filter,
		promptInput:    prompt,
		findInput:      find,
		symlinkTargets: make(map[string]string),
		symlinkPending: make(map[string]bool),
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
	if inv, ok := m.fs.(interface{ Invalidate(string) }); ok {
		inv.Invalidate(m.path)
	}
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

// rebuildSorted refreshes the sortedEntries cache from m.entries using the
// current sort mode/direction. Called when entries change or sort changes;
// applyView only reads from this cache.
func (m *BrowserModel) rebuildSorted() {
	if cap(m.sortedEntries) >= len(m.entries) {
		m.sortedEntries = m.sortedEntries[:len(m.entries)]
	} else {
		m.sortedEntries = make([]sftpclient.FileEntry, len(m.entries))
	}
	copy(m.sortedEntries, m.entries)
	sortEntries(m.sortedEntries, m.sortBy, m.sortDesc)
}

func (m *BrowserModel) applyView() {
	match := compileFilter(m.filterInput.Value())
	m.visible = m.visible[:0]
	for _, e := range m.sortedEntries {
		if !m.showHidden && strings.HasPrefix(e.Name, ".") {
			continue
		}
		if !match(e.Name) {
			continue
		}
		m.visible = append(m.visible, e)
	}
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

// compileFilter returns a name-match predicate from the user's filter string.
// Recognised forms:
//   - "re:PATTERN"       — Go regexp, case-insensitive, anchored loosely (any
//     match counts; use ^/$ for anchoring).
//   - contains * or ?    — glob via path.Match against the basename.
//   - anything else      — case-insensitive substring match.
//
// An invalid regex/glob falls back to literal substring matching so the user
// keeps seeing results while typing.
func compileFilter(raw string) func(string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return func(string) bool { return true }
	}
	if pat, ok := strings.CutPrefix(s, "re:"); ok {
		re, err := regexp.Compile("(?i)" + pat)
		if err != nil {
			lower := strings.ToLower(s)
			return func(name string) bool {
				return strings.Contains(strings.ToLower(name), lower)
			}
		}
		return func(name string) bool { return re.MatchString(name) }
	}
	if strings.ContainsAny(s, "*?[") {
		lower := strings.ToLower(s)
		return func(name string) bool {
			ok, err := path.Match(lower, strings.ToLower(name))
			if err != nil {
				return strings.Contains(strings.ToLower(name), lower)
			}
			return ok
		}
	}
	lower := strings.ToLower(s)
	return func(name string) bool {
		return strings.Contains(strings.ToLower(name), lower)
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
		if inv, ok := m.fs.(interface{ Invalidate(string) }); ok {
			inv.Invalidate(m.path)
		}
		return m, m.loadDir(m.path)

	case entriesLoadedMsg:
		if msg.side != m.side {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			if msg.path != m.path && m.filterInput.Value() != "" {
				m.filterInput.SetValue("")
			}
			m.path = msg.path
			m.entries = msg.entries
			m.err = ""
			m.rebuildSorted()
			m.applyView()
			switch {
			case m.pendingFocus != "":
				m.cursor = 0
				m.offset = 0
				for i, e := range m.visible {
					if e.Name == m.pendingFocus {
						m.cursor = i
						vis := m.visibleRows()
						if m.cursor >= vis {
							m.offset = m.cursor - vis + 1
						}
						break
					}
				}
				m.pendingFocus = ""
			default:
				if cs, ok := m.cursorMem[m.path]; ok && cs.cursor < len(m.visible) {
					m.cursor = cs.cursor
					m.offset = min(cs.offset, m.cursor)
				} else {
					m.cursor = 0
					m.offset = 0
				}
			}
		}
		return m, m.maybeResolveSymlink()

	case symlinkResolvedMsg:
		if msg.side != m.side {
			return m, nil
		}
		delete(m.symlinkPending, msg.path)
		m.symlinkTargets[msg.path] = msg.target
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
		// Most ops route through the FS interface and CachedFS already
		// invalidated the parent dir. CopyRemote bypasses that path, so
		// invalidate m.path defensively before reloading.
		if inv, ok := m.fs.(interface{ Invalidate(string) }); ok {
			inv.Invalidate(m.path)
		}
		return m, m.loadDir(m.path)

	case findResultsMsg:
		if msg.side != m.side {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = "find: " + msg.err.Error()
		}
		m.findActive = true
		m.findRoot = msg.root
		m.entries = msg.entries
		m.rebuildSorted()
		m.applyView()
		m.cursor = 0
		m.offset = 0
		if len(m.visible) == 0 && msg.err == nil {
			m.status = "no matches for " + msg.pattern
		} else if msg.err == nil {
			m.status = fmt.Sprintf("found %d in %s", len(m.visible), msg.root)
		}
		return m, nil

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
		case modeFindInput:
			return m.updateFindInput(msg)
		}
		return m.updateBrowse(msg)
	}
	return m, nil
}

// maybeResolveSymlink dispatches a Readlink for the entry under the cursor
// when it's a symlink we haven't resolved yet. Returns nil if no work needed.
func (m *BrowserModel) maybeResolveSymlink() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	e := m.visible[m.cursor]
	if !e.IsSymlink {
		return nil
	}
	if _, ok := m.symlinkTargets[e.Path]; ok {
		return nil
	}
	if m.symlinkPending[e.Path] {
		return nil
	}
	m.symlinkPending[e.Path] = true
	fs := m.fs
	side := m.side
	p := e.Path
	return func() tea.Msg {
		target, err := fs.Readlink(p)
		if err != nil {
			target = ""
		}
		return symlinkResolvedMsg{path: p, target: target, side: side}
	}
}

func (m BrowserModel) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		if m.findActive {
			// Jump to the entry's directory: cd to parent and position cursor
			// on the basename. For directories themselves, cd into them.
			m.findActive = false
			m.findRoot = ""
			m.loading = true
			if entry.IsDir {
				return m, m.loadDir(entry.Path)
			}
			m.pendingFocus = m.fs.Base(entry.Path)
			return m, m.loadDir(m.fs.Dir(entry.Path))
		}
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
		if m.findActive {
			return m, m.exitFind()
		}
		parent := m.fs.Dir(m.path)
		if parent != m.path {
			m.cursorMem[m.path] = cursorState{m.cursor, m.offset}
			m.loading = true
			return m, m.loadDir(parent)
		}

	case "R":
		m.selection = make(map[string]bool)
		m.err = ""
		m.status = ""
		if m.findActive {
			return m, m.exitFind()
		}
		m.loading = true
		if inv, ok := m.fs.(interface{ Invalidate(string) }); ok {
			inv.Invalidate(m.path)
		}
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
		switch {
		case m.findActive:
			return m, m.exitFind()
		case len(m.selection) > 0:
			m.selection = make(map[string]bool)
		case m.err != "" || m.status != "":
			m.err = ""
			m.status = ""
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
		m.rebuildSorted()
		m.applyView()

	case "S":
		m.sortDesc = !m.sortDesc
		m.rebuildSorted()
		m.applyView()

	case "F":
		m.mode = modeFindInput
		m.findInput.SetValue("")
		m.findInput.Focus()
		return m, textinput.Blink

	case "E":
		return m, func() tea.Msg { return openErrLogMsg{} }

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

	case "C":
		if len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		mode := fmt.Sprintf("%o", entry.Mode.Perm())
		m.openPrompt(promptChmodRecursive, entry.Path, "Mode (octal, recursive):", mode)
		return m, textinput.Blink

	case "f5":
		if m.fs.Kind() != "remote" || len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		m.openPrompt(promptCopy, entry.Path, "Copy to (path):", m.fs.Join(m.fs.Dir(entry.Path), entry.Name+".copy"))
		return m, textinput.Blink

	case "e":
		if m.fs.Kind() != "remote" || len(m.visible) == 0 {
			break
		}
		entry := m.visible[m.cursor]
		if entry.IsDir || entry.IsSymlink {
			break
		}
		fs := m.fs
		return m, func() tea.Msg { return editStartMsg{entry: entry, fs: fs} }
	}
	return m, m.maybeResolveSymlink()
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

func (m BrowserModel) updateFindInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.findInput.Blur()
		m.mode = modeBrowse
		return m, nil
	case "enter":
		pattern := strings.TrimSpace(m.findInput.Value())
		m.findInput.Blur()
		m.mode = modeBrowse
		if pattern == "" {
			return m, nil
		}
		m.loading = true
		m.status = "searching " + m.path + "..."
		return m, m.runFind(m.path, pattern)
	}
	var cmd tea.Cmd
	m.findInput, cmd = m.findInput.Update(msg)
	return m, cmd
}

// runFind walks the directory tree under root looking for entries whose name
// matches pattern (parsed by compileFilter — so re:/glob/substring all work).
// Found entries get their Name rewritten to the path relative to root so the
// list view shows context. Path stays absolute for ops.
func (m BrowserModel) runFind(root, pattern string) tea.Cmd {
	fs := m.fs
	side := m.side
	match := compileFilter(pattern)
	const limit = 5000
	return func() tea.Msg {
		var (
			results  []sftpclient.FileEntry
			firstErr error
		)
		var walk func(dir string)
		walk = func(dir string) {
			if firstErr != nil || len(results) >= limit {
				return
			}
			entries, err := fs.List(dir)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", dir, err)
				}
				return
			}
			for _, e := range entries {
				if match(e.Name) {
					display := strings.TrimPrefix(e.Path, root)
					display = strings.TrimPrefix(display, "/")
					if display == "" {
						display = e.Name
					}
					hit := e
					hit.Name = display
					results = append(results, hit)
					if len(results) >= limit {
						return
					}
				}
				if e.IsDir && !e.IsSymlink {
					walk(e.Path)
				}
			}
		}
		walk(root)
		return findResultsMsg{root: root, pattern: pattern, entries: results, err: firstErr, side: side}
	}
}

// exitFind clears recursive-find state and reloads the original path. Called
// from R/esc/cd-up in browse mode.
func (m *BrowserModel) exitFind() tea.Cmd {
	if !m.findActive {
		return nil
	}
	m.findActive = false
	root := m.findRoot
	m.findRoot = ""
	m.entries = nil
	m.rebuildSorted()
	m.applyView()
	m.loading = true
	return m.loadDir(root)
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
		case promptChmodRecursive:
			n, err := strconv.ParseUint(value, 8, 32)
			if err != nil {
				return opDoneMsg{op: "chmod -R", target: target, err: fmt.Errorf("invalid octal mode: %w", err), side: side}
			}
			return opDoneMsg{op: "chmod -R", target: target, err: chmodRecursive(fs, target, os.FileMode(n)), side: side}
		case promptCopy:
			dest := value
			if !strings.HasPrefix(dest, "/") {
				dest = fs.Join(fs.Dir(target), dest)
			}
			rfs, ok := unwrapFS(fs).(*RemoteFS)
			if !ok {
				return opDoneMsg{op: "copy", target: target, err: fmt.Errorf("copy only works on remote fs"), side: side}
			}
			return opDoneMsg{op: "copy", target: target, err: rfs.Client().CopyRemote(target, dest), side: side}
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
		m.helpOffset = 0
	case "up", "k":
		if m.helpOffset > 0 {
			m.helpOffset--
		}
	case "down", "j":
		m.helpOffset++
	case "pgup":
		m.helpOffset -= 10
		if m.helpOffset < 0 {
			m.helpOffset = 0
		}
	case "pgdown":
		m.helpOffset += 10
	case "g":
		m.helpOffset = 0
	case "G":
		m.helpOffset = 1 << 30 // clamped by view
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
			styleTitle.Render("  SFTP Commander  "),
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
	if m.findActive {
		sortInd += " [find]"
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
			if entry.IsDir || entry.IsSymlink {
				trailing = 1
			}
			name := truncate(entry.Name, nameWidth-2-trailing)
			var nameStyled string
			switch {
			case entry.IsSymlink:
				suffix := "@"
				if entry.IsDir {
					suffix = "/"
				}
				nameStyled = lipgloss.NewStyle().Foreground(colorAccent).Render(icon + " " + name + suffix)
			case entry.IsDir:
				nameStyled = styleDir.Render(icon + " " + name + "/")
			default:
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
		var selBytes int64
		for _, e := range m.visible {
			if m.selection[e.Path] && !e.IsDir {
				selBytes += e.Size
			}
		}
		scrollInfo += fmt.Sprintf("  %s selected (%s)",
			styleStatusOk.Render(fmt.Sprintf("%d", len(m.selection))),
			formatSize(selBytes))
	} else if len(m.visible) > 0 {
		var files, dirs int
		var totalBytes int64
		for _, e := range m.visible {
			if e.IsDir {
				dirs++
			} else {
				files++
				totalBytes += e.Size
			}
		}
		scrollInfo += fmt.Sprintf("  %dd %df %s", dirs, files, formatSize(totalBytes))
	}

	statusMsg := ""
	if m.status != "" {
		statusMsg = "  " + styleStatusOk.Render(m.status)
	}
	if statusMsg == "" && len(m.visible) > 0 && m.cursor >= 0 && m.cursor < len(m.visible) {
		cur := m.visible[m.cursor]
		if cur.IsSymlink {
			if target, ok := m.symlinkTargets[cur.Path]; ok && target != "" {
				statusMsg = "  " + lipgloss.NewStyle().Foreground(colorAccent).Render("→ "+target)
			}
		}
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
		keyHint("F", "find") + "  " +
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
	case modeFindInput:
		return overlay(base, m.viewFindInput(), m.width, m.height)
	}
	return base
}

func (m BrowserModel) viewFindInput() string {
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1)
	body := []string{
		lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Recursive find"),
		"",
		styleSubtitle.Render("under " + m.path),
		"",
		styleKeyHint.Render("pattern (substring, *.go, re:^foo)"),
		inputStyle.Render(m.findInput.View()),
		"",
		"  " + keyHint("enter", "search") + "   " + keyHint("esc", "cancel"),
	}
	return stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...))
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
		{"e", "edit ($EDITOR, remote)"},
		{"v / ↵", "preview text file"},
		{"D", "delete (recursive)"},
		{"r", "rename"},
		{"F5", "copy on remote"},
		{"M", "mkdir"},
		{"c / C", "chmod (single / recursive)"},
		{"", ""},
		{"/", "filter (re:RE, *.glob, substr)"},
		{"F", "recursive find under cwd"},
		{".", "toggle hidden"},
		{"s / S", "sort mode / direction"},
		{"", ""},
		{"b", "bookmark this path"},
		{"B", "open bookmarks"},
		{"", ""},
		{"T", "open two-pane (mc-style)"},
		{"", ""},
		{"E", "open error log"},
		{"?", "this help"},
		{"q", "quit"},
	}
	var lines []string
	keyStyle := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Width(14)
	descStyle := lipgloss.NewStyle().Foreground(colorText)
	for _, r := range rows {
		if r[0] == "" && r[1] == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, keyStyle.Render(r[0])+descStyle.Render(r[1]))
	}

	// Window: title(1) + blank(1) + body(N) + blank(1) + footer(1) + panel chrome(4).
	bodyMax := m.height - 8
	if bodyMax < 5 {
		bodyMax = 5
	}
	maxOffset := len(lines) - bodyMax
	if maxOffset < 0 {
		maxOffset = 0
	}
	offset := m.helpOffset
	if offset > maxOffset {
		offset = maxOffset
	}
	end := offset + bodyMax
	if end > len(lines) {
		end = len(lines)
	}
	body := lines[offset:end]

	scrollHint := ""
	if maxOffset > 0 {
		scrollHint = fmt.Sprintf("  [%d-%d/%d]", offset+1, end, len(lines))
	}

	out := append([]string{heading, ""}, body...)
	out = append(out, "", "  "+keyHint("↑↓/jk", "scroll")+
		"   "+keyHint("g/G", "top/end")+
		"   "+keyHint("?/esc/q", "close")+
		scrollHint)
	return stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, out...))
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

// useNerdFont is decided once at startup. SFTP_NO_NF=1 forces the ASCII
// fallback for terminals that don't have a Nerd Font configured.
var useNerdFont = os.Getenv("SFTP_NO_NF") != "1"

func fileIcon(e sftpclient.FileEntry) string {
	if !useNerdFont {
		return asciiFileIcon(e)
	}
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

// asciiFileIcon returns single-character category markers for terminals
// without Nerd Font glyphs. Width is always one column so the layout math
// in View() stays correct.
func asciiFileIcon(e sftpclient.FileEntry) string {
	if e.IsDir {
		return "d"
	}
	if e.IsSymlink {
		return "l"
	}
	ext := strings.ToLower(path.Ext(e.Name))
	switch ext {
	case ".go", ".py", ".js", ".ts", ".jsx", ".tsx",
		".c", ".cpp", ".h", ".rs", ".java", ".html", ".css":
		return "c"
	case ".json", ".yaml", ".yml", ".toml",
		".env", ".cfg", ".conf", ".ini":
		return "k"
	case ".md", ".txt", ".rst":
		return "t"
	case ".sh", ".bash", ".zsh":
		return "x"
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z":
		return "a"
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp":
		return "i"
	case ".mp4", ".mkv", ".avi", ".mov":
		return "v"
	case ".mp3", ".flac", ".wav", ".ogg":
		return "m"
	case ".pdf":
		return "p"
	case ".db", ".sqlite", ".sql":
		return "s"
	default:
		return "f"
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
