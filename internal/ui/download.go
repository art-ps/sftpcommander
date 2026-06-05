package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type backToBrowserMsg struct{}

type downloadState int

const (
	stateEditDest downloadState = iota
	stateDownloading
	stateAskContinue
	stateAskOverwrite
	stateDone
)

type failureInfo struct {
	path string
	err  error
}

type overwriteAsk struct {
	path         string
	existingSize int64
	newSize      int64
}

// resumable reports whether the existing destination is a strict prefix
// candidate of the new source — only then does OverwriteResume make sense.
// Equal sizes are excluded because they'd produce a zero-byte resume, and
// existing > new can't be a partial transfer of the new file.
func resumable(a *overwriteAsk) bool {
	if a == nil {
		return false
	}
	return a.existingSize > 0 && a.existingSize < a.newSize
}

type overwriteEvent struct {
	silent *overwriteAsk
}

type downloadEvent struct {
	progress         *sftpclient.DownloadProgress
	failure          *failureInfo
	silentSkip       *failureInfo
	overwrite        *overwriteAsk
	silentOverwrite  *overwriteEvent
	done             bool
	finalErr         error
}

type downloadEventMsg downloadEvent

type userChoice int

const (
	choiceSkip userChoice = iota
	choiceSkipAll
	choiceAbort
	choiceOverwrite
	choiceOverwriteAll
	choiceResume
)

type DownloadModel struct {
	client          *sftpclient.Client
	entries         []sftpclient.FileEntry
	localDir        string
	destInput       textinput.Model
	state           downloadState
	bar             progress.Model
	written         int64
	total           int64
	err             string
	failure         *failureInfo
	overwrite       *overwriteAsk
	skipped         []string
	overwritten     int
	skipAllOn       bool
	overwriteAllOn  bool
	startTime       time.Time
	width           int
	height          int
	eventCh         chan downloadEvent
	decisionCh      chan userChoice
	currentFile     string
	scanning        bool
	scanFile        string
	filesDone       int64
	filesTotal      int64
	verify          bool
}

func NewDownloadModel(client *sftpclient.Client, entries []sftpclient.FileEntry, localDir string) DownloadModel {
	bar := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(60),
	)
	dest := localDir
	if len(entries) == 1 && !entries[0].IsDir {
		dest = filepath.Join(localDir, filepath.Base(entries[0].Path))
	}
	input := textinput.New()
	input.SetValue(dest)
	input.CharLimit = 1024
	input.Width = 60
	input.Prompt = ""
	input.Focus()
	return DownloadModel{
		client:    client,
		entries:   entries,
		localDir:  localDir,
		destInput: input,
		bar:       bar,
		state:     stateEditDest,
	}
}

func (m DownloadModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m DownloadModel) startDownload() tea.Cmd {
	return func() tea.Msg {
		aborted := false
		skipAll := false
		overwriteAll := false
		skipAllOverwrite := false
		onFailure := func(path string, err error) sftpclient.FailureDecision {
			if aborted {
				return sftpclient.DecisionAbort
			}
			if skipAll {
				select {
				case m.eventCh <- downloadEvent{silentSkip: &failureInfo{path: path, err: err}}:
				default:
				}
				return sftpclient.DecisionSkip
			}
			m.eventCh <- downloadEvent{failure: &failureInfo{path: path, err: err}}
			switch <-m.decisionCh {
			case choiceSkipAll:
				skipAll = true
				return sftpclient.DecisionSkip
			case choiceAbort:
				aborted = true
				return sftpclient.DecisionAbort
			default:
				return sftpclient.DecisionSkip
			}
		}
		onOverwrite := func(path string, existingSize, newSize int64) sftpclient.OverwriteDecision {
			if aborted {
				return sftpclient.OverwriteAbort
			}
			if overwriteAll {
				return sftpclient.OverwriteReplace
			}
			if skipAllOverwrite {
				select {
				case m.eventCh <- downloadEvent{silentOverwrite: &overwriteEvent{silent: &overwriteAsk{path: path, existingSize: existingSize, newSize: newSize}}}:
				default:
				}
				return sftpclient.OverwriteSkip
			}
			m.eventCh <- downloadEvent{overwrite: &overwriteAsk{path: path, existingSize: existingSize, newSize: newSize}}
			switch <-m.decisionCh {
			case choiceOverwrite:
				return sftpclient.OverwriteReplace
			case choiceOverwriteAll:
				overwriteAll = true
				return sftpclient.OverwriteReplace
			case choiceResume:
				return sftpclient.OverwriteResume
			case choiceSkipAll:
				skipAllOverwrite = true
				return sftpclient.OverwriteSkip
			case choiceAbort:
				aborted = true
				return sftpclient.OverwriteAbort
			default:
				return sftpclient.OverwriteSkip
			}
		}
		progressCb := func(p sftpclient.DownloadProgress) {
			if p.ScanFile == "" && p.File != "" {
				p.File = filepath.Base(p.File)
			}
			select {
			case m.eventCh <- downloadEvent{progress: &p}:
			default:
			}
		}

		items := make([]sftpclient.BatchItem, 0, len(m.entries))
		for _, entry := range m.entries {
			var localPath string
			if len(m.entries) == 1 && !entry.IsDir {
				localPath = m.localDir
			} else {
				localPath = filepath.Join(m.localDir, filepath.Base(entry.Path))
			}
			items = append(items, sftpclient.BatchItem{
				RemotePath: entry.Path,
				LocalPath:  localPath,
				IsDir:      entry.IsDir,
			})
		}

		opts := sftpclient.BatchOptions{Parallel: 4, OnOverwrite: onOverwrite, Verify: m.verify}
		if err := m.client.DownloadBatch(items, opts, progressCb, onFailure); err != nil {
			m.eventCh <- downloadEvent{done: true, finalErr: err}
			return nil
		}
		m.eventCh <- downloadEvent{done: true}
		return nil
	}
}

func (m DownloadModel) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.eventCh
		if !ok {
			return nil
		}
		return downloadEventMsg(ev)
	}
}

func (m *DownloadModel) beginDownload() tea.Cmd {
	dest := strings.TrimSpace(m.destInput.Value())
	if dest == "" {
		m.err = "destination path cannot be empty"
		return nil
	}
	if strings.HasPrefix(dest, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dest = filepath.Join(home, dest[2:])
		}
	}
	// For a single file download dest is the target file path, not a directory.
	// MkdirAll must only create its parent, not the path itself.
	mkdirTarget := dest
	if len(m.entries) == 1 && !m.entries[0].IsDir {
		mkdirTarget = filepath.Dir(dest)
	}
	if err := os.MkdirAll(mkdirTarget, 0o755); err != nil {
		m.err = "cannot create destination: " + err.Error()
		return nil
	}
	m.localDir = dest
	m.destInput.SetValue(dest)
	m.err = ""
	m.failure = nil
	m.skipped = nil
	m.state = stateDownloading
	m.startTime = time.Now()
	m.written = 0
	m.total = 0
	m.eventCh = make(chan downloadEvent, 64)
	m.decisionCh = make(chan userChoice, 1)
	m.skipAllOn = false
	return tea.Batch(m.startDownload(), m.waitForEvent())
}

func (m DownloadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.bar.Width = min(m.width-20, 80)
		m.destInput.Width = min(m.width-20, 80)

	case downloadEventMsg:
		ev := downloadEvent(msg)
		switch {
		case ev.progress != nil:
			if ev.progress.ScanFile != "" {
				m.scanning = true
				m.scanFile = ev.progress.ScanFile
				if ev.progress.FilesTotal > 0 {
					m.filesTotal = ev.progress.FilesTotal
				}
				if ev.progress.Total > 0 {
					m.total = ev.progress.Total
				}
			} else {
				m.scanning = false
				m.written = ev.progress.Written
				m.total = ev.progress.Total
				m.currentFile = ev.progress.File
				m.filesDone = ev.progress.FilesDone
				m.filesTotal = ev.progress.FilesTotal
			}
			return m, m.waitForEvent()
		case ev.failure != nil:
			m.failure = ev.failure
			m.state = stateAskContinue
			return m, nil
		case ev.overwrite != nil:
			m.overwrite = ev.overwrite
			m.state = stateAskOverwrite
			return m, nil
		case ev.silentOverwrite != nil:
			if ev.silentOverwrite.silent != nil {
				m.skipped = append(m.skipped, ev.silentOverwrite.silent.path)
			}
			return m, m.waitForEvent()
		case ev.silentSkip != nil:
			m.skipped = append(m.skipped, ev.silentSkip.path)
			return m, m.waitForEvent()
		case ev.done:
			if ev.finalErr != nil {
				m.err = ev.finalErr.Error()
				m.state = stateEditDest
				m.destInput.Focus()
				return m, textinput.Blink
			}
			m.state = stateDone
			return m, nil
		}

	case tea.KeyMsg:
		switch m.state {
		case stateEditDest:
			switch msg.String() {
			case "esc", "ctrl+c":
				return m, func() tea.Msg { return backToBrowserMsg{} }
			case "enter":
				return m, m.beginDownload()
			case "ctrl+v":
				m.verify = !m.verify
				return m, nil
			}
			var cmd tea.Cmd
			m.destInput, cmd = m.destInput.Update(msg)
			return m, cmd

		case stateAskOverwrite:
			switch msg.String() {
			case "o", "enter":
				m.overwritten++
				m.decisionCh <- choiceOverwrite
				m.overwrite = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "O":
				m.overwriteAllOn = true
				m.decisionCh <- choiceOverwriteAll
				m.overwrite = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "r":
				if m.overwrite == nil || !resumable(m.overwrite) {
					return m, nil
				}
				m.decisionCh <- choiceResume
				m.overwrite = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "s", "y":
				if m.overwrite != nil {
					m.skipped = append(m.skipped, m.overwrite.path)
				}
				m.decisionCh <- choiceSkip
				m.overwrite = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "S", "A":
				if m.overwrite != nil {
					m.skipped = append(m.skipped, m.overwrite.path)
				}
				m.skipAllOn = true
				m.decisionCh <- choiceSkipAll
				m.overwrite = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "esc", "ctrl+c", "a":
				m.decisionCh <- choiceAbort
				m.overwrite = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			}
			return m, nil

		case stateAskContinue:
			switch msg.String() {
			case "enter", "y", "s":
				if m.failure != nil {
					m.skipped = append(m.skipped, m.failure.path)
				}
				m.decisionCh <- choiceSkip
				m.failure = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "c", "A":
				if m.failure != nil {
					m.skipped = append(m.skipped, m.failure.path)
				}
				m.skipAllOn = true
				m.decisionCh <- choiceSkipAll
				m.failure = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			case "esc", "ctrl+c", "n", "a":
				m.decisionCh <- choiceAbort
				m.failure = nil
				m.state = stateDownloading
				return m, m.waitForEvent()
			}

		case stateDownloading:
			switch msg.String() {
			case "ctrl+c", "q":
				select {
				case m.decisionCh <- choiceAbort:
				default:
				}
				return m, func() tea.Msg { return backToBrowserMsg{} }
			}

		case stateDone:
			switch msg.String() {
			case "ctrl+c", "q", "enter", "esc":
				return m, func() tea.Msg { return backToBrowserMsg{} }
			}
		}
	}

	var cmd tea.Cmd
	var barModel tea.Model
	barModel, cmd = m.bar.Update(msg)
	m.bar = barModel.(progress.Model)
	return m, cmd
}

func (m DownloadModel) View() string {
	var name, kind string
	if len(m.entries) == 1 {
		entry := m.entries[0]
		name = filepath.Base(entry.Path)
		kind = "file"
		if entry.IsDir {
			kind = "folder"
		}
	} else {
		name = fmt.Sprintf("%d items", len(m.entries))
		kind = "items"
	}

	var heading string
	switch m.state {
	case stateEditDest:
		if m.err != "" {
			heading = "  Retry download"
		} else {
			heading = "  Confirm destination"
		}
	case stateDownloading:
		if m.scanning {
			heading = "  Scanning..."
		} else {
			heading = "  Downloading " + kind
		}
	case stateAskContinue:
		heading = "  File error"
	case stateAskOverwrite:
		heading = "  File exists"
	case stateDone:
		heading = "  Download complete"
	}

	bodyWidth := max(40, min(m.width-10, 90))

	var body []string
	body = append(body, styleTitle.Render("  "+name+"  "), "")

	switch m.state {
	case stateEditDest:
		inputStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1)
		body = append(body, styleKeyHint.Render("  Destination:"))
		body = append(body, "  "+inputStyle.Render(m.destInput.View()))
		if m.err != "" {
			body = append(body, "")
			errStyle := lipgloss.NewStyle().Foreground(colorError).Bold(true).Width(bodyWidth)
			body = append(body, errStyle.Render("  Error: "+m.err))
		}
		body = append(body, "")
		verifyTag := "off"
		if m.verify {
			verifyTag = styleStatusOk.Render("on")
		}
		body = append(body, styleKeyHint.Render("  SHA256 verify: "+verifyTag))
		body = append(body, "")
		body = append(body, "  "+keyHint("enter", "start")+"   "+keyHint("^v", "toggle verify")+"   "+keyHint("esc", "cancel"))

	case stateAskContinue:
		path := ""
		errMsg := ""
		if m.failure != nil {
			path = m.failure.path
			errMsg = m.failure.err.Error()
		}
		wrapStyle := lipgloss.NewStyle().Width(bodyWidth)
		errStyle := lipgloss.NewStyle().Foreground(colorError).Bold(true).Width(bodyWidth)

		body = append(body, styleKeyHint.Render("  Failed to download:"))
		body = append(body, wrapStyle.Foreground(colorWarning).Render("  "+path))
		body = append(body, "")
		body = append(body, errStyle.Render("  "+errMsg))
		body = append(body, "")
		body = append(body, "  "+keyHint("enter", "skip")+"   "+keyHint("c", "skip all")+"   "+keyHint("esc", "abort"))

	case stateAskOverwrite:
		p := ""
		exist := int64(0)
		newSz := int64(0)
		if m.overwrite != nil {
			p = m.overwrite.path
			exist = m.overwrite.existingSize
			newSz = m.overwrite.newSize
		}
		wrapStyle := lipgloss.NewStyle().Width(bodyWidth)
		body = append(body, styleKeyHint.Render("  Destination already exists:"))
		body = append(body, wrapStyle.Foreground(colorWarning).Render("  "+p))
		body = append(body, "")
		body = append(body, styleKeyHint.Render(fmt.Sprintf("  existing: %s   new: %s",
			formatSize(exist), formatSize(newSz))))
		body = append(body, "")
		hints := "  " + keyHint("o/↵", "overwrite") + "   " + keyHint("O", "overwrite all")
		if resumable(m.overwrite) {
			hints += "   " + keyHint("r", "resume")
		}
		hints += "   " + keyHint("s", "skip") + "   " + keyHint("S", "skip all") + "   " + keyHint("esc", "abort")
		body = append(body, hints)

	case stateDownloading, stateDone:
		if m.scanning && m.state == stateDownloading {
			body = append(body, styleKeyHint.Render("  Scanning:"))
			body = append(body, stylePath.Render("  "+m.scanFile))
			if m.filesTotal > 0 {
				body = append(body, styleKeyHint.Render(fmt.Sprintf("  %d files found", m.filesTotal)))
			}
		} else {
			var pct float64
			if m.total > 0 {
				pct = float64(m.written) / float64(m.total)
			}
			if m.state == stateDone {
				pct = 1.0
			}
			elapsed := time.Since(m.startTime)
			speed := ""
			if elapsed.Seconds() > 0 && m.written > 0 {
				bps := float64(m.written) / elapsed.Seconds()
				speed = "  " + formatSize(int64(bps)) + "/s"
			}

			body = append(body, "  "+m.bar.ViewAs(pct), "")

			var statusLine string
			if m.state == stateDone {
				doneMsg := "  Done! Saved to: " + m.localDir
				if len(m.skipped) > 0 {
					doneMsg += fmt.Sprintf("  (skipped %d)", len(m.skipped))
				}
				statusLine = styleStatusOk.Render(doneMsg) +
					"\n\n" + styleKeyHint.Render("  Press enter or q to go back")
			} else {
				written := formatSize(m.written)
				total := ""
				if m.total > 0 {
					total = " / " + formatSize(m.total)
				}
				statusLine = styleProgress.Render(fmt.Sprintf("  %s%s%s  %.0f%%", written, total, speed, pct*100))
				if m.filesTotal > 0 {
					statusLine += "\n" + styleKeyHint.Render(fmt.Sprintf("  %d/%d files", m.filesDone, m.filesTotal))
				}
				if m.currentFile != "" {
					statusLine += "\n" + styleKeyHint.Render("  Downloading: "+m.currentFile)
				}
				if len(m.skipped) > 0 {
					note := fmt.Sprintf("  %d skipped", len(m.skipped))
					if m.skipAllOn {
						note += " (skipping all errors)"
					}
					statusLine += "\n" + styleKeyHint.Render(note)
				}
			}
			body = append(body, statusLine)
		}
		body = append(body, "")
		body = append(body, "  "+styleKeyHint.Render("Destination: ")+stylePath.Render(m.localDir))
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		styleLogo.Render("\n"+heading),
		stylePanel.Render(lipgloss.JoinVertical(lipgloss.Left, body...)),
	)

	if m.width > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}

func getLocalDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
