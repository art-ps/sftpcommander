package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/art-ps/sftpcommander/internal/config"
	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	tea "github.com/charmbracelet/bubbletea"
)

type view int

const (
	viewConnList view = iota
	viewConnect
	viewBrowser
	viewTwoPane
	viewDownload
	viewUpload
	viewPreview
	viewBookmarks
	viewHostPrompt
	viewPassphrase
	viewEdit
	viewErrLog
)

type connectedMsg struct {
	client   *sftpclient.Client
	connInfo string
	err      error
}

// openConnListMsg is emitted by ConnectModel when the user presses esc to
// abandon the form and go back to the saved-connections list.
type openConnListMsg struct{}

type App struct {
	current     view
	connList    ConnListModel
	connect     ConnectModel
	browser     BrowserModel
	twoPane     TwoPaneModel
	download    DownloadModel
	upload      UploadModel
	preview     PreviewModel
	bookmarks   BookmarksModel
	hostPrompt  HostPromptModel
	passphrase  PassphraseModel
	edit        EditModel
	errLogView  ErrLogModel
	errLog      []errLogEntry
	pendingConn *ConnectedMsg
	width       int
	height      int

	// client is the active SFTP connection. Owned by App so Download/Upload/
	// TwoPane can build their models against a single source of truth.
	client *sftpclient.Client

	// prevView remembers where Download/Upload was launched from so we can
	// hop back to the browser or two-pane view on completion.
	prevView view

	lastDownloadDir string

	// Last successful connection — used by bookmarks scoping.
	lastHost string
	lastPort string
	lastUser string
}

func NewApp() App {
	return App{
		current:  viewConnList,
		connList: NewConnListModel(),
		connect:  NewConnectModel(),
	}
}

func (a App) Init() tea.Cmd { return a.connList.Init() }

func (a *App) pushErr(src, msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	a.errLog = append(a.errLog, errLogEntry{ts: time.Now(), src: src, msg: msg})
	if len(a.errLog) > errLogCap {
		a.errLog = a.errLog[len(a.errLog)-errLogCap:]
	}
}

func connectCmd(msg ConnectedMsg) tea.Cmd {
	return func() tea.Msg {
		client, err := sftpclient.ConnectWithProxy(msg.Host, msg.Port, msg.User, msg.Password, msg.KeyPath, msg.KeyPassphrase, msg.ProxyJump)
		connInfo := fmt.Sprintf("%s@%s:%s", msg.User, msg.Host, msg.Port)
		if msg.ProxyJump != "" {
			connInfo += " via " + msg.ProxyJump
		}
		return connectedMsg{client: client, connInfo: connInfo, err: err}
	}
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Observe known error-bearing messages BEFORE dispatch so they land in the
	// error ring even when handled by a sub-model below.
	switch m := msg.(type) {
	case opDoneMsg:
		if m.err != nil {
			a.pushErr("op:"+m.op, m.err.Error())
		}
	case entriesLoadedMsg:
		if m.err != nil {
			a.pushErr("list", m.path+": "+m.err.Error())
		}
	case findResultsMsg:
		if m.err != nil {
			a.pushErr("find", m.err.Error())
		}
	}

	switch msg := msg.(type) {
	case errReportMsg:
		a.pushErr(msg.src, msg.msg)
		return a, nil
	case openErrLogMsg:
		a.errLogView = NewErrLogModel(a.errLog)
		a.prevView = a.current
		a.current = viewErrLog
		a.errLogView, _ = updateAs(a.errLogView, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, nil
	case backFromErrLogMsg:
		a.current = a.returnView()
		return a, nil
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		// Propagate to every sub-model so a switched-to view already knows the
		// terminal size. We dispatch one Update per view but discard the cmd
		// returned by inactive ones (they shouldn't need to do work on resize).
		var cmd tea.Cmd
		a.connList, cmd = updateAs[ConnListModel](a.connList, msg)
		a.connect, _ = updateAs[ConnectModel](a.connect, msg)
		a.browser, _ = updateAs[BrowserModel](a.browser, msg)
		a.twoPane, _ = updateAs[TwoPaneModel](a.twoPane, msg)
		a.download, _ = updateAs[DownloadModel](a.download, msg)
		a.upload, _ = updateAs[UploadModel](a.upload, msg)
		a.preview, _ = updateAs[PreviewModel](a.preview, msg)
		a.bookmarks, _ = updateAs[BookmarksModel](a.bookmarks, msg)
		a.hostPrompt, _ = updateAs[HostPromptModel](a.hostPrompt, msg)
		a.passphrase, _ = updateAs[PassphraseModel](a.passphrase, msg)
		a.edit, _ = updateAs[EditModel](a.edit, msg)
		a.errLogView, _ = updateAs[ErrLogModel](a.errLogView, msg)
		return a, cmd

	case openConnectFormMsg:
		if msg.prefill != nil {
			a.connect.Prefill(*msg.prefill)
		} else {
			a.connect = NewConnectModel()
		}
		a.current = viewConnect
		return a, a.connect.Init()

	case openConnListMsg:
		a.connList = NewConnListModel()
		a.current = viewConnList
		return a, nil

	case deleteSavedConnMsg:
		_ = config.DeleteConnection(msg.name)
		a.connList = NewConnListModel()
		return a, nil

	case ConnectedMsg:
		conn := msg
		a.pendingConn = &conn
		return a, connectCmd(conn)

	case connectedMsg:
		if msg.err != nil {
			var unk *sftpclient.UnknownHostKeyError
			if errors.As(msg.err, &unk) {
				a.hostPrompt = NewHostPromptModel(unk)
				a.hostPrompt, _ = updateAs[HostPromptModel](a.hostPrompt, tea.WindowSizeMsg{Width: a.width, Height: a.height})
				a.current = viewHostPrompt
				return a, nil
			}
			var chg *sftpclient.ChangedHostKeyError
			if errors.As(msg.err, &chg) {
				a.connect.err = "⚠ HOST KEY CHANGED for " + chg.Hostname +
					" (" + chg.Fingerprint + ") — possible MITM. " +
					"Remove the old key from ~/.ssh/known_hosts to proceed."
				a.pendingConn = nil
				a.current = viewConnect
				return a, nil
			}
			var pp *sftpclient.PassphraseRequiredError
			if errors.As(msg.err, &pp) {
				a.passphrase = NewPassphraseModel(pp.KeyPath, pp.BadPassphrase)
				a.passphrase, _ = updateAs[PassphraseModel](a.passphrase, tea.WindowSizeMsg{Width: a.width, Height: a.height})
				a.current = viewPassphrase
				return a, a.passphrase.Init()
			}
			a.connect.err = "Connection failed: " + msg.err.Error()
			a.pendingConn = nil
			a.current = viewConnect
			return a, nil
		}
		if a.pendingConn != nil {
			a.lastHost = a.pendingConn.Host
			a.lastPort = a.pendingConn.Port
			a.lastUser = a.pendingConn.User
			// Auto-save successful connection (deduped by name in store).
			_ = config.AddConnection(config.Connection{
				Name:    fmt.Sprintf("%s@%s", a.pendingConn.User, a.pendingConn.Host),
				Host:    a.pendingConn.Host,
				Port:    a.pendingConn.Port,
				User:    a.pendingConn.User,
				KeyPath: a.pendingConn.KeyPath,
			})
		}
		a.pendingConn = nil
		a.client = msg.client
		remoteFS := NewCachedFS(NewRemoteFS(msg.client, msg.connInfo))
		a.browser = NewBrowserModel(remoteFS)
		a.twoPane = NewTwoPaneModel(msg.client, NewCachedFS(NewLocalFS()), remoteFS)
		a.current = viewTwoPane
		// Re-send window size so both views compute their layout.
		a.browser, _ = updateAs[BrowserModel](a.browser, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		a.twoPane, _ = updateAs[TwoPaneModel](a.twoPane, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, tea.Batch(a.browser.Init(), a.twoPane.Init())

	case passphraseEnteredMsg:
		if a.pendingConn == nil {
			a.current = viewConnect
			return a, nil
		}
		a.pendingConn.KeyPassphrase = msg.passphrase
		return a, connectCmd(*a.pendingConn)

	case passphraseCanceledMsg:
		a.connect.err = "Passphrase entry cancelled"
		a.pendingConn = nil
		a.current = viewConnect
		return a, nil

	case hostKeyDecisionMsg:
		if !msg.accept {
			a.connect.err = "Host key rejected"
			a.pendingConn = nil
			a.current = viewConnect
			return a, nil
		}
		if a.hostPrompt.challenge != nil {
			if err := sftpclient.AppendKnownHost(a.hostPrompt.challenge); err != nil {
				a.connect.err = "Failed to save host key: " + err.Error()
				a.pendingConn = nil
				a.current = viewConnect
				return a, nil
			}
		}
		if a.pendingConn == nil {
			a.current = viewConnect
			return a, nil
		}
		return a, connectCmd(*a.pendingConn)

	case downloadStartMsg:
		localDir := msg.localDir
		if localDir == "" {
			localDir = a.lastDownloadDir
		}
		if localDir == "" {
			localDir = getLocalDownloadDir()
		}
		a.download = NewDownloadModel(a.client, msg.entries, localDir)
		a.prevView = a.current
		a.current = viewDownload
		a.download, _ = updateAs[DownloadModel](a.download, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, a.download.Init()

	case uploadStartMsg:
		if len(msg.sources) > 0 {
			a.upload = NewUploadModelMulti(a.client, msg.remoteDir, msg.sources)
		} else {
			a.upload = NewUploadModel(a.client, msg.remoteDir)
		}
		a.prevView = a.current
		a.current = viewUpload
		a.upload, _ = updateAs[UploadModel](a.upload, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, a.upload.Init()

	case previewStartMsg:
		fs := msg.fs
		if fs == nil {
			fs = a.browser.fs
		}
		a.preview = NewPreviewModel(fs, msg.entry)
		a.prevView = a.current
		a.current = viewPreview
		a.preview, _ = updateAs[PreviewModel](a.preview, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, a.preview.Init()

	case editStartMsg:
		fs := msg.fs
		if fs == nil {
			fs = a.browser.fs
		}
		a.edit = NewEditModel(fs, msg.entry)
		a.prevView = a.current
		a.current = viewEdit
		a.edit, _ = updateAs[EditModel](a.edit, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, a.edit.Init()

	case backFromEditMsg:
		a.current = a.returnView()
		if a.current == viewTwoPane {
			return a, func() tea.Msg { return refreshTwoPaneMsg{} }
		}
		return a, a.browser.Refresh()

	case openTwoPaneMsg:
		a.twoPane = NewTwoPaneModel(a.client, NewCachedFS(NewLocalFS()), NewCachedFS(NewRemoteFS(a.client, a.browser.fs.Label())))
		a.current = viewTwoPane
		a.twoPane, _ = updateAs[TwoPaneModel](a.twoPane, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, a.twoPane.Init()

	case backToSinglePaneMsg:
		a.current = viewBrowser
		return a, nil

	case openBookmarksMsg:
		a.bookmarks = NewBookmarksModel(a.lastHost, a.lastPort, a.lastUser)
		a.current = viewBookmarks
		a.bookmarks, _ = updateAs[BookmarksModel](a.bookmarks, tea.WindowSizeMsg{Width: a.width, Height: a.height})
		return a, nil

	case addBookmarkMsg:
		_ = config.AddBookmark(config.Bookmark{
			Host: a.lastHost,
			Port: a.lastPort,
			User: a.lastUser,
			Path: msg.path,
		})
		return a, nil

	case bookmarkSelectedMsg:
		a.current = viewBrowser
		return a, a.browser.LoadPath(msg.path)

	case backFromBookmarksMsg, backFromPreviewMsg:
		a.current = a.returnView()
		return a, nil

	case backFromUploadMsg:
		a.current = a.returnView()
		if a.current == viewTwoPane {
			return a, func() tea.Msg { return refreshTwoPaneMsg{} }
		}
		return a, a.browser.Refresh()

	case backToBrowserMsg:
		if a.download.localDir != "" {
			a.lastDownloadDir = a.download.localDir
		}
		a.current = a.returnView()
		if a.current == viewTwoPane {
			return a, func() tea.Msg { return refreshTwoPaneMsg{} }
		}
		return a, nil
	}

	switch a.current {
	case viewConnList:
		var cmd tea.Cmd
		a.connList, cmd = updateAs[ConnListModel](a.connList, msg)
		return a, cmd
	case viewConnect:
		var cmd tea.Cmd
		a.connect, cmd = updateAs[ConnectModel](a.connect, msg)
		return a, cmd
	case viewBrowser:
		var cmd tea.Cmd
		a.browser, cmd = updateAs[BrowserModel](a.browser, msg)
		return a, cmd
	case viewTwoPane:
		var cmd tea.Cmd
		a.twoPane, cmd = updateAs[TwoPaneModel](a.twoPane, msg)
		return a, cmd
	case viewDownload:
		var cmd tea.Cmd
		a.download, cmd = updateAs[DownloadModel](a.download, msg)
		return a, cmd
	case viewUpload:
		var cmd tea.Cmd
		a.upload, cmd = updateAs[UploadModel](a.upload, msg)
		return a, cmd
	case viewPreview:
		var cmd tea.Cmd
		a.preview, cmd = updateAs[PreviewModel](a.preview, msg)
		return a, cmd
	case viewBookmarks:
		var cmd tea.Cmd
		a.bookmarks, cmd = updateAs[BookmarksModel](a.bookmarks, msg)
		return a, cmd
	case viewHostPrompt:
		var cmd tea.Cmd
		a.hostPrompt, cmd = updateAs[HostPromptModel](a.hostPrompt, msg)
		return a, cmd
	case viewPassphrase:
		var cmd tea.Cmd
		a.passphrase, cmd = updateAs[PassphraseModel](a.passphrase, msg)
		return a, cmd
	case viewEdit:
		var cmd tea.Cmd
		a.edit, cmd = updateAs[EditModel](a.edit, msg)
		return a, cmd
	case viewErrLog:
		var cmd tea.Cmd
		a.errLogView, cmd = updateAs[ErrLogModel](a.errLogView, msg)
		return a, cmd
	}
	return a, nil
}

func (a App) View() string {
	switch a.current {
	case viewConnList:
		return a.connList.View()
	case viewConnect:
		return a.connect.View()
	case viewBrowser:
		return a.browser.View()
	case viewTwoPane:
		return a.twoPane.View()
	case viewDownload:
		return a.download.View()
	case viewUpload:
		return a.upload.View()
	case viewPreview:
		return a.preview.View()
	case viewBookmarks:
		return a.bookmarks.View()
	case viewHostPrompt:
		return a.hostPrompt.View()
	case viewPassphrase:
		return a.passphrase.View()
	case viewEdit:
		return a.edit.View()
	case viewErrLog:
		return a.errLogView.View()
	}
	return ""
}

// returnView is the destination after Download/Upload/Preview/Bookmarks
// completes. Defaults to the single-pane browser; if the user was in
// two-pane mode when the modal was triggered, hops back there instead.
func (a App) returnView() view {
	if a.prevView == viewTwoPane {
		return viewTwoPane
	}
	return viewBrowser
}

// updateAs is a small generic adapter that calls (tea.Model).Update and
// re-asserts back to the concrete model type. It exists to centralize the
// boilerplate `m, cmd := sub.Update(msg); sub = m.(SubModel); return cmd`.
func updateAs[T tea.Model](m T, msg tea.Msg) (T, tea.Cmd) {
	updated, cmd := m.Update(msg)
	return updated.(T), cmd
}
