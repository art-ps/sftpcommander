package sftp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type FileEntry struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
}

type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

// UnknownHostKeyError is returned when the server's host key is not in
// known_hosts. The caller can show the fingerprint, ask the user, and on
// approval append it via AppendKnownHost.
type UnknownHostKeyError struct {
	Hostname    string
	Address     string
	KeyType     string
	Fingerprint string
	key         ssh.PublicKey
}

func (e *UnknownHostKeyError) Error() string {
	return fmt.Sprintf("unknown host key for %s (%s)", e.Hostname, e.Fingerprint)
}

// PassphraseRequiredError signals that the private key at KeyPath is
// encrypted and the caller must supply a passphrase. BadPassphrase is set
// when a passphrase was attempted but didn't decrypt — the UI uses this to
// switch the prompt's error message between "enter passphrase" and "wrong
// passphrase, try again".
type PassphraseRequiredError struct {
	KeyPath       string
	BadPassphrase bool
}

func (e *PassphraseRequiredError) Error() string {
	if e.BadPassphrase {
		return fmt.Sprintf("incorrect passphrase for %s", e.KeyPath)
	}
	return fmt.Sprintf("passphrase required for %s", e.KeyPath)
}

// ChangedHostKeyError is returned when the server's host key differs from the
// one stored in known_hosts — possible MITM. Never auto-accepted.
type ChangedHostKeyError struct {
	Hostname    string
	Address     string
	KeyType     string
	Fingerprint string
	// knownTypes are the key-type names (e.g. "ssh-rsa", "ssh-ed25519") we
	// already have entries for for this host. Used by Connect to retry with
	// HostKeyAlgorithms restricted to known types, which silently resolves
	// the common case where the server simply offers a newer algorithm than
	// the one we recorded.
	knownTypes []string
}

func (e *ChangedHostKeyError) Error() string {
	return fmt.Sprintf("host key CHANGED for %s (%s) — possible MITM", e.Hostname, e.Fingerprint)
}

func Connect(host, port, user, password, keyPath, keyPassphrase string) (*Client, error) {
	client, err := connectOnce(host, port, user, password, keyPath, keyPassphrase, nil)
	if err == nil {
		return client, nil
	}
	// Algorithm-mismatch recovery: if the server offered a key type we have
	// no record of, but we DO have records under other algorithms, retry
	// constraining the server to algorithms we know. A true MITM where the
	// same-algorithm key has changed still fails on the second attempt.
	var chg *ChangedHostKeyError
	if errors.As(err, &chg) && len(chg.knownTypes) > 0 &&
		!slices.Contains(chg.knownTypes, chg.KeyType) {
		return connectOnce(host, port, user, password, keyPath, keyPassphrase, chg.knownTypes)
	}
	return nil, err
}

func connectOnce(host, port, user, password, keyPath, keyPassphrase string, hostKeyAlgorithms []string) (*Client, error) {
	var authMethods []ssh.AuthMethod

	// 1) ssh-agent (if SSH_AUTH_SOCK is set and reachable).
	var agentConn net.Conn
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			agentConn = conn
			ag := agent.NewClient(conn)
			authMethods = append(authMethods, ssh.PublicKeysCallback(ag.Signers))
		}
	}
	defer func() {
		if agentConn != nil {
			agentConn.Close()
		}
	}()

	// 2) Explicit key file.
	if keyPath != "" {
		key, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key: %w", err)
		}
		var signer ssh.Signer
		if keyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(keyPassphrase))
			if err != nil {
				// Treat any post-passphrase parse failure as a wrong passphrase
				// — the parser only gets this far if it already saw an
				// encryption header, so an error here means decryption failed.
				return nil, &PassphraseRequiredError{KeyPath: keyPath, BadPassphrase: true}
			}
		} else {
			signer, err = ssh.ParsePrivateKey(key)
			if err != nil {
				var pmErr *ssh.PassphraseMissingError
				if errors.As(err, &pmErr) {
					return nil, &PassphraseRequiredError{KeyPath: keyPath}
				}
				return nil, fmt.Errorf("parse key: %w", err)
			}
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// 3) Password (last resort).
	if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}

	var unknownHost *UnknownHostKeyError
	var changedHost *ChangedHostKeyError

	hostKeyCB, err := makeHostKeyCallback(&unknownHost, &changedHost)
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User:              user,
		Auth:              authMethods,
		HostKeyCallback:   hostKeyCB,
		HostKeyAlgorithms: hostKeyAlgorithms, // nil = default negotiation
		Timeout:           15 * time.Second,
	}

	sshClient, err := ssh.Dial("tcp", host+":"+port, config)
	if err != nil {
		// Prefer typed host-key errors over the wrapped handshake error.
		if unknownHost != nil {
			return nil, unknownHost
		}
		if changedHost != nil {
			return nil, changedHost
		}
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	sftpClient, err := sftp.NewClient(sshClient,
		sftp.UseConcurrentReads(true),
		sftp.MaxConcurrentRequestsPerFile(64),
	)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}

	return &Client{ssh: sshClient, sftp: sftpClient}, nil
}

func makeHostKeyCallback(unknownOut **UnknownHostKeyError, changedOut **ChangedHostKeyError) (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir .ssh: %w", err)
	}
	khPath := filepath.Join(sshDir, "known_hosts")
	if _, statErr := os.Stat(khPath); errors.Is(statErr, os.ErrNotExist) {
		f, ferr := os.OpenFile(khPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if ferr != nil {
			return nil, fmt.Errorf("create known_hosts: %w", ferr)
		}
		f.Close()
	}

	knownCB, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts: %w", err)
	}

	return func(hostname string, remoteAddr net.Addr, key ssh.PublicKey) error {
		err := knownCB(hostname, remoteAddr, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) == 0 {
				*unknownOut = &UnknownHostKeyError{
					Hostname:    stripPort(hostname),
					Address:     remoteAddr.String(),
					KeyType:     key.Type(),
					Fingerprint: ssh.FingerprintSHA256(key),
					key:         key,
				}
				return *unknownOut
			}
			ch := &ChangedHostKeyError{
				Hostname:    stripPort(hostname),
				Address:     remoteAddr.String(),
				KeyType:     key.Type(),
				Fingerprint: ssh.FingerprintSHA256(key),
			}
			seen := map[string]bool{}
			for _, w := range ke.Want {
				if w.Key == nil {
					continue
				}
				t := w.Key.Type()
				if !seen[t] {
					seen[t] = true
					ch.knownTypes = append(ch.knownTypes, t)
				}
			}
			*changedOut = ch
			return ch
		}
		return err
	}, nil
}

func stripPort(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// AppendKnownHost adds the previously-presented host key to ~/.ssh/known_hosts.
// Call after the user confirms an UnknownHostKeyError.
func AppendKnownHost(challenge *UnknownHostKeyError) error {
	if challenge == nil || challenge.key == nil {
		return errors.New("no host key to save")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	khPath := filepath.Join(home, ".ssh", "known_hosts")

	// Extract port from Address ("ip:port") to build correct known_hosts entries.
	// knownhosts.Normalize("host:22") → "host", but
	// knownhosts.Normalize("host:20022") → "[host]:20022" — crucial for non-standard ports.
	_, port, _ := net.SplitHostPort(challenge.Address)
	withPort := func(host string) string {
		if port != "" {
			return host + ":" + port
		}
		return host
	}
	addresses := []string{knownhosts.Normalize(withPort(challenge.Hostname))}
	if challenge.Address != "" {
		ip := stripPort(challenge.Address)
		if ip != "" && ip != challenge.Hostname {
			addresses = append(addresses, knownhosts.Normalize(withPort(ip)))
		}
	}
	line := knownhosts.Line(addresses, challenge.key)

	data, _ := os.ReadFile(khPath)
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}

	f, err := os.OpenFile(khPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(prefix + line + "\n"); err != nil {
		return err
	}
	return nil
}

func (c *Client) Close() {
	c.sftp.Close()
	c.ssh.Close()
}

func (c *Client) List(remotePath string) ([]FileEntry, error) {
	infos, err := c.sftp.ReadDir(remotePath)
	if err != nil {
		return nil, err
	}

	sort.Slice(infos, func(i, j int) bool {
		if infos[i].IsDir() != infos[j].IsDir() {
			return infos[i].IsDir()
		}
		return infos[i].Name() < infos[j].Name()
	})

	entries := make([]FileEntry, 0, len(infos))
	for _, info := range infos {
		if info.Name() == "." || info.Name() == ".." {
			continue
		}
		entries = append(entries, FileEntry{
			Name:    info.Name(),
			Path:    path.Join(remotePath, info.Name()),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
		})
	}
	return entries, nil
}

func (c *Client) HomeDir() string {
	home, err := c.sftp.Getwd()
	if err != nil {
		return "/"
	}
	return home
}

type DownloadProgress struct {
	Written    int64
	Total      int64
	File       string
	ScanFile   string // non-empty during pre-scan phase
	FilesDone  int64  // completed files (download phase)
	FilesTotal int64  // total files — accurate after scan, zero during scan
}

// progressWriter wraps an io.Writer and emits throttled progress callbacks.
// It is the destination for sftp.File.WriteTo, which uses concurrent reads
// internally but writes here strictly in offset order.
type progressWriter struct {
	dst      io.Writer
	written  int64
	total    int64
	file     string
	progress func(DownloadProgress)
	last     time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.written += int64(n)
	if w.progress != nil && (err != nil || time.Since(w.last) >= 50*time.Millisecond) {
		w.last = time.Now()
		w.progress(DownloadProgress{Written: w.written, Total: w.total, File: w.file})
	}
	return n, err
}

func (c *Client) DownloadFile(remotePath, localPath string, progress func(DownloadProgress)) error {
	return downloadFileWith(c.sftp, remotePath, localPath, progress)
}

func downloadFileWith(sc *sftp.Client, remotePath, localPath string, progress func(DownloadProgress)) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(localPath), err)
	}

	src, err := sc.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer src.Close()

	stat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat remote %s: %w", remotePath, err)
	}

	dst, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create local %s: %w", localPath, err)
	}
	defer dst.Close()

	pw := &progressWriter{
		dst:      dst,
		total:    stat.Size(),
		file:     remotePath,
		progress: progress,
	}
	if _, err := src.WriteTo(pw); err != nil {
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	if progress != nil {
		progress(DownloadProgress{Written: pw.written, Total: pw.total, File: remotePath})
	}
	return nil
}

type FailureDecision int

const (
	DecisionAbort FailureDecision = iota
	DecisionSkip
)

// BatchItem describes a single download target: file or directory tree.
type BatchItem struct {
	RemotePath string
	LocalPath  string
	IsDir      bool
}

// BatchOptions tunes DownloadBatch. Parallel <= 0 falls back to 4; values
// above 32 are capped because each worker holds its own SFTP read pipeline
// and the server typically refuses past that.
type BatchOptions struct {
	Parallel int
}

type fileTask struct {
	remote string
	local  string
	size   int64
}

// DownloadBatch downloads many files (and/or directory trees) in parallel.
// Files inside a single directory get flattened and shared across the worker
// pool, so a folder of many small files no longer pays RTT serially. For a
// single large file, performance equals DownloadFile (which already uses
// concurrent reads internally).
//
// Progress is aggregated: the callback receives DownloadProgress whose
// Written/Total are sums across the whole batch and File is one of the
// currently in-flight items (for display). Final emit has File="".
//
// onFailure receives per-file errors. The UI-side callback typically prompts
// the user and returns Skip or Abort. Abort is sticky: once any worker hears
// Abort, no further tasks are dispatched and in-flight tasks finish their
// current file before exiting.
func (c *Client) DownloadBatch(items []BatchItem, opts BatchOptions, progress func(DownloadProgress), onFailure FailureCallback) error {
	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = 4
	}
	if parallel > 32 {
		parallel = 32
	}

	var aborted atomic.Bool
	var failureMu sync.Mutex

	handleFailure := func(p string, err error) FailureDecision {
		if aborted.Load() {
			return DecisionAbort
		}
		if onFailure == nil {
			aborted.Store(true)
			return DecisionAbort
		}
		failureMu.Lock()
		defer failureMu.Unlock()
		if aborted.Load() {
			return DecisionAbort
		}
		d := onFailure(p, err)
		if d == DecisionAbort {
			aborted.Store(true)
		}
		return d
	}

	// Phase 1: scan all files, collect tasks and total size.
	var tasks []fileTask
	var totalBytes int64

	// emitScanFile throttles per-file scan events so they don't flood the
	// channel. Directory events (emitted before each List round-trip) are
	// always sent unthrottled — they show what we're waiting for during the
	// network pause and are rare enough not to overwhelm the channel.
	var lastScanEmit time.Time
	emitScanFile := func(p string) {
		if progress == nil || time.Since(lastScanEmit) < 50*time.Millisecond {
			return
		}
		lastScanEmit = time.Now()
		progress(DownloadProgress{ScanFile: p})
	}

	var walkDir func(remote, local string)
	walkDir = func(remote, local string) {
		if aborted.Load() {
			return
		}
		// Emit directory path before the List() round-trip so the UI shows
		// what we're waiting for during the network pause instead of hanging
		// on the last file name.
		if progress != nil {
			progress(DownloadProgress{ScanFile: remote + "/"})
		}
		entries, err := c.List(remote)
		if err != nil {
			handleFailure(remote, err)
			return
		}
		for _, e := range entries {
			if aborted.Load() {
				return
			}
			localChild := filepath.Join(local, e.Name)
			if e.IsDir {
				walkDir(e.Path, localChild)
			} else {
				totalBytes += e.Size
				tasks = append(tasks, fileTask{remote: e.Path, local: localChild, size: e.Size})
				emitScanFile(e.Path)
			}
		}
	}

	for _, item := range items {
		if aborted.Load() {
			break
		}
		if !item.IsDir {
			stat, err := c.sftp.Stat(item.RemotePath)
			if err != nil {
				handleFailure(item.RemotePath, err)
				continue
			}
			totalBytes += stat.Size()
			tasks = append(tasks, fileTask{remote: item.RemotePath, local: item.LocalPath, size: stat.Size()})
			emitScanFile(item.RemotePath)
		} else {
			walkDir(item.RemotePath, item.LocalPath)
		}
	}

	if aborted.Load() || len(tasks) == 0 {
		return nil
	}

	// Phase 2: download all collected tasks in parallel with a fixed total.
	taskCh := make(chan fileTask, len(tasks))
	for _, t := range tasks {
		taskCh <- t
	}
	close(taskCh)

	var (
		aggMu          sync.Mutex
		perFile        = make(map[string]int64)
		active         = make(map[string]bool)
		completedFiles = make(map[string]bool)
		filesDone      int64
		lastEmit       time.Time
	)
	perFileProgress := func(p DownloadProgress) {
		aggMu.Lock()
		perFile[p.File] = p.Written
		done := p.Total == 0 || p.Written >= p.Total
		if done {
			delete(active, p.File)
			if !completedFiles[p.File] {
				completedFiles[p.File] = true
				filesDone++
			}
		} else {
			active[p.File] = true
		}
		var written int64
		for _, w := range perFile {
			written += w
		}
		shouldEmit := time.Since(lastEmit) >= 50*time.Millisecond
		if shouldEmit {
			lastEmit = time.Now()
		}
		var current string
		for f := range active {
			current = f
			break
		}
		fd := filesDone
		aggMu.Unlock()
		if shouldEmit && progress != nil {
			progress(DownloadProgress{Written: written, Total: totalBytes, File: current, FilesDone: fd, FilesTotal: int64(len(tasks))})
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Go(func() {
			// Each worker gets its own SFTP session (separate SSH channel with
			// independent flow-control window). Sharing one session serialises
			// all READ requests through one channel and kills throughput.
			sc, err := sftp.NewClient(c.ssh,
				sftp.UseConcurrentReads(true),
				sftp.MaxConcurrentRequestsPerFile(64),
			)
			if err != nil {
				sc = c.sftp // fall back to shared session
			} else {
				defer sc.Close()
			}
			for t := range taskCh {
				if aborted.Load() {
					continue
				}
				if err := downloadFileWith(sc, t.remote, t.local, perFileProgress); err != nil {
					handleFailure(t.remote, err)
				}
			}
		})
	}
	wg.Wait()

	if progress != nil {
		aggMu.Lock()
		var written int64
		for _, w := range perFile {
			written += w
		}
		fd := filesDone
		aggMu.Unlock()
		progress(DownloadProgress{Written: written, Total: totalBytes, File: "", FilesDone: fd, FilesTotal: int64(len(tasks))})
	}

	return nil
}

// FailureCallback is invoked when a file or sub-directory fails to download.
// Return DecisionSkip to continue with the next entry, DecisionAbort to stop.
type FailureCallback func(path string, err error) FailureDecision

func (c *Client) DownloadDir(remotePath, localPath string, progress func(DownloadProgress), onFailure FailureCallback) error {
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", localPath, err)
	}

	entries, err := c.List(remotePath)
	if err != nil {
		return fmt.Errorf("list remote %s: %w", remotePath, err)
	}

	for _, entry := range entries {
		localEntry := filepath.Join(localPath, entry.Name)
		var dlErr error
		if entry.IsDir {
			dlErr = c.DownloadDir(entry.Path, localEntry, progress, onFailure)
		} else {
			dlErr = c.DownloadFile(entry.Path, localEntry, progress)
		}
		if dlErr == nil {
			continue
		}
		if onFailure == nil {
			return dlErr
		}
		if onFailure(entry.Path, dlErr) == DecisionAbort {
			return dlErr
		}
	}
	return nil
}

func (c *Client) Stat(path string) (os.FileInfo, error) {
	return c.sftp.Stat(path)
}

// Remove deletes a file. For directories use RemoveAll.
func (c *Client) Remove(p string) error {
	return c.sftp.Remove(p)
}

// RemoveAll deletes p recursively. Safe on both files and directories.
func (c *Client) RemoveAll(p string) error {
	info, err := c.sftp.Stat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return c.sftp.Remove(p)
	}
	entries, err := c.sftp.ReadDir(p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "." || e.Name() == ".." {
			continue
		}
		child := path.Join(p, e.Name())
		if e.IsDir() {
			if err := c.RemoveAll(child); err != nil {
				return err
			}
		} else {
			if err := c.sftp.Remove(child); err != nil {
				return err
			}
		}
	}
	return c.sftp.RemoveDirectory(p)
}

func (c *Client) Rename(oldPath, newPath string) error {
	return c.sftp.Rename(oldPath, newPath)
}

func (c *Client) Mkdir(p string) error {
	return c.sftp.MkdirAll(p)
}

func (c *Client) Chmod(p string, mode os.FileMode) error {
	return c.sftp.Chmod(p, mode)
}

// ReadFileChunk reads up to maxBytes from remotePath. truncated is true when
// the file is larger than maxBytes. Used for in-app preview.
func (c *Client) ReadFileChunk(remotePath string, maxBytes int64) (data []byte, truncated bool, err error) {
	src, err := c.sftp.Open(remotePath)
	if err != nil {
		return nil, false, err
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	n := stat.Size()
	if n > maxBytes {
		truncated = true
		n = maxBytes
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(src, buf); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, false, err
	}
	return buf, truncated, nil
}

// UploadProgress mirrors DownloadProgress but for the upload direction.
type UploadProgress struct {
	Written int64
	Total   int64
	File    string
}

// progressReader wraps a local reader and emits throttled progress callbacks
// each time bytes are pulled from it. Used as the source for sftp.File.ReadFrom,
// which dispatches writes concurrently but reads input strictly in order.
type progressReader struct {
	src      io.Reader
	written  int64
	total    int64
	file     string
	progress func(UploadProgress)
	last     time.Time
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	r.written += int64(n)
	if r.progress != nil && (err != nil || time.Since(r.last) >= 50*time.Millisecond) {
		r.last = time.Now()
		r.progress(UploadProgress{Written: r.written, Total: r.total, File: r.file})
	}
	return n, err
}

func (c *Client) UploadFile(localPath, remotePath string, progress func(UploadProgress)) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat local %s: %w", localPath, err)
	}
	if dir := path.Dir(remotePath); dir != "" && dir != "." && dir != "/" {
		_ = c.sftp.MkdirAll(dir)
	}
	dst, err := c.sftp.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote %s: %w", remotePath, err)
	}
	defer dst.Close()
	pr := &progressReader{
		src:      src,
		total:    stat.Size(),
		file:     remotePath,
		progress: progress,
	}
	if _, err := dst.ReadFrom(pr); err != nil {
		return fmt.Errorf("upload %s: %w", localPath, err)
	}
	if progress != nil {
		progress(UploadProgress{Written: pr.written, Total: pr.total, File: remotePath})
	}
	return nil
}

func (c *Client) UploadDir(localRoot, remoteRoot string, progress func(UploadProgress), onFailure FailureCallback) error {
	if err := c.sftp.MkdirAll(remoteRoot); err != nil {
		return fmt.Errorf("mkdir remote %s: %w", remoteRoot, err)
	}
	entries, err := os.ReadDir(localRoot)
	if err != nil {
		return fmt.Errorf("read local %s: %w", localRoot, err)
	}
	for _, e := range entries {
		localChild := filepath.Join(localRoot, e.Name())
		remoteChild := path.Join(remoteRoot, e.Name())
		var upErr error
		if e.IsDir() {
			upErr = c.UploadDir(localChild, remoteChild, progress, onFailure)
		} else {
			upErr = c.UploadFile(localChild, remoteChild, progress)
		}
		if upErr == nil {
			continue
		}
		if onFailure == nil {
			return upErr
		}
		if onFailure(localChild, upErr) == DecisionAbort {
			return upErr
		}
	}
	return nil
}
