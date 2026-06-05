package ui

import (
	"path/filepath"
	"testing"
	"time"

	sftpclient "github.com/art-ps/sftpcommander/internal/sftp"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCompileFilter_Empty(t *testing.T) {
	m := compileFilter("")
	for _, name := range []string{"foo", "BAR", ""} {
		if !m(name) {
			t.Errorf("empty filter should match %q", name)
		}
	}
}

func TestCompileFilter_Substring(t *testing.T) {
	m := compileFilter("log")
	cases := map[string]bool{
		"access.log":    true,
		"error_LOG.txt": true,
		"README.md":     false,
		"":              false,
	}
	for name, want := range cases {
		if got := m(name); got != want {
			t.Errorf("substring %q: got %v, want %v", name, got, want)
		}
	}
}

func TestCompileFilter_Glob(t *testing.T) {
	m := compileFilter("*.go")
	cases := map[string]bool{
		"main.go":      true,
		"NESTED.GO":    true, // case-insensitive
		"main.go.bak":  false,
		"go":           false,
		"sub/main.go":  false, // path.Match with single segment
	}
	for name, want := range cases {
		if got := m(name); got != want {
			t.Errorf("glob *.go on %q: got %v, want %v", name, got, want)
		}
	}
}

func TestCompileFilter_GlobInvalidFallsBackToSubstring(t *testing.T) {
	// Unclosed bracket — path.Match returns ErrBadPattern; fallback is substring.
	m := compileFilter("[abc")
	if !m("xx[abcyy") {
		t.Errorf("invalid glob should fall back to substring search")
	}
}

func TestCompileFilter_Regex(t *testing.T) {
	m := compileFilter("re:^foo")
	if !m("foobar") {
		t.Errorf("re:^foo should match foobar")
	}
	if m("barfoo") {
		t.Errorf("re:^foo should not match barfoo")
	}
}

func TestCompileFilter_RegexInvalidFallsBackToSubstring(t *testing.T) {
	m := compileFilter("re:[unclosed")
	// Fallback uses the full raw string (with the "re:" prefix) as substring.
	if !m("xx re:[unclosed yy") {
		t.Errorf("invalid regex should fall back to substring of full input")
	}
}

func TestSortEntries_NameAsc(t *testing.T) {
	in := []sftpclient.FileEntry{
		{Name: "Zeta", IsDir: false},
		{Name: "alpha", IsDir: false},
		{Name: "Beta", IsDir: true},
	}
	sortEntries(in, sortByName, false)
	want := []string{"Beta", "alpha", "Zeta"}
	for i, w := range want {
		if in[i].Name != w {
			t.Errorf("at %d: got %q, want %q", i, in[i].Name, w)
		}
	}
}

func TestSortEntries_NameDesc(t *testing.T) {
	in := []sftpclient.FileEntry{
		{Name: "a"}, {Name: "c"}, {Name: "b"},
	}
	sortEntries(in, sortByName, true)
	want := []string{"c", "b", "a"}
	for i, w := range want {
		if in[i].Name != w {
			t.Errorf("at %d: got %q, want %q", i, in[i].Name, w)
		}
	}
}

func TestSortEntries_DirsAlwaysFirstRegardlessOfMode(t *testing.T) {
	now := time.Now()
	in := []sftpclient.FileEntry{
		{Name: "bigfile.bin", IsDir: false, Size: 1 << 30, ModTime: now},
		{Name: "tinydir", IsDir: true, Size: 0, ModTime: now.Add(-time.Hour)},
	}
	for _, mode := range []sortMode{sortByName, sortBySize, sortByMTime} {
		cp := make([]sftpclient.FileEntry, len(in))
		copy(cp, in)
		sortEntries(cp, mode, false)
		if !cp[0].IsDir {
			t.Errorf("mode %v: dirs should sort first, got %+v", mode, cp[0])
		}
	}
}

func TestSortEntries_BySize(t *testing.T) {
	in := []sftpclient.FileEntry{
		{Name: "big", Size: 1000},
		{Name: "small", Size: 10},
		{Name: "mid", Size: 100},
	}
	sortEntries(in, sortBySize, false)
	want := []int64{10, 100, 1000}
	for i, w := range want {
		if in[i].Size != w {
			t.Errorf("size sort asc at %d: got %d, want %d", i, in[i].Size, w)
		}
	}

	sortEntries(in, sortBySize, true)
	want = []int64{1000, 100, 10}
	for i, w := range want {
		if in[i].Size != w {
			t.Errorf("size sort desc at %d: got %d, want %d", i, in[i].Size, w)
		}
	}
}

func TestSortEntries_ByMTime(t *testing.T) {
	now := time.Now()
	in := []sftpclient.FileEntry{
		{Name: "newest", ModTime: now},
		{Name: "older", ModTime: now.Add(-2 * time.Hour)},
		{Name: "mid", ModTime: now.Add(-time.Hour)},
	}
	sortEntries(in, sortByMTime, false)
	want := []string{"older", "mid", "newest"}
	for i, w := range want {
		if in[i].Name != w {
			t.Errorf("mtime asc at %d: got %q, want %q", i, in[i].Name, w)
		}
	}
}

func TestSortMode_String(t *testing.T) {
	cases := map[sortMode]string{
		sortByName:  "name",
		sortBySize:  "size",
		sortByMTime: "mtime",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("sortMode(%d).String()=%q, want %q", int(m), got, want)
		}
	}
}

func TestEmptyDash(t *testing.T) {
	if got := emptyDash(""); got != "—" {
		t.Errorf("empty input: got %q, want em-dash", got)
	}
	if got := emptyDash("x"); got != "x" {
		t.Errorf("non-empty input: got %q, want x", got)
	}
}

// --- BrowserModel.Update -----------------------------------------------------

// browserWithEntries returns a BrowserModel pre-populated with the given
// entries as if entriesLoadedMsg had just delivered them. Useful for testing
// key-driven state transitions without a real FS.
func browserWithEntries(t *testing.T, names ...string) BrowserModel {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		mustWrite(t, filepath.Join(dir, n), "")
	}
	m := NewBrowserModel(NewLocalFS())
	m.height = 24
	m.width = 80
	updated, _ := m.Update(entriesLoadedMsg{path: dir, entries: snapshotLocal(t, dir), side: 0})
	return updated.(BrowserModel)
}

func snapshotLocal(t *testing.T, dir string) []sftpclient.FileEntry {
	t.Helper()
	entries, err := NewLocalFS().List(dir)
	if err != nil {
		t.Fatalf("snapshot List: %v", err)
	}
	return entries
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestBrowserUpdate_CursorMovement(t *testing.T) {
	m := browserWithEntries(t, "a", "b", "c")
	if m.cursor != 0 {
		t.Fatalf("initial cursor: %d, want 0", m.cursor)
	}
	m2, _ := m.Update(keyMsg("j"))
	m = m2.(BrowserModel)
	if m.cursor != 1 {
		t.Errorf("after j: cursor=%d, want 1", m.cursor)
	}
	m2, _ = m.Update(keyMsg("j"))
	m = m2.(BrowserModel)
	m2, _ = m.Update(keyMsg("j"))
	m = m2.(BrowserModel)
	// Already at last entry; further j should clamp.
	if m.cursor != 2 {
		t.Errorf("after 3x j on 3 entries: cursor=%d, want 2", m.cursor)
	}
	m2, _ = m.Update(keyMsg("k"))
	m = m2.(BrowserModel)
	if m.cursor != 1 {
		t.Errorf("after k: cursor=%d, want 1", m.cursor)
	}
}

func TestBrowserUpdate_TopAndBottom(t *testing.T) {
	m := browserWithEntries(t, "a", "b", "c", "d")
	m2, _ := m.Update(keyMsg("G"))
	m = m2.(BrowserModel)
	if m.cursor != 3 {
		t.Errorf("G should jump to last: cursor=%d, want 3", m.cursor)
	}
	m2, _ = m.Update(keyMsg("g"))
	m = m2.(BrowserModel)
	if m.cursor != 0 {
		t.Errorf("g should jump to first: cursor=%d", m.cursor)
	}
}

func TestBrowserUpdate_SortCycle(t *testing.T) {
	m := browserWithEntries(t, "a")
	if m.sortBy != sortByName {
		t.Fatalf("initial sortBy=%v, want sortByName", m.sortBy)
	}
	for _, want := range []sortMode{sortBySize, sortByMTime, sortByName} {
		m2, _ := m.Update(keyMsg("s"))
		m = m2.(BrowserModel)
		if m.sortBy != want {
			t.Errorf("after s: sortBy=%v, want %v", m.sortBy, want)
		}
	}
}

func TestBrowserUpdate_SortDirectionToggle(t *testing.T) {
	m := browserWithEntries(t, "a")
	if m.sortDesc {
		t.Fatal("initial sortDesc should be false")
	}
	m2, _ := m.Update(keyMsg("S"))
	m = m2.(BrowserModel)
	if !m.sortDesc {
		t.Errorf("after S: sortDesc=false, want true")
	}
	m2, _ = m.Update(keyMsg("S"))
	m = m2.(BrowserModel)
	if m.sortDesc {
		t.Errorf("after second S: sortDesc=true, want false")
	}
}

func TestBrowserUpdate_HiddenToggle(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "visible"), "")
	mustWrite(t, filepath.Join(dir, ".dot"), "")

	m := NewBrowserModel(NewLocalFS())
	m.height, m.width = 24, 80
	updated, _ := m.Update(entriesLoadedMsg{path: dir, entries: snapshotLocal(t, dir), side: 0})
	m = updated.(BrowserModel)

	if len(m.visible) != 1 || m.visible[0].Name != "visible" {
		t.Fatalf("hidden file should be filtered by default, got %v", names(m.visible))
	}
	m2, _ := m.Update(keyMsg("."))
	m = m2.(BrowserModel)
	if len(m.visible) != 2 {
		t.Errorf("after `.` toggle: visible=%d, want 2", len(m.visible))
	}
	m2, _ = m.Update(keyMsg("."))
	m = m2.(BrowserModel)
	if len(m.visible) != 1 {
		t.Errorf("after second `.` toggle: visible=%d, want 1", len(m.visible))
	}
}

func TestBrowserUpdate_SpaceTogglesSelectionAndAdvances(t *testing.T) {
	m := browserWithEntries(t, "a", "b", "c")

	m2, _ := m.Update(keyMsg("space"))
	m = m2.(BrowserModel)
	if len(m.selection) != 1 {
		t.Errorf("after space: selection size=%d, want 1", len(m.selection))
	}
	if m.cursor != 1 {
		t.Errorf("space should advance cursor: cursor=%d, want 1", m.cursor)
	}
}

func TestBrowserUpdate_SelectAllThenClear(t *testing.T) {
	m := browserWithEntries(t, "a", "b", "c")

	m2, _ := m.Update(keyMsg("a"))
	m = m2.(BrowserModel)
	if len(m.selection) != 3 {
		t.Errorf("`a` should select all visible, got %d", len(m.selection))
	}
	m2, _ = m.Update(keyMsg("a"))
	m = m2.(BrowserModel)
	if len(m.selection) != 0 {
		t.Errorf("second `a` should clear selection, got %d", len(m.selection))
	}
}

func TestBrowserUpdate_FilterModeEntered(t *testing.T) {
	m := browserWithEntries(t, "a")
	if m.mode != modeBrowse {
		t.Fatalf("initial mode=%v, want modeBrowse", m.mode)
	}
	m2, _ := m.Update(keyMsg("/"))
	m = m2.(BrowserModel)
	if m.mode != modeFilter {
		t.Errorf("`/` should enter filter mode, got %v", m.mode)
	}
}

func TestBrowserUpdate_EscClearsSelection(t *testing.T) {
	m := browserWithEntries(t, "a", "b")
	m2, _ := m.Update(keyMsg("a"))
	m = m2.(BrowserModel)
	if len(m.selection) == 0 {
		t.Fatal("selection should be non-empty after `a`")
	}
	m2, _ = m.Update(keyMsg("esc"))
	m = m2.(BrowserModel)
	if len(m.selection) != 0 {
		t.Errorf("esc should clear selection, got %d", len(m.selection))
	}
}

func TestBrowserUpdate_IgnoresOtherSideMsgs(t *testing.T) {
	// A two-pane left-side browser must drop messages tagged for side=1.
	m := NewBrowserModelSide(NewLocalFS(), 0)
	m.twoPane = true
	m.height, m.width = 24, 80

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "x"), "")
	updated, _ := m.Update(entriesLoadedMsg{path: dir, entries: snapshotLocal(t, dir), side: 1})
	m = updated.(BrowserModel)
	if len(m.entries) != 0 {
		t.Errorf("entriesLoadedMsg for the other side should be ignored, got %d entries", len(m.entries))
	}
}

func names(entries []sftpclient.FileEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}
