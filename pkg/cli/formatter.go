package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"
)

type OutputOptions struct {
	JSON  bool
	Quiet bool
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			MarginBottom(1)

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	successBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575")).
			SetString("● ENABLED")

	disabledBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF3333")).
			SetString("✖ DISABLED")
)

func Render(w io.Writer, humanText string, data any, quietVal string, opts OutputOptions) error {
	if opts.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}

	if opts.Quiet {
		if quietVal != "" {
			fmt.Fprintln(w, quietVal)
		}
		return nil
	}

	fmt.Fprintln(w, humanText)
	return nil
}
