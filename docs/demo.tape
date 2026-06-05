# VHS demo for sftpcommander.
# Render: vhs docs/demo.tape  →  docs/demo.gif
#
# Prerequisites:
#   - ./sftpcommander built (go build -o sftpcommander .)
#   - SSH agent loaded with the key, OR the connection's key_path is unencrypted
#   - The connection already saved in ~/.config/sftpcommander/connections.toml

Output "docs/demo.gif"

Set Shell "bash"
Set FontSize 14
Set Width 1280
Set Height 760
Set Padding 16
Set Theme "Dracula"
Set TypingSpeed 50ms
Set PlaybackSpeed 1.0

Hide
Type "clear"
Enter
Show

# Launch — opens on the connection list with the saved entry.
Type "./sftpcommander"
Sleep 300ms
Enter
Sleep 2500ms

Sleep 300ms
Enter

Sleep 300ms
Enter

# Connect to the highlighted entry.
Enter
Sleep 4500ms

# We are now in two-pane mode (local | remote). Focus is on the left (local).
# Switch to the remote pane.
Tab
Sleep 1s

# Scroll the remote listing.
Down@200ms 5
Sleep 600ms
Down@200ms 5
Sleep 600ms
Up@200ms 2
Sleep 600ms

# Cycle sort modes (name → size → mtime).
Type "s"
Sleep 1800ms
Type "s"
Sleep 1800ms
Type "s"
Sleep 1800ms

# Toggle hidden files.
Type "."
Sleep 2s
Type "."
Sleep 2s

# Live filter.
Type "/"
Sleep 1300ms
Type "."
Sleep 1200ms
Escape
Sleep 1600ms

# Open help overlay, then close.
Type "?"
Sleep 3500ms
Escape
Sleep 1s

# Switch back to single-pane browser.
Ctrl+W
Sleep 2500ms

# Quit.
Type "q"
Sleep 500ms
