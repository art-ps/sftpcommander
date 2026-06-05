# sftpcommander

A terminal SFTP client with a keyboard-driven TUI. Browse, transfer, and
manage remote files without leaving the shell.

Built on [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lipgloss](https://github.com/charmbracelet/lipgloss),
[pkg/sftp](https://github.com/pkg/sftp), and
[chroma](https://github.com/alecthomas/chroma).

## Features

- **Saved connections + SSH config** — start screen lists `~/.config/sftpcommander/connections.toml` entries and `~/.ssh/config` Host aliases side by side. Successful connections are auto-saved (dedup by `user@host`).
- **Secure auth chain** — ssh-agent (via `SSH_AUTH_SOCK`) -> private key (with passphrase prompt for encrypted keys) -> password.
- **ProxyJump** — single-hop `ProxyJump` from `~/.ssh/config` is honoured; the jump host authenticates via ssh-agent and/or its declared `IdentityFile`.
- **Host key verification** — `~/.ssh/known_hosts` enforced. First-time hosts trigger a TOFU prompt with fingerprint; changed keys hard-fail with a MITM warning. Key-algorithm rotation (e.g. RSA -> Ed25519) is detected and silently negotiated.
- **Parallel transfers** — worker pool (default 4, cap 32) flattens directory trees into a flat task queue for both downloads and uploads; many small files transfer in parallel, large files use concurrent reads via `sftp.UseConcurrentReads`. Workers share a pooled set of SFTP sessions so reuse is free across batches.
- **Parallel scan** — directory walk during transfer pre-scan also runs in a worker pool, so deep trees stop blocking on the first slow `ReadDir`.
- **Overwrite prompt** — per-file `replace / skip / resume / abort` decision when the destination already exists, with sticky "all" variants to apply the same choice to the rest of the batch.
- **Optional SHA256 verification** — `Verify` mode hashes each file post-transfer (`sha256sum` over an ssh session) and surfaces mismatches as failures.
- **Cross-pane copy + move** — `F5` copies the selected entries to the other panel (download or upload, picked automatically by direction). `F6` moves entries when both panels share the same FS.
- **Remote-side copy** — `F5` in single-pane runs `cp -R` over an ssh session, so the bytes never round-trip through the client.
- **Recursive find** — `F` walks the current subtree and replaces the listing with matches; entry names show paths relative to the search root. Patterns share the filter syntax (substring, glob, or `re:` prefix).
- **External editor** — `e` downloads the remote file to a temp path, opens `$EDITOR`, and re-uploads on save (mtime-checked, so closing without changes is a no-op).
- **Text preview** — first 256 KB rendered with chroma syntax highlighting (lexer chosen from filename), line numbers, and binary detection. Press `m` to load the next 256 KB on demand; the status bar shows how much is buffered.
- **Symlink targets** — symlinks in the listing show `→ target` in the status bar (resolved async, cached per session). Preview pane shows the target next to the path.
- **Directory summary** — when no selection is active, status bar shows `N dirs, M files, X MB` totals for the current view.
- **Filtering and sorting** — live substring filter (`/`), three sort modes (name/size/mtime), asc/desc toggle, hidden-files toggle. Filter resets on `cd`. Sorted entries cached so cursor moves don't pay the sort cost.
- **Bookmarks** — per-host path bookmarks stored in `~/.config/sftpcommander/bookmarks.toml`.
- **Cursor memory** — jumping into a subdirectory and back puts the cursor where it was.
- **Multi-select** — space to select, `a` to select all, then `d`/`D` operate on the whole selection.
- **chmod (recursive)** — `c` changes the cursor entry, `C` applies recursively. `M` makes a new directory; `r` renames.
- **Two-pane mode (mc-style)** — `T` opens a local-FS panel next to the remote one. `Tab` switches focus, `F5`/`F6` copy/move across, `Ctrl-U` swaps panel contents, `=` aligns the inactive panel to the active path.
- **Error log** — `E` opens an in-app log of failures recorded across operations; survives panel switches so the user can review what went wrong after dismissing the inline error.

## Install

### Homebrew (macOS / Linux)

```sh
brew install art-ps/tap/sftpcommander
```

### Go install

Requires Go 1.25 or newer.

```sh
go install github.com/art-ps/sftpcommander@latest
```

### From source

```sh
git clone https://github.com/art-ps/sftpcommander.git
cd sftpcommander
go build -o sftpcommander .
```

The resulting binary is a single self-contained executable.

### Pre-built binaries

Download from the [releases page](https://github.com/art-ps/sftpcommander/releases) — archives for darwin/linux on amd64/arm64.

## Usage

```sh
sftpcommander
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
| `esc` | Clear selection / error / status |
| `d` | Download selection (or cursor) |
| `u` | Upload from local path |
| `e` | Edit remote file in `$EDITOR` |
| `v` | Preview text file |
| `D` | Delete (with confirm; recursive for directories) |
| `r` | Rename |
| `M` | Make directory |
| `c` / `C` | Change mode (octal; `C` is recursive) |
| `F5` | Remote-side copy (`cp -R` via ssh) |
| `/` | Filter directory |
| `F` | Recursive find under cwd |
| `.` | Toggle hidden files |
| `s` / `S` | Cycle sort mode / toggle direction |
| `b` | Bookmark current path |
| `B` | Open bookmarks list |
| `T` | Open two-pane (mc-style) mode |
| `E` | Open error log |
| `?` | Help overlay |
| `q` | Quit |

### Keyboard shortcuts (two-pane mode)

| Key | Action |
|---|---|
| `tab` | Switch active panel |
| `F5` | Copy selection from active panel to the other panel (auto download/upload) |
| `F6` | Move selection across panels (same-FS only — atomic rename) |
| `=` | Align inactive panel to the active panel's path |
| `Ctrl-U` | Swap panels |
| `F2`, `Ctrl-W` | Back to single-pane browser |
| `E` | Open error log |
| `q` | Quit |

All single-panel shortcuts (`/`, `s`, `D`, `r`, `M`, `c`, `v`, `?`, etc.) also work inside the focused panel. Transfer keys `d`/`u` and bookmark keys `b`/`B` are disabled on the local panel.

### Keyboard shortcuts (preview)

- `up`/`down`/`pgup`/`pgdn`, `g`/`G` to scroll.
- `m` to load the next 256 KB chunk.
- `esc`, `q`, `h`, `left`, `backspace` to return.

### Keyboard shortcuts (other screens)

- **Connections list**: `enter` to connect, `n` for a new form, `D` to delete a saved entry, `R` to reload.
- **Connect form**: `tab`/`shift+tab` to move between fields, `enter` to advance or submit, `esc` back to the list.
- **Bookmarks list**: `enter` to navigate, `D` to delete, `esc` back.
- **Error log**: `up`/`down` to scroll, `C` to clear, `esc`/`q` to close.

## Configuration

All state lives under `~/.config/sftpcommander/`. The format is TOML; files are
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
- `ProxyJump` (single hop; jump host authenticates via ssh-agent or its own
  `IdentityFile`)

Other directives (e.g. `IdentitiesOnly`) are not yet wired in.

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

### Display

- `SFTP_NO_NF=1` forces the ASCII file-icon fallback for terminals without a
  Nerd Font.

## Build from source

```sh
go mod tidy
go build -o sftpcommander .
go vet ./...
```

## Project layout

```
.
├── main.go
├── internal/
│   ├── sftp/         # SSH dial, known_hosts, ProxyJump, file ops, parallel batch transfer
│   ├── ui/           # Bubble Tea models (connect, browser, download, upload, preview, edit, errlog, etc.)
│   └── config/       # TOML store for connections and bookmarks
└── go.mod
```

## Roadmap

Not yet implemented, listed in rough priority:

- Multi-hop `ProxyJump` chains
- Adaptive theme for light terminals
- Mouse support
- Cross-FS move (currently F6 errors out unless both panels share the same FS)

## Releasing

Releases are built by [GoReleaser](https://goreleaser.com/) on tag push.
Setup (one-time):

1. Create GitHub repos `art-ps/sftpcommander` and `art-ps/homebrew-tap`.
2. In `art-ps/homebrew-tap`, create an empty `Formula/` directory (or just
   `git init` + first commit).
3. Generate a GitHub Personal Access Token with `repo` scope, save it as
   the `HOMEBREW_TAP_GITHUB_TOKEN` secret on `art-ps/sftpcommander`.
4. Push this repo: `git remote add origin git@github.com:art-ps/sftpcommander.git && git push -u origin main`.

To cut a release:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow builds darwin/linux × amd64/arm64 archives,
publishes a GitHub release, and pushes an updated formula to
`art-ps/homebrew-tap`. Users then install via:

```sh
brew install art-ps/tap/sftpcommander
```

## License

[MIT](LICENSE) — free for any use, including commercial, with attribution.
