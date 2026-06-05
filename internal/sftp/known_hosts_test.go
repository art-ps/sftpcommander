package sftp

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestStripPort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"example.com:22", "example.com"},
		{"192.0.2.1:2222", "192.0.2.1"},
		{"[::1]:22", "::1"},
		{"[2001:db8::1]:2222", "2001:db8::1"},
		// No port — net.SplitHostPort fails, stripPort returns the input.
		{"example.com", "example.com"},
		{"::1", "::1"},
	}
	for _, tc := range cases {
		got := stripPort(tc.in)
		if got != tc.want {
			t.Errorf("stripPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAppendKnownHostIPv6 exercises the path that constructs a known_hosts
// entry for an IPv6 host. The interesting bit is that knownhosts.Normalize
// must produce the bracketed `[::1]:2222` form for non-22 ports — a bug there
// would make future connections appear as "unknown host" again.
func TestAppendKnownHostIPv6(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(mustEd25519Priv(t))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	_ = pub
	pubKey := signer.PublicKey()

	cases := []struct {
		name     string
		hostname string
		address  string
		// substrings expected somewhere in the known_hosts line
		wantContains []string
	}{
		{
			name:         "ipv6 default port",
			hostname:     "::1",
			address:      "[::1]:22",
			wantContains: []string{"::1"},
		},
		{
			name:         "ipv6 nonstandard port",
			hostname:     "::1",
			address:      "[::1]:2222",
			wantContains: []string{"[::1]:2222"},
		},
		{
			name:         "ipv6 hostname-only fallback",
			hostname:     "2001:db8::1",
			address:      "[2001:db8::1]:22",
			wantContains: []string{"2001:db8::1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each subtest writes into its own known_hosts file to avoid
			// cross-pollution.
			sshDir := filepath.Join(tmp, tc.name, ".ssh")
			if err := os.MkdirAll(sshDir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			t.Setenv("HOME", filepath.Join(tmp, tc.name))

			ch := &UnknownHostKeyError{
				Hostname: tc.hostname,
				Address:  tc.address,
				key:      pubKey,
			}
			if err := AppendKnownHost(ch); err != nil {
				t.Fatalf("AppendKnownHost: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(sshDir, "known_hosts"))
			if err != nil {
				t.Fatalf("read known_hosts: %v", err)
			}
			got := string(data)
			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("known_hosts missing %q\nfull:\n%s", sub, got)
				}
			}
		})
	}
}

func mustEd25519Priv(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return priv
}
