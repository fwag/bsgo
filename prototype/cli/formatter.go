package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	jsonOutput  bool
	quietOutput bool
	serverAddr  string
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	successBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575")).
			SetString("● SUCCEEDED")

	buildingBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00BFFF")).
			SetString("◐ BUILDING")

	pendingBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFA500")).
			SetString("○ PENDING")

	failedBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF3333")).
			SetString("✖ FAILED")

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(1, 2)
)

func renderOutput(humanText string, data any, quietValue string) {
	if jsonOutput {
		out, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "JSON encoding error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		return
	}

	if quietOutput {
		if quietValue != "" {
			fmt.Println(quietValue)
		}
		return
	}

	fmt.Println(humanText)
}
