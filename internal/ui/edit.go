package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type backFromEditMsg struct{}

type editState int

const (
	editStateDownloading editState = iota
	editStateEditing
	editStateConflict
	editStateUploading
	editStateDone
	editStateError
)

type editDownloadedMsg struct{ err error }
type editFinishedMsg struct{ err error }
type editUploadedMsg struct{ err error }

type EditModel struct {
	fs    FileSystem
	entry sftpclient.FileEntry

	tempPath string

	// origRemote* captured before the editor runs so we can detect a remote
	// change between download and upload (concurrent writer on the server).
	origRemoteSize  int64
	origRemoteMTime time.Time

	// preEdit* are the temp file's stat right after download. Compared to the
	// post-editor stat to skip the upload when the user opened+quit without
	// saving.
	preEditSize  int64
	preEditMTime time.Time

	state  editState
	err    string
	status string
	width  int
	height int

	// remoteChanged captures the current server-side size/mtime when an
	// upload-time conflict is detected, so the prompt can show what changed.
	remoteChanged sftpclient.FileEntry
}

func NewEditModel(fs FileSystem, entry sftpclient.FileEntry) EditModel {
	return EditModel{
		fs:    fs,
		entry: entry,
		state: editStateDownloading,
	}
}

func (m EditModel) Init() tea.Cmd {
	return m.downloadTemp()
}

func (m *EditModel) downloadTemp() tea.Cmd {
	fs := m.fs
	entry := m.entry
	tmpDir, err := os.MkdirTemp("", "sftp-edit-")
	if err != nil {
		m.err = err.Error()
		m.state = editStateError
		return nil
	}
	m.tempPath = filepath.Join(tmpDir, filepath.Base(entry.Path))
	tempPath := m.tempPath
	return func() tea.Msg {
		rfs, ok := unwrapFS(fs).(*RemoteFS)
		if !ok {
			return editDownloadedMsg{err: fmt.Errorf("edit only works on remote fs")}
		}
		if err := rfs.Client().DownloadFile(entry.Path, tempPath, nil); err != nil {
			return editDownloadedMsg{err: err}
		}
		return editDownloadedMsg{}
	}
}

func runEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg { return editFinishedMsg{err: err} })
}

func (m EditModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case editDownloadedMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.state = editStateError
			return m, nil
		}
		// Record original remote stat and temp file stat for later
		// change/conflict detection.
		m.origRemoteSize = m.entry.Size
		m.origRemoteMTime = m.entry.ModTime
		if info, err := os.Stat(m.tempPath); err == nil {
			m.preEditSize = info.Size()
			m.preEditMTime = info.ModTime()
		}
		m.state = editStateEditing
		return m, runEditor(m.tempPath)

	case editFinishedMsg:
		if msg.err != nil {
			m.cleanup()
			m.err = "editor: " + msg.err.Error()
			m.state = editStateError
			return m, nil
		}
		// If the user didn't save, skip upload.
		info, err := os.Stat(m.tempPath)
		if err != nil {
			m.cleanup()
			m.err = "stat temp: " + err.Error()
			m.state = editStateError
			return m, nil
		}
		if info.Size() == m.preEditSize && info.ModTime().Equal(m.preEditMTime) {
			m.status = "no changes"
			m.cleanup()
			m.state = editStateDone
			return m, nil
		}
		// Detect remote-side change.
		remote, statErr := m.fs.Stat(m.entry.Path)
		if statErr == nil && (remote.Size != m.origRemoteSize || !remote.ModTime.Equal(m.origRemoteMTime)) {
			m.remoteChanged = remote
			m.state = editStateConflict
			return m, nil
		}
		m.state = editStateUploading
		return m, m.uploadCmd()

	case editUploadedMsg:
		m.cleanup()
		if msg.err != nil {
			m.err = msg.err.Error()
			m.state = editStateError
			return m, nil
		}
		m.status = "uploaded"
		m.state = editStateDone
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case editStateConflict:
			switch msg.String() {
			case "o", "enter":
				m.state = editStateUploading
				return m, m.uploadCmd()
			case "k":
				// Keep local: dump temp file beside cwd for the user to inspect.
				dest := filepath.Join(os.TempDir(), "edit-keep-"+filepath.Base(m.entry.Path))
				_ = copyFileContents(m.tempPath, dest)
				m.status = "saved to " + dest
				m.cleanup()
				m.state = editStateDone
				return m, nil
			case "esc", "ctrl+c", "a":
				m.cleanup()
				return m, func() tea.Msg { return backFromEditMsg{} }
			}
		case editStateDone, editStateError:
			switch msg.String() {
			case "enter", "esc", "q", "ctrl+c":
				return m, func() tea.Msg { return backFromEditMsg{} }
			}
		}
	}
	return m, nil
}

func (m EditModel) uploadCmd() tea.Cmd {
	fs := m.fs
	entry := m.entry
	tempPath := m.tempPath
	return func() tea.Msg {
		rfs, ok := unwrapFS(fs).(*RemoteFS)
		if !ok {
			return editUploadedMsg{err: fmt.Errorf("edit only works on remote fs")}
		}
		if err := rfs.Client().UploadFile(tempPath, entry.Path, nil); err != nil {
			return editUploadedMsg{err: err}
		}
		return editUploadedMsg{}
	}
}

func (m EditModel) cleanup() {
	if m.tempPath == "" {
		return
	}
	_ = os.Remove(m.tempPath)
	_ = os.Remove(filepath.Dir(m.tempPath))
}

func copyFileContents(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func (m EditModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var heading, body string
	switch m.state {
	case editStateDownloading:
		heading = "  Preparing edit"
		body = styleSecondary("Downloading " + m.entry.Path + " to a temp file...")
	case editStateEditing:
		heading = "  Editor running"
		body = styleSecondary("Editing " + m.tempPath)
	case editStateConflict:
		heading = "  Conflict"
		body = lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Foreground(colorWarning).Render("Remote file changed while you were editing."),
			"",
			styleKeyHint.Render(fmt.Sprintf("  original: %s   now: %s",
				formatSize(m.origRemoteSize), formatSize(m.remoteChanged.Size))),
			"",
			"  "+keyHint("o/↵", "overwrite")+"   "+keyHint("k", "keep local copy")+"   "+keyHint("esc", "abort"),
		)
	case editStateUploading:
		heading = "  Uploading"
		body = styleSecondary("Uploading changes to " + m.entry.Path)
	case editStateDone:
		heading = "  Edit complete"
		msg := m.status
		if msg == "" {
			msg = "done"
		}
		body = lipgloss.JoinVertical(lipgloss.Left,
			styleStatusOk.Render(msg),
			"",
			styleKeyHint.Render("  Press enter or esc to go back"),
		)
	case editStateError:
		heading = "  Edit failed"
		body = lipgloss.JoinVertical(lipgloss.Left,
			styleError.Render("  "+m.err),
			"",
			styleKeyHint.Render("  Press enter or esc to go back"),
		)
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		styleLogo.Render("\n"+heading),
		stylePanel.Render(body),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}
