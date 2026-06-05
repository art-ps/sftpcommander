package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempHome reroutes os.UserHomeDir to a temp directory for the duration of
// the test. configDir() reads HOME via os.UserHomeDir, so this isolates the
// test from the developer's real ~/.config/sftpcommander.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestConnections_LoadEmpty(t *testing.T) {
	withTempHome(t)
	got, err := LoadConnections()
	if err != nil {
		t.Fatalf("LoadConnections on empty home: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 connections, got %d", len(got))
	}
}

func TestConnections_SaveLoadRoundTrip(t *testing.T) {
	withTempHome(t)
	want := []Connection{
		{Name: "a@host1", Host: "host1", Port: "22", User: "a", KeyPath: "/k1"},
		{Name: "b@host2", Host: "host2", Port: "2222", User: "b"},
	}
	if err := SaveConnections(want); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	got, err := LoadConnections()
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestConnections_FileModeIs0600(t *testing.T) {
	home := withTempHome(t)
	if err := SaveConnections([]Connection{{Name: "x", Host: "h", User: "u"}}); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".config", "sftpcommander", "connections.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("expected mode 0600, got %#o", mode)
	}
}

func TestConnections_AddDedupesByName(t *testing.T) {
	withTempHome(t)

	if err := AddConnection(Connection{Name: "x@h", Host: "h", User: "x", Port: "22"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := AddConnection(Connection{Name: "y@h2", Host: "h2", User: "y", Port: "22"}); err != nil {
		t.Fatalf("second add: %v", err)
	}
	// Same name → in-place replace, not duplicate.
	if err := AddConnection(Connection{Name: "x@h", Host: "h-updated", User: "x", Port: "2200", KeyPath: "/k"}); err != nil {
		t.Fatalf("third add: %v", err)
	}

	got, err := LoadConnections()
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries after dedup, got %d", len(got))
	}
	if got[0].Host != "h-updated" || got[0].Port != "2200" || got[0].KeyPath != "/k" {
		t.Errorf("first entry not updated: %+v", got[0])
	}
}

func TestConnections_Delete(t *testing.T) {
	withTempHome(t)
	for _, c := range []Connection{
		{Name: "a", Host: "h1", User: "u"},
		{Name: "b", Host: "h2", User: "u"},
		{Name: "c", Host: "h3", User: "u"},
	} {
		if err := AddConnection(c); err != nil {
			t.Fatalf("AddConnection: %v", err)
		}
	}
	if err := DeleteConnection("b"); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	got, _ := LoadConnections()
	if len(got) != 2 {
		t.Fatalf("want 2 after delete, got %d", len(got))
	}
	for _, c := range got {
		if c.Name == "b" {
			t.Errorf("b should be deleted, still present")
		}
	}
}

func TestConnections_DeleteMissingIsNoop(t *testing.T) {
	withTempHome(t)
	_ = AddConnection(Connection{Name: "keep", Host: "h", User: "u"})
	if err := DeleteConnection("ghost"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	got, _ := LoadConnections()
	if len(got) != 1 || got[0].Name != "keep" {
		t.Errorf("delete-missing should not touch list, got %+v", got)
	}
}

func TestBookmarks_AddAndScope(t *testing.T) {
	withTempHome(t)
	for _, b := range []Bookmark{
		{Host: "h1", Port: "22", User: "a", Path: "/var/log"},
		{Host: "h1", Port: "22", User: "a", Path: "/etc"},
		{Host: "h1", Port: "22", User: "b", Path: "/home/b"},
		{Host: "h2", Port: "22", User: "a", Path: "/srv"},
	} {
		if err := AddBookmark(b); err != nil {
			t.Fatalf("AddBookmark: %v", err)
		}
	}

	got, err := BookmarksForHost("h1", "22", "a")
	if err != nil {
		t.Fatalf("BookmarksForHost: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 bookmarks for h1/a, got %d", len(got))
	}
	for _, b := range got {
		if b.User != "a" || b.Host != "h1" {
			t.Errorf("scope leak: %+v", b)
		}
	}
}

func TestBookmarks_EmptyHostUserReturnsAll(t *testing.T) {
	withTempHome(t)
	_ = AddBookmark(Bookmark{Host: "h1", User: "a", Path: "/a"})
	_ = AddBookmark(Bookmark{Host: "h2", User: "b", Path: "/b"})

	got, err := BookmarksForHost("", "", "")
	if err != nil {
		t.Fatalf("BookmarksForHost: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want all bookmarks when host+user empty, got %d", len(got))
	}
}

func TestBookmarks_EmptyStoredPortMatchesAnyQueriedPort(t *testing.T) {
	withTempHome(t)
	// Legacy entry with empty port.
	_ = AddBookmark(Bookmark{Host: "h", User: "u", Path: "/p"})
	got, err := BookmarksForHost("h", "22", "u")
	if err != nil {
		t.Fatalf("BookmarksForHost: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("legacy bookmark with empty port should match any port, got %d", len(got))
	}
}

func TestBookmarks_AddDedupes(t *testing.T) {
	withTempHome(t)
	b := Bookmark{Host: "h", Port: "22", User: "u", Path: "/p"}
	for i := 0; i < 3; i++ {
		if err := AddBookmark(b); err != nil {
			t.Fatalf("AddBookmark #%d: %v", i, err)
		}
	}
	got, _ := LoadBookmarks()
	if len(got) != 1 {
		t.Errorf("duplicate AddBookmark calls should dedupe, got %d", len(got))
	}
}

func TestBookmarks_Delete(t *testing.T) {
	withTempHome(t)
	_ = AddBookmark(Bookmark{Host: "h", Port: "22", User: "u", Path: "/keep"})
	_ = AddBookmark(Bookmark{Host: "h", Port: "22", User: "u", Path: "/drop"})

	if err := DeleteBookmark("h", "22", "u", "/drop"); err != nil {
		t.Fatalf("DeleteBookmark: %v", err)
	}
	got, _ := LoadBookmarks()
	if len(got) != 1 || got[0].Path != "/keep" {
		t.Errorf("delete mis-targeted, got %+v", got)
	}
}

func TestConnections_MalformedTomlReturnsError(t *testing.T) {
	home := withTempHome(t)
	dir := filepath.Join(home, ".config", "sftpcommander")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connections.toml"), []byte("not [valid toml = \"hmm"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	_, err := LoadConnections()
	if err == nil {
		t.Fatal("expected error on malformed TOML, got nil")
	}
	if !strings.Contains(err.Error(), "toml") && !strings.Contains(err.Error(), "expected") {
		t.Logf("error message: %v", err)
	}
}
