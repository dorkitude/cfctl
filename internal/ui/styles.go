package ui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var (
	// Cloudflare-themed color palette ☁️
	Primary   = lipgloss.Color("#F38020") // Cloudflare Orange
	Secondary = lipgloss.Color("#43A047") // Green (success)
	Accent    = lipgloss.Color("#7C4DFF") // Purple (records)
	Warning   = lipgloss.Color("#FFB300") // Amber
	Error     = lipgloss.Color("#E53935") // Red
	Subtle    = lipgloss.Color("#6B7280") // Gray
	Highlight = lipgloss.Color("#F3F4F6") // Light

	// Styles
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(Primary).
			MarginBottom(1)

	SuccessStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(Secondary)

	ErrorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(Error)

	WarningStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(Warning)

	AccentStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(Accent)

	SubtleStyle = lipgloss.NewStyle().
			Foreground(Subtle)

	LabelStyle = lipgloss.NewStyle().
			Bold(true).
			Width(14)

	ValueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF"))

	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Primary).
			Padding(1, 2)

	ListItemStyle = lipgloss.NewStyle().
			PaddingLeft(2)

	SelectedItemStyle = lipgloss.NewStyle().
				Foreground(Primary).
				Bold(true).
				PaddingLeft(2)

	// Record type badge colors
	RecordTypeStyle = lipgloss.NewStyle().
			Bold(true).
			Width(8).
			Foreground(Accent)
)

// Helper functions for common output patterns

func Success(msg string) string {
	return SuccessStyle.Render("✓ " + msg)
}

func Err(msg string) string {
	return ErrorStyle.Render("✗ " + msg)
}

func Warn(msg string) string {
	return WarningStyle.Render("⚠ " + msg)
}

func Info(msg string) string {
	return SubtleStyle.Render("ℹ " + msg)
}

func Title(msg string) string {
	return TitleStyle.Render(msg)
}

// DisableColor turns off all ANSI styling (for --no-color and NO_COLOR).
func DisableColor() {
	lipgloss.SetColorProfile(termenv.Ascii)
}
