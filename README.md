<div align="center">

# SFTP Commander

A keyboard-driven terminal SFTP client. Browse, transfer, and manage remote
files without leaving the shell — with a Midnight-Commander-style two-pane
layout, parallel transfers, and full SSH-config integration.

[![CI](https://github.com/art-ps/sftpcommander/actions/workflows/ci.yml/badge.svg)](https://github.com/art-ps/sftpcommander/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/art-ps/sftpcommander?sort=semver)](https://github.com/art-ps/sftpcommander/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/art-ps/sftpcommander)](https://goreportcard.com/report/github.com/art-ps/sftpcommander)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/art-ps/sftpcommander)](go.mod)

</div>

<p align="center">
  <img src="docs/demo.gif" alt="sftpcommander demo" width="900">
</p>

## Table of contents

- [Highlights](#highlights)
- [Install](#install)
- [Quick start](#quick-start)
- [Features](#features)
- [Keyboard shortcuts](#keyboard-shortcuts)
- [Configuration](#configuration)
- [Development](#development)
- [Releasing](#releasing)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)
- [Acknowledgments](#acknowledgments)

## Highlights

- 🗂️ **Two-pane layout** — Midnight Commander-style remote/local browser, default view on connect.
- ⚡ **Parallel transfers** — worker pool (up to 32) with pooled SFTP sessions; concurrent reads for large files.
- 🔐 **Secure by default** — ssh-agent → key → password chain; strict `known_hosts` with TOFU prompt and MITM hard-fail.
- 🌐 **SSH config aware** — `~/.ssh/config` aliases + single-hop `ProxyJump`.
- 🎨 **Syntax-highlighted preview** — chroma-powered, lazy paging.
- ✏️ **External editor round-trip** — `e` opens remote file in `$EDITOR`, re-uploads on save.

## Install

### Homebrew (macOS / Linux)

```sh
brew install art-ps/tap/sftpcommander
```

### Go

```sh
go install github.com/art-ps/sftpcommander@latest
```

Requires Go 1.25 or newer.

### Pre-built binaries

Grab a tarball from the [releases page](https://github.com/art-ps/sftpcommander/releases) — darwin/linux × amd64/arm64.

### From source

```sh
git clone https://github.com/art-ps/sftpcommander.git
cd sftpcommander
go build -o sftpcommander .
```

## Quick start

```sh
sftpcommander
```

The app opens on the **connection list**, merging entries from
`~/.config/sftpcommander/connections.toml` and `~/.ssh/config` Host aliases.

- `n` — new connection form.
- `enter` — connect (auth via ssh-agent → key → password).
- After a successful connect, you land in **two-pane mode** (local on the
  left, remote on the right). `F2` switches to the classic single-pane view.

Successful connections are auto-saved (deduped by `user@host`).

## Features

<details>
<summary><strong>Connections &amp; auth</strong></summary>

- **Saved connections + SSH config** — `~/.config/sftpcommander/connections.toml` entries and `~/.ssh/config` Host aliases shown side by side.
- **Secure auth chain** — ssh-agent (`SSH_AUTH_SOCK`) → private key (passphrase-prompted for encrypted keys) → password.
- **ProxyJump** — single-hop `ProxyJump` from `~/.ssh/config`; jump host authenticates via ssh-agent and/or its declared `IdentityFile`.
- **Host key verification** — `~/.ssh/known_hosts` enforced. Unknown hosts get a TOFU prompt with the SHA256 fingerprint; changed keys hard-fail with a MITM warning. Key-algorithm rotation (e.g. RSA → Ed25519) is detected and silently negotiated.

</details>

<details>
<summary><strong>Transfers</strong></summary>

- **Parallel batch transfers** — worker pool (default 4, cap 32) flattens directory trees into a flat task queue. Many small files transfer in parallel; large files use concurrent reads (`sftp.UseConcurrentReads`). Workers share a pooled set of SFTP sessions so reuse is free across batches.
- **Parallel pre-scan** — directory walk during transfer also runs in a worker pool, so deep trees stop blocking on the first slow `ReadDir`.
- **Overwrite prompt** — per-file `replace / skip / resume / abort` decision when the destination exists, with sticky "all" variants to apply the same choice to the rest of the batch.
- **Optional SHA256 verification** — hashes each file post-transfer (`sha256sum` over an ssh session) and surfaces mismatches as failures.
- **Cross-pane copy + move** — `F5` copies, `F6` moves (same-FS only — atomic rename).
- **Remote-side copy** — in single-pane, `F5` runs `cp -R` over an ssh session, so bytes never round-trip through the client.

</details>

<details>
<summary><strong>Browsing</strong></summary>

- **Two-pane mode (mc-style)** — default after connect. `Tab` switches focus, `=` aligns the inactive panel, `Ctrl-U` swaps panels.
- **Recursive find** — `F` walks the cwd subtree and shows matches relative to the search root. Patterns: substring, glob, or `re:`-prefixed regex.
- **External editor** — `e` downloads the remote file to a temp path, opens `$EDITOR`, re-uploads on save (mtime-checked).
- **Syntax-highlighted preview** — first 256 KB rendered via chroma (lexer chosen from filename), line numbers, binary detection. `m` loads the next 256 KB chunk on demand.
- **Symlink targets** — symlinks show `→ target` in the status bar (resolved async, cached per session).
- **Directory summary** — when nothing is selected, status bar shows `N dirs, M files, X MB` totals.
- **Filter, sort, hidden toggle** — live substring filter `/`, three sort modes (name/size/mtime), asc/desc, hidden toggle. Filter resets on `cd`; sorted entries cached.
- **Bookmarks** — per-host path bookmarks (`b` add, `B` list).
- **Cursor memory** — jumping into a subdirectory and back puts the cursor where it was.
- **Multi-select** — `space` selects, `a` selects all; `d`/`D` operate on the whole selection.
- **chmod (recursive)** — `c` cursor entry, `C` recursive. `M` mkdir; `r` rename.
- **Error log** — `E` opens an in-app log of failures recorded across operations; survives panel switches.

</details>

## Keyboard shortcuts

<details>
<summary><strong>Browser (single-pane &amp; focused two-pane panel)</strong></summary>

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move cursor |
| `pgup`/`pgdn` | Page |
| `g` / `G` | Top / bottom |
| `enter`, `l`, `→` | Open directory or preview file |
| `h`, `←`, `backspace` | Parent directory |
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
| `F2` | Toggle two-pane / single-pane |
| `E` | Open error log |
| `?` | Help overlay |
| `q` | Quit |

</details>

<details>
<summary><strong>Two-pane mode</strong></summary>

| Key | Action |
|---|---|
| `tab` | Switch active panel |
| `F5` | Copy selection from active panel to the other panel (auto download/upload) |
| `F6` | Move selection across panels (same-FS only — atomic rename) |
| `=` | Align inactive panel to the active panel's path |
| `Ctrl-U` | Swap panels |
| `F2`, `Ctrl-W` | Switch to single-pane browser |
| `E` | Open error log |
| `q` | Quit |

All single-panel shortcuts (`/`, `s`, `D`, `r`, `M`, `c`, `v`, `?`, etc.) also
work inside the focused panel. Transfer keys `d`/`u` and bookmark keys
`b`/`B` are disabled on the local panel.

</details>

<details>
<summary><strong>Preview, connect form, bookmarks, error log</strong></summary>

**Preview:** `↑`/`↓`/`pgup`/`pgdn`, `g`/`G` scroll. `m` loads next 256 KB. `esc`/`q`/`h`/`←`/`backspace` return.

**Connections list:** `enter` connect, `n` new form, `D` delete saved entry, `R` reload.

**Connect form:** `tab`/`shift+tab` move between fields, `enter` advance/submit, `esc` back.

**Bookmarks list:** `enter` navigate, `D` delete, `esc` back.

**Error log:** `↑`/`↓` scroll, `C` clear, `esc`/`q` close.

</details>

## Configuration

All state lives under `~/.config/sftpcommander/`. Files are TOML, mode `0600`,
created on first write.

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

`~/.ssh/config` is parsed via [`kevinburke/ssh_config`](https://github.com/kevinburke/ssh_config),
which understands `Include`, wildcard Host patterns, and `Match`. Aliases
appear in the connection list tagged `[ssh]`. Honoured directives:

- `HostName`
- `Port`
- `User`
- `IdentityFile`
- `ProxyJump` (single hop)

Other directives (e.g. `IdentitiesOnly`) are not yet wired in.

### Known hosts

`~/.ssh/known_hosts` is the source of truth:

- **Unknown host** — prompt with SHA256 fingerprint. On accept the key is appended (one line per `hostname,ip`).
- **Known host, same key** — silent.
- **Known host, same algorithm, different key** — hard fail with MITM warning. Resolution is manual edit of `~/.ssh/known_hosts`.
- **Known host, different algorithm** — silently retry with `HostKeyAlgorithms` restricted to algorithms already on file, so an RSA → Ed25519 rotation does not trip the MITM path.

### Environment

| Variable | Effect |
|---|---|
| `EDITOR` | Editor invoked by `e`. |
| `SFTP_NO_NF=1` | Force ASCII file-icon fallback for terminals without a Nerd Font. |
| `SSH_AUTH_SOCK` | Used for ssh-agent auth (set by your agent). |

## Development

```sh
go mod tidy
go build -o sftpcommander .
go vet ./...
go test ./...
```

### Project layout

```
.
├── main.go
├── internal/
│   ├── sftp/         # SSH dial, known_hosts, ProxyJump, file ops, parallel batch transfer
│   ├── ui/           # Bubble Tea models (connect, browser, download, upload, preview, edit, errlog, etc.)
│   └── config/       # TOML store for connections and bookmarks
├── .goreleaser.yaml  # cross-build + brew tap publishing
└── go.mod
```

## Releasing

Releases are built by [GoReleaser](https://goreleaser.com/) on tag push.

```sh
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow builds darwin/linux × amd64/arm64 archives, publishes
a GitHub release, and pushes an updated formula to
[`art-ps/homebrew-tap`](https://github.com/art-ps/homebrew-tap).

One-time setup (already done for this repo):

1. Create the GitHub repos `art-ps/sftpcommander` and `art-ps/homebrew-tap`.
2. In `art-ps/homebrew-tap`, create an empty `Formula/` directory.
3. Generate a Personal Access Token with `repo` scope, save it as the
   `HOMEBREW_TAP_GITHUB_TOKEN` secret on `art-ps/sftpcommander`.

## Roadmap

- [ ] Multi-hop `ProxyJump` chains
- [ ] Adaptive theme for light terminals
- [ ] Mouse support
- [ ] Cross-FS move (currently `F6` errors out unless both panels share the same FS)

## Contributing

Issues and PRs welcome. Before opening a PR:

1. `go vet ./...` clean.
2. `go test ./...` passing.
3. Keep changes focused — a one-feature-per-PR policy makes review easier.
4. Match the existing style; this codebase favours small, composable Bubble Tea models.

Bug reports: open an [issue](https://github.com/art-ps/sftpcommander/issues)
with the OS, terminal, Go version, and steps to reproduce.

## License

[MIT](LICENSE) — free for any use, including commercial, with attribution.

## Acknowledgments

Built on the shoulders of:

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — styling
- [pkg/sftp](https://github.com/pkg/sftp) — SFTP client
- [chroma](https://github.com/alecthomas/chroma) — syntax highlighting
- [kevinburke/ssh_config](https://github.com/kevinburke/ssh_config) — SSH config parsing
- [GoReleaser](https://goreleaser.com/) — release automation
