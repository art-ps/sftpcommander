package main

import (
	"fmt"
	"os"

	"github.com/art-ps/sftpcommander/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	p := tea.NewProgram(
		ui.NewApp(),
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
