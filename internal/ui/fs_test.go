package ui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"
)

func TestLocalFS_MkdirListStatRemove(t *testing.T) {
	dir := t.TempDir()
	fs := NewLocalFS()

	sub := filepath.Join(dir, "child")
	if err := fs.Mkdir(sub); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	st, err := fs.Stat(sub)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !st.IsDir {
		t.Errorf("expected IsDir=true for created directory, got %+v", st)
	}

	entries, err := fs.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "child" || !entries[0].IsDir {
		t.Errorf("List did not see new dir: %+v", entries)
	}

	if err := fs.Remove(sub); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := fs.Stat(sub); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected ErrNotExist after Remove, got %v", err)
	}
}

func TestLocalFS_ListOrdersDirsFirstThenLexCI(t *testing.T) {
	dir := t.TempDir()
	fs := NewLocalFS()

	mustWrite(t, filepath.Join(dir, "b.txt"), "")
	mustWrite(t, filepath.Join(dir, "A.txt"), "")
	mustMkdir(t, filepath.Join(dir, "z_sub"))
	mustMkdir(t, filepath.Join(dir, "a_sub"))

	entries, err := fs.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a_sub", "z_sub", "A.txt", "b.txt"}
	if len(entries) != len(want) {
		t.Fatalf("len=%d, want %d", len(entries), len(want))
	}
	for i, name := range want {
		if entries[i].Name != name {
			t.Errorf("entries[%d].Name=%q, want %q", i, entries[i].Name, name)
		}
	}
}

func TestLocalFS_Rename(t *testing.T) {
	dir := t.TempDir()
	fs := NewLocalFS()

	src := filepath.Join(dir, "a")
	dst := filepath.Join(dir, "b")
	mustWrite(t, src, "hi")

	if err := fs.Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("src still exists after Rename: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dst missing after Rename: %v", err)
	}
}

func TestLocalFS_Chmod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows")
	}
	dir := t.TempDir()
	fs := NewLocalFS()

	p := filepath.Join(dir, "f")
	mustWrite(t, p, "x")

	if err := fs.Chmod(p, 0o640); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	st, _ := fs.Stat(p)
	if got := st.Mode.Perm(); got != 0o640 {
		t.Errorf("mode after Chmod: %#o, want 0640", got)
	}
}

func TestLocalFS_ReadFileChunk_Truncation(t *testing.T) {
	dir := t.TempDir()
	fs := NewLocalFS()
	p := filepath.Join(dir, "big")
	mustWrite(t, p, "0123456789")

	data, truncated, err := fs.ReadFileChunk(p, 4)
	if err != nil {
		t.Fatalf("ReadFileChunk: %v", err)
	}
	if !truncated {
		t.Errorf("expected truncated=true")
	}
	if string(data) != "0123" {
		t.Errorf("data=%q, want %q", string(data), "0123")
	}
}

func TestLocalFS_ReadFileChunk_WholeFile(t *testing.T) {
	dir := t.TempDir()
	fs := NewLocalFS()
	p := filepath.Join(dir, "small")
	mustWrite(t, p, "ab")

	data, truncated, err := fs.ReadFileChunk(p, 1024)
	if err != nil {
		t.Fatalf("ReadFileChunk: %v", err)
	}
	if truncated {
		t.Errorf("expected truncated=false for full read")
	}
	if string(data) != "ab" {
		t.Errorf("data=%q", string(data))
	}
}

func TestLocalFS_ReadFileRange(t *testing.T) {
	dir := t.TempDir()
	fs := NewLocalFS()
	p := filepath.Join(dir, "f")
	mustWrite(t, p, "abcdefghij")

	data, total, err := fs.ReadFileRange(p, 3, 4)
	if err != nil {
		t.Fatalf("ReadFileRange: %v", err)
	}
	if total != 10 {
		t.Errorf("total=%d, want 10", total)
	}
	if string(data) != "defg" {
		t.Errorf("data=%q, want defg", string(data))
	}

	// Offset past EOF.
	data, total, err = fs.ReadFileRange(p, 50, 4)
	if err != nil {
		t.Fatalf("ReadFileRange past EOF: %v", err)
	}
	if total != 10 || len(data) != 0 {
		t.Errorf("past-EOF should return total=10, empty data; got total=%d len=%d", total, len(data))
	}
}

func TestLocalFS_ReadlinkAndSymlinkFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privs on Windows")
	}
	dir := t.TempDir()
	fs := NewLocalFS()

	target := filepath.Join(dir, "target")
	mustWrite(t, target, "T")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := fs.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != target {
		t.Errorf("Readlink=%q, want %q", got, target)
	}

	st, err := fs.Stat(link)
	if err != nil {
		t.Fatalf("Stat link: %v", err)
	}
	if !st.IsSymlink {
		t.Errorf("expected IsSymlink=true for link, got %+v", st)
	}
}

func TestLocalFS_MetadataAccessors(t *testing.T) {
	fs := NewLocalFS()
	if fs.Kind() != "local" {
		t.Errorf("Kind=%q, want local", fs.Kind())
	}
	if fs.Label() != "local" {
		t.Errorf("Label=%q, want local", fs.Label())
	}
	if fs.Home() == "" {
		t.Errorf("Home() should not be empty")
	}
	if got := fs.Join("a", "b"); got != filepath.Join("a", "b") {
		t.Errorf("Join: %q", got)
	}
	if got := fs.Base("/a/b"); got != "b" {
		t.Errorf("Base: %q", got)
	}
	if got := fs.Dir("/a/b"); got != filepath.Dir("/a/b") {
		t.Errorf("Dir: %q", got)
	}
}

// --- CachedFS ---------------------------------------------------------------

// countingFS is a minimal FileSystem implementation that records List() hits
// and lets us drive specific return values. Only the methods CachedFS proxies
// need real behaviour; the rest can be no-ops.
type countingFS struct {
	*LocalFS
	listCalls atomic.Int32
	listFn    func(path string) ([]sftpclient.FileEntry, error)
}

func (c *countingFS) List(p string) ([]sftpclient.FileEntry, error) {
	c.listCalls.Add(1)
	if c.listFn != nil {
		return c.listFn(p)
	}
	return c.LocalFS.List(p)
}

func newCountingFS() *countingFS { return &countingFS{LocalFS: NewLocalFS()} }

func TestCachedFS_CachesList(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a"), "")

	inner := newCountingFS()
	c := NewCachedFS(inner)

	for i := 0; i < 5; i++ {
		entries, err := c.List(dir)
		if err != nil {
			t.Fatalf("List #%d: %v", i, err)
		}
		if len(entries) != 1 {
			t.Errorf("List size: got %d, want 1", len(entries))
		}
	}
	if got := inner.listCalls.Load(); got != 1 {
		t.Errorf("inner.List called %d times, want 1 (cached)", got)
	}
}

func TestCachedFS_InvalidateForcesReload(t *testing.T) {
	dir := t.TempDir()
	inner := newCountingFS()
	c := NewCachedFS(inner)

	_, _ = c.List(dir)
	c.Invalidate(dir)
	_, _ = c.List(dir)

	if got := inner.listCalls.Load(); got != 2 {
		t.Errorf("after Invalidate, inner.List should run again: got %d, want 2", got)
	}
}

func TestCachedFS_InvalidateAll(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "a"))
	mustMkdir(t, filepath.Join(dir, "b"))

	inner := newCountingFS()
	c := NewCachedFS(inner)

	_, _ = c.List(filepath.Join(dir, "a"))
	_, _ = c.List(filepath.Join(dir, "b"))
	c.InvalidateAll()
	_, _ = c.List(filepath.Join(dir, "a"))
	_, _ = c.List(filepath.Join(dir, "b"))

	if got := inner.listCalls.Load(); got != 4 {
		t.Errorf("after InvalidateAll both entries should miss: got %d, want 4", got)
	}
}

func TestCachedFS_MutationsInvalidateParent(t *testing.T) {
	dir := t.TempDir()
	inner := newCountingFS()
	c := NewCachedFS(inner)

	// Prime the cache for `dir`.
	if _, err := c.List(dir); err != nil {
		t.Fatalf("List prime: %v", err)
	}
	before := inner.listCalls.Load()

	// Mkdir invalidates its parent — next List should miss.
	if err := c.Mkdir(filepath.Join(dir, "new")); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := c.List(dir); err != nil {
		t.Fatalf("List after Mkdir: %v", err)
	}
	if got := inner.listCalls.Load(); got <= before {
		t.Errorf("Mkdir should have invalidated parent; calls=%d (before=%d)", got, before)
	}

	// Same for Remove.
	before = inner.listCalls.Load()
	target := filepath.Join(dir, "new")
	if err := c.Remove(target); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := c.List(dir); err != nil {
		t.Fatalf("List after Remove: %v", err)
	}
	if got := inner.listCalls.Load(); got <= before {
		t.Errorf("Remove should have invalidated parent; calls=%d (before=%d)", got, before)
	}

	// Same for Rename — both src and dst parents invalidated.
	src := filepath.Join(dir, "x")
	dst := filepath.Join(dir, "y")
	mustWrite(t, src, "")
	c.Invalidate(dir)
	_, _ = c.List(dir)
	before = inner.listCalls.Load()
	if err := c.Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	_, _ = c.List(dir)
	if got := inner.listCalls.Load(); got <= before {
		t.Errorf("Rename should have invalidated parent; calls=%d (before=%d)", got, before)
	}
}

func TestCachedFS_PassthroughMetadata(t *testing.T) {
	inner := newCountingFS()
	c := NewCachedFS(inner)
	if c.Kind() != inner.Kind() {
		t.Errorf("Kind passthrough broken")
	}
	if c.Home() != inner.Home() {
		t.Errorf("Home passthrough broken")
	}
	if c.Inner() != inner {
		t.Errorf("Inner() should return the wrapped FS")
	}
}

// --- helpers ----------------------------------------------------------------

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// sortNames is a convenience for tests that compare lists irrespective of FS
// ordering — currently unused but handy when extending the table.
var _ = sort.Strings
