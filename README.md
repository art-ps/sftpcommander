# sftpbrowser

A terminal SFTP client with a keyboard-driven TUI. Browse, transfer, and
manage remote files without leaving the shell.

Built on [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lipgloss](https://github.com/charmbracelet/lipgloss), and
[pkg/sftp](https://github.com/pkg/sftp).

## Features

- **Saved connections + SSH config** — start screen lists `~/.config/sftpbrowser/connections.toml` entries and `~/.ssh/config` Host aliases side by side. Successful connections are auto-saved (dedup by `user@host`).
- **Secure auth chain** — ssh-agent (via `SSH_AUTH_SOCK`) -> private key (with passphrase prompt for encrypted keys) -> password.
- **Host key verification** — `~/.ssh/known_hosts` enforced. First-time hosts trigger a TOFU prompt with fingerprint; changed keys hard-fail with a MITM warning. Key-algorithm rotation (e.g. RSA -> Ed25519) is detected and silently negotiated.
- **Parallel downloads** — 4-worker pool flattens directory trees into a task queue; many small files transfer in parallel, large files use concurrent reads via `sftp.UseConcurrentReads`.
- **Upload with progress** — file or directory, with the same fail-skip-abort prompt as downloads.
- **Remote file operations** — delete (recursive, with confirm), rename, mkdir, chmod (octal).
- **Text preview** — viewport renders the first 256 KB of any file, binary detection avoids garbage on the screen.
- **Filtering and sorting** — live substring filter (`/`), three sort modes (name/size/mtime), asc/desc toggle, hidden-files toggle.
- **Bookmarks** — per-host path bookmarks stored in `~/.config/sftpbrowser/bookmarks.toml`.
- **Cursor memory** — jumping into a subdirectory and back puts the cursor where it was.
- **Multi-select** — space to select, `a` to select all, then `d`/`D` operate on the whole selection.
- **Two-pane mode (mc-style)** — `T` opens a local-FS panel next to the remote one. `Tab` switches focus, `F5` copies selected entries to the other panel (download or upload, picked automatically by direction).

## Install

Requires Go 1.25 or newer.

```sh
git clone https://github.com/USER/sftpbrowser.git
cd sftpbrowser
go build -o sftpbrowser .
```

Or install directly:

```sh
go install ./...
```

The resulting binary is a single self-contained executable.

## Usage

```sh
./sftpbrowser
```

The app opens on the connection list. Pick a saved entry or `n` for a new
connection. After connecting, you land in the browser at the remote home
directory.

### Keyboard shortcuts (browser)

| Key | Action |
|---|---|
| `up`/`down`, `j`/`k` | Move cursor |
| `pgup`/`pgdn` | Page |
| `g` / `G` | Top / bottom |
| `enter`, `l`, `right` | Open directory or preview file |
| `h`, `left`, `backspace` | Parent directory |
| `R` | Refresh current directory |
| `space` | Toggle select on cursor |
| `a` | Select / clear all |
| `esc` | Clear selection |
| `d` | Download selection (or cursor) |
| `u` | Upload from local path |
| `v` | Preview text file |
| `D` | Delete (with confirm; recursive for directories) |
| `r` | Rename |
| `M` | Make directory |
| `c` | Change mode (octal) |
| `/` | Filter directory |
| `.` | Toggle hidden files |
| `s` / `S` | Cycle sort mode / toggle direction |
| `b` | Bookmark current path |
| `B` | Open bookmarks list |
| `T` | Open two-pane (mc-style) mode |
| `?` | Help overlay |
| `q` | Quit |

### Keyboard shortcuts (two-pane mode)

| Key | Action |
|---|---|
| `tab` | Switch active panel |
| `F5` | Copy selection from active panel to the other panel (auto download/upload) |
| `F2`, `Ctrl-W` | Back to single-pane browser |
| `q` | Quit |

All single-panel shortcuts (`/`, `s`, `D`, `r`, `M`, `c`, `v`, `?`, etc.) also work inside the focused panel. Transfer keys `d`/`u` and bookmark keys `b`/`B` are disabled on the local panel.

### Keyboard shortcuts (other screens)

- **Connections list**: `enter` to connect, `n` for a new form, `D` to delete a saved entry, `R` to reload.
- **Connect form**: `tab`/`shift+tab` to move between fields, `enter` to advance or submit, `esc` back to the list.
- **Preview**: `up`/`down`/`pgup`/`pgdn` to scroll, `esc` back.
- **Bookmarks list**: `enter` to navigate, `D` to delete, `esc` back.

## Configuration

All state lives under `~/.config/sftpbrowser/`. The format is TOML; files are
created on first write with mode `0600`.

### `connections.toml`

```toml
[[connection]]
name     = "deploy@prod.example.com"
host     = "prod.example.com"
port     = "22"
user     = "deploy"
key_path = "~/.ssh/id_ed25519"
```

Passwords and passphrases are never persisted.

### `bookmarks.toml`

```toml
[[bookmark]]
host  = "prod.example.com"
user  = "deploy"
path  = "/var/log/app"
label = "app logs"
```

Bookmarks are scoped by `host` + `user`, so the bookmarks list only shows
entries relevant to the current session.

### SSH config

The app reads `~/.ssh/config` via `kevinburke/ssh_config`, which understands
`Include`, wildcard Host patterns, and `Match`. Aliases declared there appear
in the connection list tagged `[ssh]`. For each alias the app honours:

- `HostName`
- `Port`
- `User`
- `IdentityFile`

Other directives (e.g. `ProxyJump`, `IdentitiesOnly`) are not yet wired in.

### Known hosts

`~/.ssh/known_hosts` is the source of truth for host key verification:

- Unknown host: prompt with the SHA256 fingerprint. On accept the key is
  appended (one line per `hostname,ip`).
- Known host, same key: silent.
- Known host, same algorithm, different key: hard fail with a MITM warning.
  Resolution is manual editing of `~/.ssh/known_hosts`.
- Known host, different algorithm: silently retry with
  `HostKeyAlgorithms` restricted to algorithms already on file, so an
  RSA -> Ed25519 rotation does not trip the MITM path.

## Build from source

```sh
go mod tidy
go build -o sftpbrowser .
go vet ./...
```

## Project layout

```
.
├── main.go
├── internal/
│   ├── sftp/         # SSH dial, known_hosts, file ops, parallel batch download
│   ├── ui/           # Bubble Tea models (connect, browser, download, upload, preview, etc.)
│   └── config/       # TOML store for connections and bookmarks
└── go.mod
```

## Roadmap

Not yet implemented, listed in rough priority:

- `ProxyJump` support
- ReadDir cache with `R`-keyed invalidation
- Adaptive theme for light terminals
- Mouse support
- NerdFont fallback for the file-icon glyphs
- Move (`F6`) and parallel uploads in the worker pool

## License

[MIT](LICENSE) — free for any use, including commercial, with attribution.
