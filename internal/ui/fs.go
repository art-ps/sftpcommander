package ui

import (
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	sftpclient "sftpbrowser/internal/sftp"
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
		Name:    info.Name(),
		Path:    p,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
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
			Name:    info.Name(),
			Path:    filepath.Join(p, info.Name()),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
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
	info, err := os.Stat(p)
	if err != nil {
		return sftpclient.FileEntry{}, err
	}
	return sftpclient.FileEntry{
		Name:    info.Name(),
		Path:    p,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
	}, nil
}

func (l *LocalFS) Remove(p string) error                { return os.Remove(p) }
func (l *LocalFS) RemoveAll(p string) error             { return os.RemoveAll(p) }
func (l *LocalFS) Rename(o, n string) error             { return os.Rename(o, n) }
func (l *LocalFS) Mkdir(p string) error                 { return os.MkdirAll(p, 0o755) }
func (l *LocalFS) Chmod(p string, m os.FileMode) error  { return os.Chmod(p, m) }

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
