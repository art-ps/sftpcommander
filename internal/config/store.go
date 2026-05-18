// Package config persists saved connections and bookmarks to TOML files under
// ~/.config/sftpbrowser/. Both stores share the same load-modify-save pattern.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Connection struct {
	Name     string `toml:"name"`
	Host     string `toml:"host"`
	Port     string `toml:"port"`
	User     string `toml:"user"`
	KeyPath  string `toml:"key_path,omitempty"`
}

type connectionsFile struct {
	Connection []Connection `toml:"connection"`
}

type Bookmark struct {
	Host  string `toml:"host"`
	User  string `toml:"user"`
	Path  string `toml:"path"`
	Label string `toml:"label,omitempty"`
}

type bookmarksFile struct {
	Bookmark []Bookmark `toml:"bookmark"`
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "sftpbrowser")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func connectionsPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "connections.toml"), nil
}

func bookmarksPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bookmarks.toml"), nil
}

func LoadConnections() ([]Connection, error) {
	p, err := connectionsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f connectionsFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Connection, nil
}

func SaveConnections(conns []Connection) error {
	p, err := connectionsPath()
	if err != nil {
		return err
	}
	return writeTOML(p, connectionsFile{Connection: conns})
}

func AddConnection(c Connection) error {
	existing, err := LoadConnections()
	if err != nil {
		return err
	}
	// Replace by name when one exists; otherwise append.
	for i, e := range existing {
		if e.Name == c.Name {
			existing[i] = c
			return SaveConnections(existing)
		}
	}
	return SaveConnections(append(existing, c))
}

func DeleteConnection(name string) error {
	existing, err := LoadConnections()
	if err != nil {
		return err
	}
	out := existing[:0]
	for _, e := range existing {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return SaveConnections(out)
}

func LoadBookmarks() ([]Bookmark, error) {
	p, err := bookmarksPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f bookmarksFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Bookmark, nil
}

func SaveBookmarks(b []Bookmark) error {
	p, err := bookmarksPath()
	if err != nil {
		return err
	}
	return writeTOML(p, bookmarksFile{Bookmark: b})
}

// BookmarksForHost returns bookmarks whose host+user match. Empty host/user
// matches everything (useful before a connection is set up).
func BookmarksForHost(host, user string) ([]Bookmark, error) {
	all, err := LoadBookmarks()
	if err != nil {
		return nil, err
	}
	if host == "" && user == "" {
		return all, nil
	}
	out := make([]Bookmark, 0, len(all))
	for _, b := range all {
		if (host == "" || b.Host == host) && (user == "" || b.User == user) {
			out = append(out, b)
		}
	}
	return out, nil
}

func AddBookmark(b Bookmark) error {
	existing, err := LoadBookmarks()
	if err != nil {
		return err
	}
	// Dedupe by host+user+path.
	for _, e := range existing {
		if e.Host == b.Host && e.User == b.User && e.Path == b.Path {
			return nil
		}
	}
	return SaveBookmarks(append(existing, b))
}

func DeleteBookmark(host, user, p string) error {
	existing, err := LoadBookmarks()
	if err != nil {
		return err
	}
	out := existing[:0]
	for _, e := range existing {
		if !(e.Host == host && e.User == user && e.Path == p) {
			out = append(out, e)
		}
	}
	return SaveBookmarks(out)
}

func writeTOML(p string, v any) error {
	f, err := os.OpenFile(p, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(v)
}
