package sftp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kevinburke/ssh_config"
)

type SSHConfigEntry struct {
	HostName     string
	Port         string
	User         string
	IdentityFile string
}

// LookupSSHConfig parses ~/.ssh/config and returns the directives that apply
// to the given alias. The Config-level Get walks Host blocks (including
// wildcard patterns) and resolves Include directives, but does NOT substitute
// OpenSSH built-in defaults — so an empty field means "not set in the user's
// config", which is what the UI uses to decide whether to fill a form field.
// ListSSHHosts returns concrete Host aliases declared in ~/.ssh/config.
// Wildcards and the catch-all "*" pattern are skipped because they aren't
// useful as connection targets on their own.
func ListSSHHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, h := range cfg.Hosts {
		for _, p := range h.Patterns {
			s := p.String()
			if s == "" || s == "*" || strings.ContainsAny(s, "*?!") {
				continue
			}
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func LookupSSHConfig(alias string) SSHConfigEntry {
	home, _ := os.UserHomeDir()

	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return SSHConfigEntry{}
	}
	defer f.Close()

	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return SSHConfigEntry{}
	}

	get := func(key string) string {
		v, _ := cfg.Get(alias, key)
		return v
	}

	identity := get("IdentityFile")
	if home != "" && strings.HasPrefix(identity, "~/") {
		identity = filepath.Join(home, identity[2:])
	}

	return SSHConfigEntry{
		HostName:     get("Hostname"),
		Port:         get("Port"),
		User:         get("User"),
		IdentityFile: identity,
	}
}
