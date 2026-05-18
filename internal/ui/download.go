package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sftpclient "sftpbrowser/internal/sftp"

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
	stateDone
)

type failureInfo struct {
	path string
	err  error
}

type downloadEvent struct {
	progress   *sftpclient.DownloadProgress
	failure    *failureInfo
	silentSkip *failureInfo
	done       bool
	finalErr   error
}

type downloadEventMsg downloadEvent

type userChoice int

const (
	choiceSkip userChoice = iota
	choiceSkipAll
	choiceAbort
)

type DownloadModel struct {
	client     *sftpclient.Client
	entries    []sftpclient.FileEntry
	localDir   string
	destInput  textinput.Model
	state      downloadState
	bar        progress.Model
	written    int64
	total      int64
	err        string
	failure    *failureInfo
	skipped    []string
	skipAllOn  bool
	startTime  time.Time
	width      int
	height     int
	eventCh    chan downloadEvent
	decisionCh chan userChoice
	currentFile string
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
		progressCb := func(p sftpclient.DownloadProgress) {
			if p.File != "" {
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

		if err := m.client.DownloadBatch(items, sftpclient.BatchOptions{Parallel: 4}, progressCb, onFailure); err != nil {
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
	if err := os.MkdirAll(dest, 0o755); err != nil {
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
			m.written = ev.progress.Written
			m.total = ev.progress.Total
			m.currentFile = ev.progress.File
			return m, m.waitForEvent()
		case ev.failure != nil:
			m.failure = ev.failure
			m.state = stateAskContinue
			return m, nil
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
			}
			var cmd tea.Cmd
			m.destInput, cmd = m.destInput.Update(msg)
			return m, cmd

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
		heading = "  Downloading " + kind
	case stateAskContinue:
		heading = "  File error"
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
		body = append(body, "  "+keyHint("enter", "start")+"   "+keyHint("esc", "cancel"))

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

	case stateDownloading, stateDone:
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
