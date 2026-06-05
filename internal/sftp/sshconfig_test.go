package sftp

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// withSSHConfig stages a temporary $HOME with a .ssh/config containing the
// given body, so LookupSSHConfig/ListSSHHosts read from that file instead of
// the developer's real home.
func withSSHConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return home
}

func TestLookupSSHConfig_ExactHost(t *testing.T) {
	withSSHConfig(t, `
Host myserver
    HostName 10.0.0.1
    User deploy
    Port 2222
    IdentityFile ~/.ssh/id_special
`)
	got := LookupSSHConfig("myserver")
	if got.HostName != "10.0.0.1" {
		t.Errorf("HostName=%q, want 10.0.0.1", got.HostName)
	}
	if got.User != "deploy" {
		t.Errorf("User=%q, want deploy", got.User)
	}
	if got.Port != "2222" {
		t.Errorf("Port=%q, want 2222", got.Port)
	}
	// IdentityFile with ~/ should be expanded to absolute path.
	want, _ := os.UserHomeDir()
	want = filepath.Join(want, ".ssh", "id_special")
	if got.IdentityFile != want {
		t.Errorf("IdentityFile=%q, want %q", got.IdentityFile, want)
	}
}

func TestLookupSSHConfig_ProxyJump(t *testing.T) {
	withSSHConfig(t, `
Host inner
    HostName inner.example.com
    User app
    ProxyJump jumpbox
`)
	got := LookupSSHConfig("inner")
	if got.ProxyJump != "jumpbox" {
		t.Errorf("ProxyJump=%q, want jumpbox", got.ProxyJump)
	}
}

func TestLookupSSHConfig_UnknownAliasReturnsEmpty(t *testing.T) {
	withSSHConfig(t, `
Host real
    HostName real.example.com
`)
	got := LookupSSHConfig("ghost")
	// The library has no defaults — unknown alias means no Host block matched,
	// so HostName/User/Port should be empty (not "ghost", not "22").
	if got.HostName != "" {
		t.Errorf("HostName for unknown alias should be empty, got %q", got.HostName)
	}
	if got.Port != "" {
		t.Errorf("Port for unknown alias should be empty, got %q", got.Port)
	}
}

func TestLookupSSHConfig_NoConfigFileReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := LookupSSHConfig("anything")
	if (got != SSHConfigEntry{}) {
		t.Errorf("no config should return zero-value entry, got %+v", got)
	}
}

func TestListSSHHosts_SkipsWildcardsAndCatchAll(t *testing.T) {
	withSSHConfig(t, `
Host bastion
    HostName bastion.example.com

Host *.dev
    User dev

Host *
    ServerAliveInterval 60

Host prod
    HostName prod.example.com
`)
	got := ListSSHHosts()
	sort.Strings(got)
	want := []string{"bastion", "prod"}
	if len(got) != len(want) {
		t.Fatalf("got %d hosts (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("at %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestListSSHHosts_DedupesRepeatedAliases(t *testing.T) {
	withSSHConfig(t, `
Host shared
    HostName a.example.com

Host shared
    User other
`)
	got := ListSSHHosts()
	count := 0
	for _, h := range got {
		if h == "shared" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'shared' once, got %d (full list: %v)", count, got)
	}
}
