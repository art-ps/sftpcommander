package ui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

var (
	colorPrimary   = lipgloss.Color("#7C3AED")
	colorSecondary = lipgloss.Color("#A78BFA")
	colorAccent    = lipgloss.Color("#10B981")
	colorMuted     = lipgloss.Color("#6B7280")
	colorError     = lipgloss.Color("#EF4444")
	colorWarning   = lipgloss.Color("#F59E0B")
	colorText      = lipgloss.Color("#F9FAFB")
	colorBg        = lipgloss.Color("#1F2937")
	colorBgAlt     = lipgloss.Color("#111827")
	colorBorder    = lipgloss.Color("#374151")
	colorSelected  = lipgloss.Color("#4C1D95")

	styleLogo = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true).
			PaddingBottom(1)

	styleTitle = lipgloss.NewStyle().
			Foreground(colorText).
			Bold(true).
			Background(colorPrimary).
			Padding(0, 2)

	styleSubtitle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	stylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(1, 2)

	styleSelected = lipgloss.NewStyle().
			Background(colorSelected).
			Foreground(colorText).
			Bold(true).
			PaddingLeft(1).
			PaddingRight(1)

	styleNormal = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(1).
			PaddingRight(1)

	styleDir = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true)

	styleFile = lipgloss.NewStyle().
			Foreground(colorText)

	styleSize = lipgloss.NewStyle().
			Foreground(colorMuted).
			Width(10).
			Align(lipgloss.Right)

	styleStatusBar = lipgloss.NewStyle().
			Background(colorBgAlt).
			Foreground(colorMuted).
			Padding(0, 1)

	styleStatusOk = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleStatusErr = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	styleKeyHint = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleKey = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true)

	styleError = lipgloss.NewStyle().
			Foreground(colorError).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorError).
			Padding(0, 1)

	styleProgress = lipgloss.NewStyle().
			Foreground(colorAccent)

	stylePath = lipgloss.NewStyle().
			Foreground(colorWarning).
			Bold(true)
)

func keyHint(key, desc string) string {
	return styleKey.Render(key) + styleKeyHint.Render(" "+desc)
}

func formatSize(size int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case size >= GB:
		return fmt.Sprintf("%.1fG", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.1fM", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.1fK", float64(size)/KB)
	default:
		return fmt.Sprintf("%dB", size)
	}
}
