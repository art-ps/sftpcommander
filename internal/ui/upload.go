package ui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type uploadState int

const (
	uploadEditSrc uploadState = iota
	uploadInProgress
	uploadAskContinue
	uploadAskOverwrite
	uploadDone
)

type uploadFailureInfo struct {
	path string
	err  error
}

type uploadEvent struct {
	progress        *sftpclient.UploadProgress
	failure         *uploadFailureInfo
	silentSkip      *uploadFailureInfo
	overwrite       *overwriteAsk
	silentOverwrite *overwriteEvent
	done            bool
	finalErr        error
}

type uploadEventMsg uploadEvent

type backFromUploadMsg struct{}

type UploadModel struct {
	client     *sftpclient.Client
	remoteDir  string
	srcInput   textinput.Model
	state      uploadState
	bar        progress.Model
	written    int64
	total      int64
	err        string
	failure    *uploadFailureInfo
	skipped    []string
	skipAllOn  bool
	startTime  time.Time
	width      int
	height     int
	eventCh    chan uploadEvent
	decisionCh chan userChoice
	currentSrc string
	scanning       bool
	scanFile       string
	filesDone      int64
	filesTotal     int64
	overwrite      *overwriteAsk
	overwriteAllOn bool
	verify         bool

	// presetSources, when non-empty, skips the source-input step entirely.
	// Used by two-pane copy: the source list is already known (selected items
	// in the active panel), the user just confirms.
	presetSources []string
}

func NewUploadModel(client *sftpclient.Client, remoteDir string) UploadModel {
	bar := progress.New(progress.WithDefaultGradient(), progress.WithWidth(60))
	in := textinput.New()
	if home, err := os.UserHomeDir(); err == nil {
		in.SetValue(home + string(filepath.Separator))
	}
	in.CharLimit = 1024
	in.Width = 60
	in.Prompt = ""
	in.Focus()
	return UploadModel{
		client:    client,
		remoteDir: remoteDir,
		srcInput:  in,
		bar:       bar,
		state:     uploadEditSrc,
	}
}

// NewUploadModelMulti starts an upload of pre-known source paths without
// prompting the user. Used for the two-pane F5 copy where the source list
// is the active panel's selection — the view opens straight into progress.
func NewUploadModelMulti(client *sftpclient.Client, remoteDir string, sources []string) UploadModel {
	m := NewUploadModel(client, remoteDir)
	m.presetSources = sources
	m.state = uploadInProgress
	m.startTime = time.Now()
	m.eventCh = make(chan uploadEvent, 64)
	m.decisionCh = make(chan userChoice, 1)
	return m
}

func (m UploadModel) Init() tea.Cmd {
	if len(m.presetSources) > 0 {
		return tea.Batch(m.runUploads(m.presetSources), m.waitForEvent())
	}
	return textinput.Blink
}

func (m *UploadModel) start() tea.Cmd {
	var sources []string
	if len(m.presetSources) > 0 {
		sources = m.presetSources
	} else {
		raw := strings.TrimSpace(m.srcInput.Value())
		if raw == "" {
			m.err = "source path cannot be empty"
			return nil
		}
		if strings.HasPrefix(raw, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				raw = filepath.Join(home, raw[2:])
			}
		}
		sources = []string{raw}
	}

	// Validate sources up-front so the user sees errors before transfer starts.
	for _, s := range sources {
		if _, err := os.Stat(s); err != nil {
			m.err = "cannot read " + s + ": " + err.Error()
			return nil
		}
	}

	m.err = ""
	m.failure = nil
	m.skipped = nil
	m.skipAllOn = false
	m.state = uploadInProgress
	m.startTime = time.Now()
	m.written = 0
	m.total = 0
	m.eventCh = make(chan uploadEvent, 64)
	m.decisionCh = make(chan userChoice, 1)
	return tea.Batch(m.runUploads(sources), m.waitForEvent())
}

func (m UploadModel) runUploads(sources []string) tea.Cmd {
	client := m.client
	remoteDir := m.remoteDir
	verify := m.verify
	return func() tea.Msg {
		aborted := false
		skipAll := false
		overwriteAll := false
		skipAllOverwrite := false
		onOverwrite := func(p string, existingSize, newSize int64) sftpclient.OverwriteDecision {
			if aborted {
				return sftpclient.OverwriteAbort
			}
			if overwriteAll {
				return sftpclient.OverwriteReplace
			}
			if skipAllOverwrite {
				select {
				case m.eventCh <- uploadEvent{silentOverwrite: &overwriteEvent{silent: &overwriteAsk{path: p, existingSize: existingSize, newSize: newSize}}}:
				default:
				}
				return sftpclient.OverwriteSkip
			}
			m.eventCh <- uploadEvent{overwrite: &overwriteAsk{path: p, existingSize: existingSize, newSize: newSize}}
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
		onFailure := func(p string, err error) sftpclient.FailureDecision {
			if aborted {
				return sftpclient.DecisionAbort
			}
			if skipAll {
				select {
				case m.eventCh <- uploadEvent{silentSkip: &uploadFailureInfo{path: p, err: err}}:
				default:
				}
				return sftpclient.DecisionSkip
			}
			m.eventCh <- uploadEvent{failure: &uploadFailureInfo{path: p, err: err}}
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
		progressCb := func(p sftpclient.UploadProgress) {
			if p.ScanFile == "" && p.File != "" {
				p.File = filepath.Base(p.File)
			}
			select {
			case m.eventCh <- uploadEvent{progress: &p}:
			default:
			}
		}

		items := make([]sftpclient.UploadItem, 0, len(sources))
		for _, src := range sources {
			info, err := os.Stat(src)
			if err != nil {
				if onFailure(src, err) == sftpclient.DecisionAbort {
					m.eventCh <- uploadEvent{done: true, finalErr: err}
					return nil
				}
				continue
			}
			base := filepath.Base(src)
			items = append(items, sftpclient.UploadItem{
				LocalPath:  src,
				RemotePath: path.Join(remoteDir, base),
				IsDir:      info.IsDir(),
			})
		}

		opts := sftpclient.BatchOptions{Parallel: 4, OnOverwrite: onOverwrite, Verify: verify}
		if err := client.UploadBatch(items, opts, progressCb, onFailure); err != nil {
			m.eventCh <- uploadEvent{done: true, finalErr: err}
			return nil
		}
		m.eventCh <- uploadEvent{done: true}
		return nil
	}
}

func (m UploadModel) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.eventCh
		if !ok {
			return nil
		}
		return uploadEventMsg(ev)
	}
}

func (m UploadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.bar.Width = min(m.width-20, 80)
		m.srcInput.Width = min(m.width-20, 80)

	case uploadEventMsg:
		ev := uploadEvent(msg)
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
				m.currentSrc = ev.progress.File
				m.filesDone = ev.progress.FilesDone
				m.filesTotal = ev.progress.FilesTotal
			}
			return m, m.waitForEvent()
		case ev.failure != nil:
			m.failure = ev.failure
			m.state = uploadAskContinue
			return m, nil
		case ev.overwrite != nil:
			m.overwrite = ev.overwrite
			m.state = uploadAskOverwrite
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
				m.state = uploadEditSrc
				m.srcInput.Focus()
				return m, textinput.Blink
			}
			m.state = uploadDone
			return m, nil
		}

	case tea.KeyMsg:
		switch m.state {
		case uploadAskOverwrite:
			switch msg.String() {
			case "o", "enter":
				m.decisionCh <- choiceOverwrite
				m.overwrite = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "O":
				m.overwriteAllOn = true
				m.decisionCh <- choiceOverwriteAll
				m.overwrite = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "r":
				if m.overwrite == nil || !resumable(m.overwrite) {
					return m, nil
				}
				m.decisionCh <- choiceResume
				m.overwrite = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "s", "y":
				if m.overwrite != nil {
					m.skipped = append(m.skipped, m.overwrite.path)
				}
				m.decisionCh <- choiceSkip
				m.overwrite = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "S", "A":
				if m.overwrite != nil {
					m.skipped = append(m.skipped, m.overwrite.path)
				}
				m.skipAllOn = true
				m.decisionCh <- choiceSkipAll
				m.overwrite = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "esc", "ctrl+c", "a":
				m.decisionCh <- choiceAbort
				m.overwrite = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			}
			return m, nil

		case uploadEditSrc:
			switch msg.String() {
			case "esc", "ctrl+c":
				return m, func() tea.Msg { return backFromUploadMsg{} }
			case "enter":
				return m, m.start()
			case "ctrl+v":
				m.verify = !m.verify
				return m, nil
			}
			var cmd tea.Cmd
			m.srcInput, cmd = m.srcInput.Update(msg)
			return m, cmd

		case uploadAskContinue:
			switch msg.String() {
			case "enter", "y", "s":
				if m.failure != nil {
					m.skipped = append(m.skipped, m.failure.path)
				}
				m.decisionCh <- choiceSkip
				m.failure = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "c", "A":
				if m.failure != nil {
					m.skipped = append(m.skipped, m.failure.path)
				}
				m.skipAllOn = true
				m.decisionCh <- choiceSkipAll
				m.failure = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			case "esc", "ctrl+c", "n", "a":
				m.decisionCh <- choiceAbort
				m.failure = nil
				m.state = uploadInProgress
				return m, m.waitForEvent()
			}

		case uploadInProgress:
			switch msg.String() {
			case "ctrl+c", "q":
				select {
				case m.decisionCh <- choiceAbort:
				default:
				}
				return m, func() tea.Msg { return backFromUploadMsg{} }
			}

		case uploadDone:
			switch msg.String() {
			case "ctrl+c", "q", "enter", "esc":
				return m, func() tea.Msg { return backFromUploadMsg{} }
			}
		}
	}
	var cmd tea.Cmd
	var bm tea.Model
	bm, cmd = m.bar.Update(msg)
	m.bar = bm.(progress.Model)
	return m, cmd
}

func (m UploadModel) View() string {
	bodyWidth := max(40, min(m.width-10, 90))

	var heading string
	switch m.state {
	case uploadEditSrc:
		heading = "  Choose source"
	case uploadInProgress:
		if m.scanning {
			heading = "  Scanning..."
		} else {
			heading = "  Uploading"
		}
	case uploadAskContinue:
		heading = "  File error"
	case uploadAskOverwrite:
		heading = "  File exists"
	case uploadDone:
		heading = "  Upload complete"
	}

	body := []string{
		styleTitle.Render("  → " + m.remoteDir + "  "),
		"",
	}
	switch m.state {
	case uploadEditSrc:
		inputStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1)
		body = append(body, styleKeyHint.Render("  Local file or directory:"))
		body = append(body, "  "+inputStyle.Render(m.srcInput.View()))
		if m.err != "" {
			body = append(body, "", lipgloss.NewStyle().Foreground(colorError).Bold(true).Width(bodyWidth).Render("  Error: "+m.err))
		}
		verifyTag := "off"
		if m.verify {
			verifyTag = styleStatusOk.Render("on")
		}
		body = append(body, "", styleKeyHint.Render("  SHA256 verify: "+verifyTag))
		body = append(body, "", "  "+keyHint("enter", "start")+"   "+keyHint("^v", "toggle verify")+"   "+keyHint("esc", "cancel"))
	case uploadAskContinue:
		p := ""
		errMsg := ""
		if m.failure != nil {
			p = m.failure.path
			errMsg = m.failure.err.Error()
		}
		wrap := lipgloss.NewStyle().Width(bodyWidth)
		errStyle := lipgloss.NewStyle().Foreground(colorError).Bold(true).Width(bodyWidth)
		body = append(body, styleKeyHint.Render("  Failed to upload:"))
		body = append(body, wrap.Foreground(colorWarning).Render("  "+p))
		body = append(body, "", errStyle.Render("  "+errMsg))
		body = append(body, "", "  "+keyHint("enter", "skip")+"   "+keyHint("c", "skip all")+"   "+keyHint("esc", "abort"))
	case uploadAskOverwrite:
		p := ""
		exist := int64(0)
		newSz := int64(0)
		if m.overwrite != nil {
			p = m.overwrite.path
			exist = m.overwrite.existingSize
			newSz = m.overwrite.newSize
		}
		wrap := lipgloss.NewStyle().Width(bodyWidth)
		body = append(body, styleKeyHint.Render("  Remote file already exists:"))
		body = append(body, wrap.Foreground(colorWarning).Render("  "+p))
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
	case uploadInProgress, uploadDone:
		if m.scanning && m.state == uploadInProgress {
			body = append(body, styleKeyHint.Render("  Scanning:"))
			body = append(body, stylePath.Render("  "+m.scanFile))
			if m.filesTotal > 0 {
				body = append(body, styleKeyHint.Render(fmt.Sprintf("  %d files found", m.filesTotal)))
			}
			break
		}
		var pct float64
		if m.total > 0 {
			pct = float64(m.written) / float64(m.total)
		}
		if m.state == uploadDone {
			pct = 1.0
		}
		elapsed := time.Since(m.startTime)
		speed := ""
		if elapsed.Seconds() > 0 && m.written > 0 {
			bps := float64(m.written) / elapsed.Seconds()
			speed = "  " + formatSize(int64(bps)) + "/s"
		}
		body = append(body, "  "+m.bar.ViewAs(pct), "")
		if m.state == uploadDone {
			done := "  Done — uploaded to " + m.remoteDir
			if len(m.skipped) > 0 {
				done += fmt.Sprintf("  (skipped %d)", len(m.skipped))
			}
			body = append(body, styleStatusOk.Render(done), "", styleKeyHint.Render("  Press enter or q to go back"))
		} else {
			body = append(body, styleProgress.Render(fmt.Sprintf("  %s / %s%s  %.0f%%",
				formatSize(m.written), formatSize(m.total), speed, pct*100)))
			if m.filesTotal > 0 {
				body = append(body, styleKeyHint.Render(fmt.Sprintf("  %d/%d files", m.filesDone, m.filesTotal)))
			}
			if m.currentSrc != "" {
				body = append(body, styleKeyHint.Render("  Uploading: "+m.currentSrc))
			}
			if len(m.skipped) > 0 {
				note := fmt.Sprintf("  %d skipped", len(m.skipped))
				if m.skipAllOn {
					note += " (skipping all errors)"
				}
				body = append(body, styleKeyHint.Render(note))
			}
		}
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
