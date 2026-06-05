package ui

import (
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"
)

// FileSystem is the minimal set of operations both panels need. The remote
// adapter wraps *sftpclient.Client; the local adapter calls into os/filepath.
// All methods take absolute paths in the filesystem's native format.
//
// Path manipulation is delegated to the FS because the local side may use
// platform-specific separators (Windows in the future); both adapters here
// normalize to forward slashes on Linux/Mac.
type FileSystem interface {
	List(path string) ([]sftpclient.FileEntry, error)
	Stat(path string) (sftpclient.FileEntry, error)
	Remove(path string) error
	RemoveAll(path string) error
	Rename(oldPath, newPath string) error
	Mkdir(path string) error
	Chmod(path string, mode os.FileMode) error
	ReadFileChunk(path string, maxBytes int64) (data []byte, truncated bool, err error)
	ReadFileRange(path string, offset, maxBytes int64) (data []byte, total int64, err error)
	Readlink(path string) (string, error)

	Home() string
	Join(parts ...string) string
	Dir(p string) string
	Base(p string) string

	// Kind returns "local" or "remote" — used by TwoPane to decide transfer
	// direction.
	Kind() string
	// Label is shown in the panel header (e.g. "local" or "user@host:port").
	Label() string
}

// --- Remote ----------------------------------------------------------------

type RemoteFS struct {
	client *sftpclient.Client
	label  string
}

func NewRemoteFS(client *sftpclient.Client, label string) *RemoteFS {
	return &RemoteFS{client: client, label: label}
}

func (r *RemoteFS) Client() *sftpclient.Client { return r.client }

func (r *RemoteFS) List(p string) ([]sftpclient.FileEntry, error) { return r.client.List(p) }

func (r *RemoteFS) Stat(p string) (sftpclient.FileEntry, error) {
	info, err := r.client.Stat(p)
	if err != nil {
		return sftpclient.FileEntry{}, err
	}
	return sftpclient.FileEntry{
		Name:      info.Name(),
		Path:      p,
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
		Size:      info.Size(),
		Mode:      info.Mode(),
		ModTime:   info.ModTime(),
	}, nil
}

func (r *RemoteFS) Remove(p string) error             { return r.client.Remove(p) }
func (r *RemoteFS) RemoveAll(p string) error          { return r.client.RemoveAll(p) }
func (r *RemoteFS) Rename(o, n string) error          { return r.client.Rename(o, n) }
func (r *RemoteFS) Mkdir(p string) error              { return r.client.Mkdir(p) }
func (r *RemoteFS) Chmod(p string, m os.FileMode) error { return r.client.Chmod(p, m) }

func (r *RemoteFS) ReadFileChunk(p string, maxBytes int64) ([]byte, bool, error) {
	return r.client.ReadFileChunk(p, maxBytes)
}

func (r *RemoteFS) Readlink(p string) (string, error) { return r.client.Readlink(p) }

func (r *RemoteFS) ReadFileRange(p string, offset, maxBytes int64) ([]byte, int64, error) {
	return r.client.ReadFileRange(p, offset, maxBytes)
}

func (r *RemoteFS) Home() string                    { return r.client.HomeDir() }
func (r *RemoteFS) Join(parts ...string) string     { return pathpkg.Join(parts...) }
func (r *RemoteFS) Dir(p string) string             { return pathpkg.Dir(p) }
func (r *RemoteFS) Base(p string) string            { return pathpkg.Base(p) }
func (r *RemoteFS) Kind() string                    { return "remote" }
func (r *RemoteFS) Label() string                   { return r.label }

// --- Local -----------------------------------------------------------------

type LocalFS struct{}

func NewLocalFS() *LocalFS { return &LocalFS{} }

func (l *LocalFS) List(p string) ([]sftpclient.FileEntry, error) {
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]sftpclient.FileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, sftpclient.FileEntry{
			Name:      info.Name(),
			Path:      filepath.Join(p, info.Name()),
			IsDir:     info.IsDir(),
			IsSymlink: info.Mode()&os.ModeSymlink != 0,
			Size:      info.Size(),
			Mode:      info.Mode(),
			ModTime:   info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (l *LocalFS) Stat(p string) (sftpclient.FileEntry, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return sftpclient.FileEntry{}, err
	}
	return sftpclient.FileEntry{
		Name:      info.Name(),
		Path:      p,
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
		Size:      info.Size(),
		Mode:      info.Mode(),
		ModTime:   info.ModTime(),
	}, nil
}

func (l *LocalFS) Remove(p string) error                { return os.Remove(p) }
func (l *LocalFS) RemoveAll(p string) error             { return os.RemoveAll(p) }
func (l *LocalFS) Rename(o, n string) error             { return os.Rename(o, n) }
func (l *LocalFS) Mkdir(p string) error                 { return os.MkdirAll(p, 0o755) }
func (l *LocalFS) Chmod(p string, m os.FileMode) error  { return os.Chmod(p, m) }

func (l *LocalFS) Readlink(p string) (string, error) { return os.Readlink(p) }

func (l *LocalFS) ReadFileRange(p string, offset, maxBytes int64) ([]byte, int64, error) {
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	total := stat.Size()
	if offset >= total {
		return nil, total, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, total, err
	}
	remaining := total - offset
	n := maxBytes
	if remaining < n {
		n = remaining
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, total, err
	}
	return buf, total, nil
}

func (l *LocalFS) ReadFileChunk(p string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	n := stat.Size()
	truncated := n > maxBytes
	if truncated {
		n = maxBytes
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, false, err
	}
	return buf, truncated, nil
}

func (l *LocalFS) Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return string(filepath.Separator)
	}
	return h
}

func (l *LocalFS) Join(parts ...string) string { return filepath.Join(parts...) }
func (l *LocalFS) Dir(p string) string         { return filepath.Dir(p) }
func (l *LocalFS) Base(p string) string        { return filepath.Base(p) }
func (l *LocalFS) Kind() string                { return "local" }
func (l *LocalFS) Label() string               { return "local" }

// --- Cached wrapper --------------------------------------------------------

// CachedFS wraps a FileSystem and memoises List(path) results. Cache is
// invalidated explicitly on Refresh (`R` in the UI) and on any write op that
// changes a directory's contents (Mkdir/Rename/Remove/Chmod). There's no TTL —
// stale entries persist until invalidated, which matches the requirement that
// stale cache reflects user intent ("I haven't pressed R").
type CachedFS struct {
	inner FileSystem
	mu    sync.Mutex
	cache map[string][]sftpclient.FileEntry
}

func NewCachedFS(inner FileSystem) *CachedFS {
	return &CachedFS{inner: inner, cache: make(map[string][]sftpclient.FileEntry)}
}

// Inner returns the underlying FS — used by code that needs a concrete
// *RemoteFS (e.g. to access *sftp.Client for copy/edit).
func (c *CachedFS) Inner() FileSystem { return c.inner }

func (c *CachedFS) Invalidate(p string) {
	c.mu.Lock()
	delete(c.cache, p)
	c.mu.Unlock()
}

func (c *CachedFS) InvalidateAll() {
	c.mu.Lock()
	c.cache = make(map[string][]sftpclient.FileEntry)
	c.mu.Unlock()
}

func (c *CachedFS) List(p string) ([]sftpclient.FileEntry, error) {
	c.mu.Lock()
	if e, ok := c.cache[p]; ok {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()
	entries, err := c.inner.List(p)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[p] = entries
	c.mu.Unlock()
	return entries, nil
}

func (c *CachedFS) Stat(p string) (sftpclient.FileEntry, error) { return c.inner.Stat(p) }

func (c *CachedFS) Remove(p string) error {
	err := c.inner.Remove(p)
	c.Invalidate(c.inner.Dir(p))
	return err
}

func (c *CachedFS) RemoveAll(p string) error {
	err := c.inner.RemoveAll(p)
	c.Invalidate(c.inner.Dir(p))
	c.Invalidate(p)
	return err
}

func (c *CachedFS) Rename(o, n string) error {
	err := c.inner.Rename(o, n)
	c.Invalidate(c.inner.Dir(o))
	c.Invalidate(c.inner.Dir(n))
	return err
}

func (c *CachedFS) Mkdir(p string) error {
	err := c.inner.Mkdir(p)
	c.Invalidate(c.inner.Dir(p))
	return err
}

func (c *CachedFS) Chmod(p string, m os.FileMode) error {
	err := c.inner.Chmod(p, m)
	c.Invalidate(c.inner.Dir(p))
	return err
}

func (c *CachedFS) ReadFileChunk(p string, n int64) ([]byte, bool, error) {
	return c.inner.ReadFileChunk(p, n)
}

func (c *CachedFS) Readlink(p string) (string, error) { return c.inner.Readlink(p) }

func (c *CachedFS) ReadFileRange(p string, offset, maxBytes int64) ([]byte, int64, error) {
	return c.inner.ReadFileRange(p, offset, maxBytes)
}

func (c *CachedFS) Home() string                { return c.inner.Home() }
func (c *CachedFS) Join(parts ...string) string { return c.inner.Join(parts...) }
func (c *CachedFS) Dir(p string) string         { return c.inner.Dir(p) }
func (c *CachedFS) Base(p string) string        { return c.inner.Base(p) }
func (c *CachedFS) Kind() string                { return c.inner.Kind() }
func (c *CachedFS) Label() string               { return c.inner.Label() }

// unwrapFS returns the concrete FS underneath any CachedFS layer. Code that
// needs the *RemoteFS (e.g. to grab *sftp.Client for copy/edit operations)
// goes through this so caching stays transparent at the call site.
func unwrapFS(fs FileSystem) FileSystem {
	for {
		w, ok := fs.(interface{ Inner() FileSystem })
		if !ok {
			return fs
		}
		fs = w.Inner()
	}
}

// chmodRecursive applies mode to p and, if p is a directory, every entry
// below it. Walks via FileSystem.List so it works for both local and remote.
func chmodRecursive(fs FileSystem, p string, mode os.FileMode) error {
	info, err := fs.Stat(p)
	if err != nil {
		return err
	}
	if err := fs.Chmod(p, mode); err != nil {
		return err
	}
	if !info.IsDir {
		return nil
	}
	entries, err := fs.List(p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsSymlink {
			continue
		}
		if err := chmodRecursive(fs, e.Path, mode); err != nil {
			return err
		}
	}
	return nil
}
