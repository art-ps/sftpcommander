package sftp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type FileEntry struct {
	Name      string
	Path      string
	IsDir     bool
	IsSymlink bool
	Size      int64
	Mode      os.FileMode
	ModTime   time.Time
}

type Client struct {
	ssh         *ssh.Client
	sftp        *sftp.Client
	sessionPool chan *sftp.Client
}

const maxPooledSessions = 32

// AcquireSession returns a worker-private sftp.Client (own SSH channel,
// independent flow-control window) from the pool, or creates a new one if the
// pool is empty. The returned release fn returns the session to the pool, or
// closes it if the pool is already full. Workers must always call release
// when done; on error the session is closed instead of pooled.
func (c *Client) AcquireSession() (*sftp.Client, func(), error) {
	select {
	case s := <-c.sessionPool:
		return s, func() { c.releaseSession(s) }, nil
	default:
	}
	s, err := sftp.NewClient(c.ssh,
		sftp.UseConcurrentReads(true),
		sftp.MaxConcurrentRequestsPerFile(64),
	)
	if err != nil {
		return nil, nil, err
	}
	return s, func() { c.releaseSession(s) }, nil
}

func (c *Client) releaseSession(s *sftp.Client) {
	select {
	case c.sessionPool <- s:
	default:
		s.Close()
	}
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
	return ConnectWithProxy(host, port, user, password, keyPath, keyPassphrase, "")
}

// ConnectWithProxy is like Connect but routes the SSH session through the
// given ProxyJump alias from ~/.ssh/config (single hop). Empty proxyJump is
// equivalent to Connect. The jump host authenticates via ssh-agent and/or the
// IdentityFile declared in its own ssh_config block — passwords are not
// prompted for the jump.
func ConnectWithProxy(host, port, user, password, keyPath, keyPassphrase, proxyJump string) (*Client, error) {
	var jumpClient *ssh.Client
	if proxyJump != "" {
		jc, err := dialJumpHost(proxyJump)
		if err != nil {
			return nil, fmt.Errorf("proxy jump %s: %w", proxyJump, err)
		}
		jumpClient = jc
	}

	client, err := connectOnce(host, port, user, password, keyPath, keyPassphrase, nil, jumpClient)
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
		return connectOnce(host, port, user, password, keyPath, keyPassphrase, chg.knownTypes, jumpClient)
	}
	if jumpClient != nil {
		jumpClient.Close()
	}
	return nil, err
}

func dialJumpHost(alias string) (*ssh.Client, error) {
	cfg := LookupSSHConfig(alias)
	host := cfg.HostName
	if host == "" {
		host = alias
	}
	port := cfg.Port
	if port == "" {
		port = "22"
	}
	user := cfg.User
	if user == "" {
		if u := os.Getenv("USER"); u != "" {
			user = u
		}
	}
	return sshDial(host, port, user, "", cfg.IdentityFile, "", nil, nil)
}

func connectOnce(host, port, user, password, keyPath, keyPassphrase string, hostKeyAlgorithms []string, jumpClient *ssh.Client) (*Client, error) {
	sshClient, err := sshDial(host, port, user, password, keyPath, keyPassphrase, hostKeyAlgorithms, jumpClient)
	if err != nil {
		return nil, err
	}
	sftpClient, err := sftp.NewClient(sshClient,
		sftp.UseConcurrentReads(true),
		sftp.MaxConcurrentRequestsPerFile(64),
	)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}
	return &Client{
		ssh:         sshClient,
		sftp:        sftpClient,
		sessionPool: make(chan *sftp.Client, maxPooledSessions),
	}, nil
}

func sshDial(host, port, user, password, keyPath, keyPassphrase string, hostKeyAlgorithms []string, jumpClient *ssh.Client) (*ssh.Client, error) {
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

	addr := host + ":" + port
	var sshClient *ssh.Client
	if jumpClient != nil {
		// Tunnel a TCP connection through the jump host, then negotiate SSH
		// over it. ssh.Dial can't take a custom net.Conn, so we go through
		// NewClientConn manually.
		nconn, derr := jumpClient.Dial("tcp", addr)
		if derr != nil {
			return nil, fmt.Errorf("ssh dial via jump: %w", derr)
		}
		c, chans, reqs, herr := ssh.NewClientConn(nconn, addr, config)
		if herr != nil {
			nconn.Close()
			if unknownHost != nil {
				return nil, unknownHost
			}
			if changedHost != nil {
				return nil, changedHost
			}
			return nil, fmt.Errorf("ssh handshake via jump: %w", herr)
		}
		sshClient = ssh.NewClient(c, chans, reqs)
	} else {
		var derr error
		sshClient, derr = ssh.Dial("tcp", addr, config)
		if derr != nil {
			// Prefer typed host-key errors over the wrapped handshake error.
			if unknownHost != nil {
				return nil, unknownHost
			}
			if changedHost != nil {
				return nil, changedHost
			}
			return nil, fmt.Errorf("ssh dial: %w", derr)
		}
	}

	return sshClient, nil
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
	// IPv6 hostnames must be bracketed before joining with a port or
	// net.JoinHostPort/Normalize can't parse them back ("::1:2222" is
	// ambiguous; "[::1]:2222" is not).
	_, port, _ := net.SplitHostPort(challenge.Address)
	withPort := func(host string) string {
		if port == "" {
			return host
		}
		return net.JoinHostPort(host, port)
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
	for {
		select {
		case s := <-c.sessionPool:
			s.Close()
		default:
			c.sftp.Close()
			c.ssh.Close()
			return
		}
	}
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
			Name:      info.Name(),
			Path:      path.Join(remotePath, info.Name()),
			IsDir:     info.IsDir(),
			IsSymlink: info.Mode()&os.ModeSymlink != 0,
			Size:      info.Size(),
			Mode:      info.Mode(),
			ModTime:   info.ModTime(),
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
	return downloadFileWith(c.sftp, remotePath, localPath, 0, progress)
}

// downloadFileWith pulls remotePath into localPath. When startOffset > 0 the
// local file is opened without truncation and both sides are seeked to the
// offset — used by the resume path. The concurrent-reads fast path
// (sftp.File.WriteTo) cannot honor a non-zero start offset, so resumed
// transfers fall back to a single-stream io.Copy.
func downloadFileWith(sc *sftp.Client, remotePath, localPath string, startOffset int64, progress func(DownloadProgress)) error {
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

	var dst *os.File
	if startOffset > 0 {
		dst, err = os.OpenFile(localPath, os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open local %s for resume: %w", localPath, err)
		}
		if _, err := dst.Seek(startOffset, io.SeekStart); err != nil {
			dst.Close()
			return fmt.Errorf("seek local %s: %w", localPath, err)
		}
		if _, err := src.Seek(startOffset, io.SeekStart); err != nil {
			dst.Close()
			return fmt.Errorf("seek remote %s: %w", remotePath, err)
		}
	} else {
		dst, err = os.Create(localPath)
		if err != nil {
			return fmt.Errorf("create local %s: %w", localPath, err)
		}
	}
	defer dst.Close()

	pw := &progressWriter{
		dst:      dst,
		written:  startOffset,
		total:    stat.Size(),
		file:     remotePath,
		progress: progress,
	}
	if startOffset > 0 {
		if _, err := io.Copy(pw, src); err != nil {
			return fmt.Errorf("download %s: %w", remotePath, err)
		}
	} else {
		if _, err := src.WriteTo(pw); err != nil {
			return fmt.Errorf("download %s: %w", remotePath, err)
		}
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

type OverwriteDecision int

const (
	OverwriteAbort OverwriteDecision = iota
	OverwriteSkip
	OverwriteReplace
	// OverwriteResume — destination is a prefix of source; continue writing
	// from existingSize. Only meaningful when existingSize < newSize, the
	// callback is expected to enforce that.
	OverwriteResume
)

// OverwriteCallback is invoked when a destination file already exists.
// existingSize/newSize help the UI show a meaningful prompt. The caller is
// expected to be sticky on "all"-variants on its own side.
type OverwriteCallback func(path string, existingSize, newSize int64) OverwriteDecision

// BatchItem describes a single download target: file or directory tree.
type BatchItem struct {
	RemotePath string
	LocalPath  string
	IsDir      bool
}

// BatchOptions tunes DownloadBatch/UploadBatch. Parallel <= 0 falls back to
// 4; values above 32 are capped because each worker holds its own SFTP
// pipeline and the server typically refuses past that. OnOverwrite is
// invoked from a worker goroutine when the destination file already exists
// and must be safe to call concurrently.
// Verify enables a post-transfer SHA256 check: local sum vs `sha256sum`
// executed on the server over an ssh.Session. Mismatches are reported via
// onFailure.
type BatchOptions struct {
	Parallel    int
	OnOverwrite OverwriteCallback
	Verify      bool
}

// SHA256Remote runs `sha256sum -- path` over a fresh ssh session and parses
// the first hex token. Requires `sha256sum` in the server's PATH.
func (c *Client) SHA256Remote(remotePath string) (string, error) {
	session, err := c.ssh.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer session.Close()
	cmd := "sha256sum -- " + shellQuote(remotePath)
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("sha256sum: %s", msg)
		}
		return "", fmt.Errorf("sha256sum: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("sha256sum: empty output")
	}
	return fields[0], nil
}

func sha256Local(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type fileTask struct {
	remote string
	local  string
	size   int64
}

type scanJob struct {
	remote string
	local  string
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

	// Phase 1: parallel scan. Scan workers consume a shared queue of
	// directories, each acquiring its own SFTP session so concurrent List
	// calls run on independent SSH channels. Files and subdir jobs are
	// pushed back under scanMu; termination fires when no dir is queued
	// AND none is in-flight (tracked by scanPending).
	var (
		scanMu      sync.Mutex
		tasks       []fileTask
		totalBytes  int64
		visited     = map[string]bool{}
		scanJobs    []scanJob
		scanPending int
		scanClosed  bool
	)
	scanCond := sync.NewCond(&scanMu)

	pushDir := func(j scanJob) {
		scanMu.Lock()
		if visited[j.remote] {
			scanMu.Unlock()
			return
		}
		visited[j.remote] = true
		scanJobs = append(scanJobs, j)
		scanPending++
		scanCond.Signal()
		scanMu.Unlock()
	}
	popDir := func() (scanJob, bool) {
		scanMu.Lock()
		for len(scanJobs) == 0 && !scanClosed {
			scanCond.Wait()
		}
		if len(scanJobs) == 0 {
			scanMu.Unlock()
			return scanJob{}, false
		}
		n := len(scanJobs) - 1
		j := scanJobs[n]
		scanJobs = scanJobs[:n]
		scanMu.Unlock()
		return j, true
	}
	doneDir := func() {
		scanMu.Lock()
		scanPending--
		if scanPending == 0 {
			scanClosed = true
			scanCond.Broadcast()
		}
		scanMu.Unlock()
	}

	var scanLastEmit atomic.Int64
	emitScanFile := func(p string) {
		if progress == nil {
			return
		}
		now := time.Now().UnixNano()
		last := scanLastEmit.Load()
		if now-last < int64(50*time.Millisecond) {
			return
		}
		if !scanLastEmit.CompareAndSwap(last, now) {
			return
		}
		scanMu.Lock()
		n := int64(len(tasks))
		tb := totalBytes
		scanMu.Unlock()
		progress(DownloadProgress{ScanFile: p, FilesTotal: n, Total: tb})
	}

	walkOne := func(sc *sftp.Client, j scanJob) {
		if aborted.Load() {
			return
		}
		if progress != nil {
			progress(DownloadProgress{ScanFile: j.remote + "/"})
		}
		infos, err := sc.ReadDir(j.remote)
		if err != nil {
			handleFailure(j.remote, err)
			return
		}
		var (
			localTasks []fileTask
			localBytes int64
			subDirs    []scanJob
			lastFile   string
		)
		for _, info := range infos {
			if aborted.Load() {
				return
			}
			if info.Name() == "." || info.Name() == ".." {
				continue
			}
			ePath := path.Join(j.remote, info.Name())
			localChild := filepath.Join(j.local, info.Name())
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				target, lerr := sc.ReadLink(ePath)
				if lerr != nil {
					handleFailure(ePath, lerr)
					continue
				}
				resolved := target
				if !path.IsAbs(resolved) {
					resolved = path.Join(path.Dir(ePath), resolved)
				}
				tinfo, terr := sc.Stat(resolved)
				if terr != nil {
					handleFailure(ePath, terr)
					continue
				}
				if tinfo.IsDir() {
					subDirs = append(subDirs, scanJob{remote: resolved, local: localChild})
					continue
				}
				localBytes += tinfo.Size()
				localTasks = append(localTasks, fileTask{remote: resolved, local: localChild, size: tinfo.Size()})
				lastFile = resolved
			case info.IsDir():
				subDirs = append(subDirs, scanJob{remote: ePath, local: localChild})
			default:
				localBytes += info.Size()
				localTasks = append(localTasks, fileTask{remote: ePath, local: localChild, size: info.Size()})
				lastFile = ePath
			}
		}
		if len(localTasks) > 0 {
			scanMu.Lock()
			tasks = append(tasks, localTasks...)
			totalBytes += localBytes
			scanMu.Unlock()
		}
		for _, sd := range subDirs {
			pushDir(sd)
		}
		if lastFile != "" {
			emitScanFile(lastFile)
		}
	}

	// Seed: single-file items handled inline (one Stat each, no point
	// parallelising); directories enqueued for the scan pool.
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
			pushDir(scanJob{remote: item.RemotePath, local: item.LocalPath})
		}
	}

	// Run scan workers only if any directory was queued. Cap at 8 — beyond
	// that the SSH server typically refuses extra channels and the linear
	// speedup tapers off anyway.
	scanMu.Lock()
	hasDirs := scanPending > 0
	scanMu.Unlock()
	if hasDirs {
		scanWorkers := parallel
		if scanWorkers > 8 {
			scanWorkers = 8
		}
		var swg sync.WaitGroup
		for i := 0; i < scanWorkers; i++ {
			swg.Go(func() {
				sc, release, err := c.AcquireSession()
				if err != nil {
					sc = c.sftp
				} else {
					defer release()
				}
				for {
					j, ok := popDir()
					if !ok {
						return
					}
					walkOne(sc, j)
					doneDir()
				}
			})
		}
		swg.Wait()
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

	// Atomic aggregator. In-flight delta updates write to `written` directly;
	// `currentFile` is best-effort (whichever worker most recently posted is
	// shown). `lastEmit` throttles via CAS so only one worker per 50ms wins
	// the right to emit, the rest just update counters and move on.
	var (
		written     atomic.Int64
		filesDone   atomic.Int64
		lastEmit    atomic.Int64
		currentFile atomic.Value
	)
	currentFile.Store("")
	filesTotal := int64(len(tasks))
	emitNow := func() {
		if progress == nil {
			return
		}
		cur, _ := currentFile.Load().(string)
		progress(DownloadProgress{
			Written:    written.Load(),
			Total:      totalBytes,
			File:       cur,
			FilesDone:  filesDone.Load(),
			FilesTotal: filesTotal,
		})
	}
	maybeEmit := func() {
		now := time.Now().UnixNano()
		last := lastEmit.Load()
		if now-last < int64(50*time.Millisecond) {
			return
		}
		if !lastEmit.CompareAndSwap(last, now) {
			return
		}
		emitNow()
	}
	forceEmit := func() {
		lastEmit.Store(time.Now().UnixNano())
		emitNow()
	}

	var overwriteMu sync.Mutex
	checkOverwrite := func(localPath string, newSize int64) OverwriteDecision {
		if opts.OnOverwrite == nil {
			return OverwriteReplace
		}
		info, err := os.Stat(localPath)
		if err != nil {
			return OverwriteReplace
		}
		if info.IsDir() {
			return OverwriteSkip
		}
		overwriteMu.Lock()
		defer overwriteMu.Unlock()
		if aborted.Load() {
			return OverwriteAbort
		}
		d := opts.OnOverwrite(localPath, info.Size(), newSize)
		if d == OverwriteAbort {
			aborted.Store(true)
		}
		return d
	}

	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Go(func() {
			// Each worker gets its own SFTP session (separate SSH channel with
			// independent flow-control window). Sharing one session serialises
			// all READ requests through one channel and kills throughput.
			sc, release, err := c.AcquireSession()
			if err != nil {
				sc = c.sftp // fall back to shared session
			} else {
				defer release()
			}
			for t := range taskCh {
				if aborted.Load() {
					continue
				}
				var resumeOffset int64
				switch checkOverwrite(t.local, t.size) {
				case OverwriteSkip:
					written.Add(t.size)
					filesDone.Add(1)
					currentFile.Store(t.remote)
					forceEmit()
					continue
				case OverwriteAbort:
					continue
				case OverwriteResume:
					if info, err := os.Stat(t.local); err == nil {
						resumeOffset = info.Size()
						written.Add(resumeOffset)
					}
				}
				sentSoFar := resumeOffset
				fileProgress := func(p DownloadProgress) {
					if p.Written > sentSoFar {
						written.Add(p.Written - sentSoFar)
						sentSoFar = p.Written
					}
					currentFile.Store(t.remote)
					maybeEmit()
				}
				if err := downloadFileWith(sc, t.remote, t.local, resumeOffset, fileProgress); err != nil {
					handleFailure(t.remote, err)
					continue
				}
				if opts.Verify {
					if err := c.verifyDownloaded(t.remote, t.local); err != nil {
						handleFailure(t.remote, err)
					}
				}
				filesDone.Add(1)
				currentFile.Store(t.remote)
				forceEmit()
			}
		})
	}
	wg.Wait()

	if progress != nil {
		currentFile.Store("")
		forceEmit()
	}

	return nil
}

// verifyDownloaded computes local SHA256 of localPath and compares it to
// `sha256sum remote` on the server. Returns a "checksum mismatch" error on
// disagreement so the batch driver can surface it via onFailure like any
// other per-file error.
func (c *Client) verifyDownloaded(remote, local string) error {
	remoteHash, err := c.SHA256Remote(remote)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	localHash, err := sha256Local(local)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !strings.EqualFold(remoteHash, localHash) {
		return fmt.Errorf("checksum mismatch: remote=%s local=%s", remoteHash, localHash)
	}
	return nil
}

// verifyUploaded is the upload-direction counterpart of verifyDownloaded.
func (c *Client) verifyUploaded(local, remote string) error {
	localHash, err := sha256Local(local)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	remoteHash, err := c.SHA256Remote(remote)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !strings.EqualFold(remoteHash, localHash) {
		return fmt.Errorf("checksum mismatch: remote=%s local=%s", remoteHash, localHash)
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

// Readlink returns the literal target of a symlink (relative or absolute,
// as stored on the server). Used for "→ target" display next to symlinks.
func (c *Client) Readlink(p string) (string, error) {
	return c.sftp.ReadLink(p)
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

// CopyRemote duplicates src to dst on the server via an `cp -R` exec session.
// SFTP itself has no copy primitive so bytes would otherwise have to travel
// through the client. Requires a POSIX-ish remote with `cp` in PATH.
func (c *Client) CopyRemote(src, dst string) error {
	session, err := c.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer session.Close()
	cmd := "cp -R -- " + shellQuote(src) + " " + shellQuote(dst)
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("cp: %w", err)
		}
		return fmt.Errorf("cp: %s", msg)
	}
	return nil
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
// Safe against arbitrary user-provided paths going into a remote shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// ReadFileRange reads up to maxBytes from remotePath starting at offset and
// returns the total file size so the caller can detect EOF. Used by preview
// paging to fetch further chunks on demand.
func (c *Client) ReadFileRange(remotePath string, offset, maxBytes int64) (data []byte, total int64, err error) {
	src, err := c.sftp.Open(remotePath)
	if err != nil {
		return nil, 0, err
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return nil, 0, err
	}
	total = stat.Size()
	if offset >= total {
		return nil, total, nil
	}
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return nil, total, err
	}
	remaining := total - offset
	n := maxBytes
	if remaining < n {
		n = remaining
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(src, buf); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, total, err
	}
	return buf, total, nil
}

// UploadProgress mirrors DownloadProgress but for the upload direction.
type UploadProgress struct {
	Written    int64
	Total      int64
	File       string
	ScanFile   string
	FilesDone  int64
	FilesTotal int64
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

// UploadItem describes a single upload target.
type UploadItem struct {
	LocalPath  string
	RemotePath string
	IsDir      bool
}

type uploadFileTask struct {
	local  string
	remote string
	size   int64
}

// UploadBatch uploads many files (and/or directory trees) in parallel using
// the same worker-pool pattern as DownloadBatch. The pre-scan phase walks
// local directories, builds a flat task list and aggregates byte totals, then
// `Parallel` workers (each with its own SFTP session) consume the queue.
func (c *Client) UploadBatch(items []UploadItem, opts BatchOptions, progress func(UploadProgress), onFailure FailureCallback) error {
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

	var tasks []uploadFileTask
	var remoteDirs []string
	seenDir := make(map[string]bool)
	var totalBytes int64

	var lastScanEmit time.Time
	emitScan := func(p string) {
		if progress == nil || time.Since(lastScanEmit) < 50*time.Millisecond {
			return
		}
		lastScanEmit = time.Now()
		progress(UploadProgress{ScanFile: p, FilesTotal: int64(len(tasks)), Total: totalBytes})
	}

	addRemoteDir := func(p string) {
		if p == "" || p == "." || p == "/" || seenDir[p] {
			return
		}
		seenDir[p] = true
		remoteDirs = append(remoteDirs, p)
	}

	var walkLocal func(local, remote string)
	walkLocal = func(local, remote string) {
		if aborted.Load() {
			return
		}
		if progress != nil {
			progress(UploadProgress{ScanFile: local + "/"})
		}
		addRemoteDir(remote)
		entries, err := os.ReadDir(local)
		if err != nil {
			handleFailure(local, err)
			return
		}
		for _, e := range entries {
			if aborted.Load() {
				return
			}
			localChild := filepath.Join(local, e.Name())
			remoteChild := path.Join(remote, e.Name())
			info, err := e.Info()
			if err != nil {
				handleFailure(localChild, err)
				continue
			}
			// Skip symlinks to avoid following loops / unintended targets.
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				walkLocal(localChild, remoteChild)
			} else {
				totalBytes += info.Size()
				tasks = append(tasks, uploadFileTask{local: localChild, remote: remoteChild, size: info.Size()})
				emitScan(localChild)
			}
		}
	}

	for _, item := range items {
		if aborted.Load() {
			break
		}
		if item.IsDir {
			walkLocal(item.LocalPath, item.RemotePath)
			continue
		}
		info, err := os.Stat(item.LocalPath)
		if err != nil {
			handleFailure(item.LocalPath, err)
			continue
		}
		totalBytes += info.Size()
		tasks = append(tasks, uploadFileTask{local: item.LocalPath, remote: item.RemotePath, size: info.Size()})
		addRemoteDir(path.Dir(item.RemotePath))
		emitScan(item.LocalPath)
	}

	if aborted.Load() || len(tasks) == 0 {
		return nil
	}

	// Pre-create remote directory tree once (sequential, cheap relative to data).
	for _, d := range remoteDirs {
		if err := c.sftp.MkdirAll(d); err != nil {
			handleFailure(d, err)
			if aborted.Load() {
				return nil
			}
		}
	}

	taskCh := make(chan uploadFileTask, len(tasks))
	for _, t := range tasks {
		taskCh <- t
	}
	close(taskCh)

	var (
		written     atomic.Int64
		filesDone   atomic.Int64
		lastEmit    atomic.Int64
		currentFile atomic.Value
	)
	currentFile.Store("")
	filesTotal := int64(len(tasks))
	emitNow := func() {
		if progress == nil {
			return
		}
		cur, _ := currentFile.Load().(string)
		progress(UploadProgress{
			Written:    written.Load(),
			Total:      totalBytes,
			File:       cur,
			FilesDone:  filesDone.Load(),
			FilesTotal: filesTotal,
		})
	}
	maybeEmit := func() {
		now := time.Now().UnixNano()
		last := lastEmit.Load()
		if now-last < int64(50*time.Millisecond) {
			return
		}
		if !lastEmit.CompareAndSwap(last, now) {
			return
		}
		emitNow()
	}
	forceEmit := func() {
		lastEmit.Store(time.Now().UnixNano())
		emitNow()
	}

	var overwriteMu sync.Mutex
	checkOverwriteRemote := func(sc *sftp.Client, remotePath string, newSize int64) OverwriteDecision {
		if opts.OnOverwrite == nil {
			return OverwriteReplace
		}
		info, err := sc.Stat(remotePath)
		if err != nil {
			return OverwriteReplace
		}
		if info.IsDir() {
			return OverwriteSkip
		}
		overwriteMu.Lock()
		defer overwriteMu.Unlock()
		if aborted.Load() {
			return OverwriteAbort
		}
		d := opts.OnOverwrite(remotePath, info.Size(), newSize)
		if d == OverwriteAbort {
			aborted.Store(true)
		}
		return d
	}

	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Go(func() {
			sc, release, err := c.AcquireSession()
			if err != nil {
				sc = c.sftp
			} else {
				defer release()
			}
			for t := range taskCh {
				if aborted.Load() {
					continue
				}
				var resumeOffset int64
				switch checkOverwriteRemote(sc, t.remote, t.size) {
				case OverwriteSkip:
					written.Add(t.size)
					filesDone.Add(1)
					currentFile.Store(t.remote)
					forceEmit()
					continue
				case OverwriteAbort:
					continue
				case OverwriteResume:
					if info, err := sc.Stat(t.remote); err == nil {
						resumeOffset = info.Size()
						written.Add(resumeOffset)
					}
				}
				sentSoFar := resumeOffset
				fileProgress := func(p UploadProgress) {
					if p.Written > sentSoFar {
						written.Add(p.Written - sentSoFar)
						sentSoFar = p.Written
					}
					currentFile.Store(t.remote)
					maybeEmit()
				}
				if err := uploadFileWith(sc, t.local, t.remote, resumeOffset, fileProgress); err != nil {
					handleFailure(t.local, err)
					continue
				}
				if opts.Verify {
					if err := c.verifyUploaded(t.local, t.remote); err != nil {
						handleFailure(t.local, err)
					}
				}
				filesDone.Add(1)
				currentFile.Store(t.remote)
				forceEmit()
			}
		})
	}
	wg.Wait()

	if progress != nil {
		currentFile.Store("")
		forceEmit()
	}
	return nil
}

func uploadFileWith(sc *sftp.Client, localPath, remotePath string, startOffset int64, progress func(UploadProgress)) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat local %s: %w", localPath, err)
	}
	var dst *sftp.File
	if startOffset > 0 {
		dst, err = sc.OpenFile(remotePath, os.O_WRONLY)
		if err != nil {
			return fmt.Errorf("open remote %s for resume: %w", remotePath, err)
		}
		if _, err := dst.Seek(startOffset, io.SeekStart); err != nil {
			dst.Close()
			return fmt.Errorf("seek remote %s: %w", remotePath, err)
		}
		if _, err := src.Seek(startOffset, io.SeekStart); err != nil {
			dst.Close()
			return fmt.Errorf("seek local %s: %w", localPath, err)
		}
	} else {
		dst, err = sc.Create(remotePath)
		if err != nil {
			return fmt.Errorf("create remote %s: %w", remotePath, err)
		}
	}
	defer dst.Close()
	pr := &progressReader{
		src:      src,
		written:  startOffset,
		total:    stat.Size(),
		file:     remotePath,
		progress: progress,
	}
	if startOffset > 0 {
		if _, err := io.Copy(dst, pr); err != nil {
			return fmt.Errorf("upload %s: %w", localPath, err)
		}
	} else {
		if _, err := dst.ReadFrom(pr); err != nil {
			return fmt.Errorf("upload %s: %w", localPath, err)
		}
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
