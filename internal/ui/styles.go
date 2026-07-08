package ui

import "github.com/charmbracelet/lipgloss"

// Catppuccin Mocha palette
var (
	colBase    = lipgloss.Color("#1e1e2e")
	colSurface = lipgloss.Color("#313244")
	colOverlay = lipgloss.Color("#6c7086")
	colText    = lipgloss.Color("#cdd6f4")
	colSubtext = lipgloss.Color("#a6adc8")
	colBlue    = lipgloss.Color("#89b4fa")
	colMauve   = lipgloss.Color("#cba6f7")
	colGreen   = lipgloss.Color("#a6e3a1")
	colYellow  = lipgloss.Color("#f9e2af")
	colRed     = lipgloss.Color("#f38ba8")

	styleInput = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBlue).
			Padding(0, 1).
			Foreground(colText)

	styleSelected = lipgloss.NewStyle().
			Background(colSurface).
			Foreground(colText).
			Bold(true).
			PaddingLeft(1)

	styleNormal = lipgloss.NewStyle().
			Foreground(colText).
			PaddingLeft(1)

	styleDim = lipgloss.NewStyle().
			Foreground(colOverlay)

	styleBranch = lipgloss.NewStyle().
			Foreground(colMauve)

	styleSessionActive = lipgloss.NewStyle().
				Foreground(colGreen)

	styleSessionNone = lipgloss.NewStyle().
				Foreground(colOverlay)

	styleSpinner = lipgloss.NewStyle().
			Foreground(colBlue)

	styleWorktreeItem = lipgloss.NewStyle().
				Foreground(colSubtext).
				PaddingLeft(3)

	styleWorktreeSelected = lipgloss.NewStyle().
				Background(colSurface).
				Foreground(colSubtext).
				PaddingLeft(3)

	styleHints = lipgloss.NewStyle().
			Foreground(colOverlay).
			PaddingLeft(1)

	styleTitle = lipgloss.NewStyle().
			Foreground(colBlue).
			Bold(true).
			PaddingLeft(1).
			PaddingBottom(1)

	styleSeparator = lipgloss.NewStyle().
			Foreground(colSurface)
)

const (
	iconGit      = " "
	iconDir      = " "
	iconWorktree = " "
	iconDot      = "●"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
