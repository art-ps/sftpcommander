# Changelog

All notable changes to this project are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning aims at
[SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- ProxyJump (single hop) honoured from `~/.ssh/config` when connecting via an SSH alias.
- Parallel upload via worker pool with the same task-queue layout as DownloadBatch (per-worker SFTP session, scan + transfer split).
- Parallel scan in DownloadBatch: directory walk now runs in a worker pool, so deep trees stop blocking on the first slow `ReadDir`.
- Pooled SFTP sessions in `Client` — batches acquire/release sessions instead of paying SSH-channel setup cost each time.
- Overwrite prompt during batch transfers: per-file `replace / skip / resume / abort` with sticky "all" variants.
- Optional SHA256 verification mode (`BatchOptions.Verify`) compares `sha256sum` over an ssh session against a local hash.
- Remote-side copy: `F5` in single-pane runs `cp -R` via ssh, no bytes through the client.
- Recursive find: `F` walks the subtree under the cwd and shows matches relative to the search root.
- External editor: `e` downloads the cursor file to a temp path, opens `$EDITOR`, and re-uploads on save (mtime-checked).
- Preview paging: `m` loads the next 256 KB chunk on demand; status line shows how much is buffered.
- Preview syntax highlighting (chroma, monokai/terminal256 lexer chosen from filename) and always-on line numbers.
- Symlink target shown inline: status bar shows `→ target` when the cursor is on a symlink (resolved async, cached); preview pane shows the target next to the path.
- Directory summary in status bar (`N dirs, M files, X MB`) when no selection is active.
- Two-pane mode (`T`): `Tab` switches focus, `F5`/`F6` copy/move across, `Ctrl-U` swaps panel contents, `=` aligns the inactive panel to the active path.
- `chmod -R` via `C`; `r` rename; `M` mkdir; `D` delete with confirm.
- Error log overlay (`E`) accumulates per-operation failures so they survive after the inline status clears.
- Bookmarks (`b` to add, `B` to list), scoped per `host`+`user`.
- Cached FS layer (`CachedFS`) memoises `List(path)`; invalidated on write ops and explicit refresh (`R`).
- `SFTP_NO_NF=1` forces the ASCII file-icon fallback for terminals without a Nerd Font.

### Changed
- Filter is reset on `cd` so the user does not silently see a filtered child directory.
- Progress aggregation in `DownloadBatch`/`UploadBatch` replaced `sync.Mutex` + per-file map with atomic counters and a CAS-throttled emit.
- Sorted entries cached in the browser; cursor moves and filter changes no longer trigger a full re-sort.

### Fixed
- Download path consistency between resume and first-shot paths.

## [0.1.0] - 2026-05-31

Initial public revision.

### Added
- Saved connections list (`~/.config/sftpcommander/connections.toml`) merged with `~/.ssh/config` aliases.
- Auth chain: ssh-agent -> private key (with passphrase prompt) -> password.
- Host key verification against `~/.ssh/known_hosts` with TOFU prompt, MITM hard-fail, and quiet host-key-algorithm rotation handling.
- Single-pane SFTP browser with sort modes, hidden-file toggle, substring filter, parallel download, single-stream upload, preview, delete/rename/mkdir/chmod, and bookmarks.
