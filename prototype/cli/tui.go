package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Open the interactive live TUI dashboard",
	Run: func(cmd *cobra.Command, args []string) {
		p := tea.NewProgram(initialModel())
		if _, err := p.Run(); err != nil {
			fmt.Printf("Error starting TUI: %v\n", err)
		}
	},
}

type tickMsg time.Time

type model struct {
	cursor   int
	jobs     []BuildJob
	workers  []Worker
	viewLog  bool
	logLines []string
}

func initialModel() model {
	return model{
		cursor:  0,
		jobs:    mockJobs,
		workers: mockWorkers,
		viewLog: false,
		logLines: []string{
			"[00:00:01] bsgo-worker-x86-01: claimed build job",
			"[00:00:03] obs-build: bootstrap chroot on QCOW2 overlay",
			"[00:00:10] obs-build: compiling C sources with gcc",
			"[00:00:25] obs-build: package check passed (rpmlint clean)",
		},
	}
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
		case "l", "enter":
			m.viewLog = !m.viewLog
		case "c":
			if len(m.jobs) > 0 {
				m.jobs[m.cursor].Status = "ABORTED"
			}
		}
	case tickMsg:
		return m, tickCmd()
	}
	return m, nil
}

func (m model) View() string {
	var s strings.Builder

	// Header
	header := headerStyle.Render(" bsgo ") + " " +
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Render("Cloud-Native Build Service Dashboard") +
		subtleStyle.Render(" (NATS JetStream: Connected • Postgres: Synced • S3: Ready)")
	s.WriteString(header + "\n\n")

	// Jobs Panel
	var jobPanel strings.Builder
	jobPanel.WriteString(titleStyle.Render("Active & Pending Jobs") + "\n")

	for i, j := range m.jobs {
		cursor := "  "
		if m.cursor == i {
			cursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("▶ ")
		}

		badge := pendingBadge
		switch j.Status {
		case "BUILDING":
			badge = buildingBadge
		case "SUCCEEDED":
			badge = successBadge
		case "FAILED", "ABORTED":
			badge = failedBadge.SetString(j.Status)
		}

		fmt.Fprintf(&jobPanel, "%s%-12s %s  %-20s %-8s p:%-3d %s\n",
			cursor,
			j.ID,
			badge.Render(),
			lipgloss.NewStyle().Bold(true).Render(j.Package),
			subtleStyle.Render(j.Arch),
			j.Priority,
			subtleStyle.Render(j.Duration),
		)
	}

	// Workers Panel
	var workerPanel strings.Builder
	workerPanel.WriteString(titleStyle.Render("Worker Fleet Capacity") + "\n")

	for _, w := range m.workers {
		wStatus := successBadge.SetString("IDLE").Render()
		if w.Status == "BUSY" {
			wStatus = buildingBadge.SetString("BUSY").Render()
		}
		fmt.Fprintf(&workerPanel, "  • %-16s %-8s %s  (%d cores, %dGB)\n    %s\n",
			w.ID,
			subtleStyle.Render(w.Arch),
			wStatus,
			w.VCPUs,
			w.MemoryMB/1024,
			subtleStyle.Render("running: "+w.ActiveJob),
		)
	}

	// Layout columns
	leftCol := boxStyle.Width(58).Render(jobPanel.String())
	rightCol := boxStyle.Width(46).Render(workerPanel.String())

	s.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol))
	s.WriteString("\n")

	// Optional Log Viewer Drawer
	if m.viewLog && len(m.jobs) > 0 {
		curJob := m.jobs[m.cursor]
		var logBox strings.Builder
		logBox.WriteString(lipgloss.NewStyle().Bold(true).Render("Live NATS Log Stream: bsgo.logs."+curJob.ID) + "\n")
		for _, l := range m.logLines {
			logBox.WriteString(subtleStyle.Render(l) + "\n")
		}
		s.WriteString(boxStyle.Width(106).Render(logBox.String()))
		s.WriteString("\n")
	}

	// Footer Help
	s.WriteString(subtleStyle.Render("  [↑/↓] Navigate  •  [Enter/l] Toggle Logs  •  [c] Abort Job  •  [q] Quit"))

	return s.String()
}
