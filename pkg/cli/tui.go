package cli

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/spf13/cobra"
)

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	buildingBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00BFFF")).
			SetString("◐ BUILDING")
)

type DashboardModel struct {
	cursor   int
	jobs     []v1.BuildJob
	workers  []v1.WorkerHeartbeat
	viewLog  bool
	logLines []string
}

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Open the interactive live TUI dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := tea.NewProgram(initialDashboardModel())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("failed to run TUI: %w", err)
			}
			return nil
		},
	}
}

func initialDashboardModel() DashboardModel {
	return DashboardModel{
		cursor: 0,
		jobs: []v1.BuildJob{
			{JobID: "job-9a8b1c", Project: "openSUSE:Factory", Package: "glibc", Architecture: "x86_64", Priority: 100},
			{JobID: "job-7f6e5d", Project: "openSUSE:Factory", Package: "chromium", Architecture: "x86_64", Priority: 10},
		},
		workers: []v1.WorkerHeartbeat{
			{WorkerID: "worker-x86-01", Architecture: "x86_64", Status: "BUSY", ActiveJobID: "job-9a8b1c", Hardware: v1.WorkerHardware{TotalVCPUs: 16, TotalMemoryMB: 65536}},
			{WorkerID: "worker-arm-01", Architecture: "aarch64", Status: "IDLE", Hardware: v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384}},
		},
		viewLog: false,
		logLines: []string{
			"[00:00:01] bsgo-worker-x86-01: claimed build job",
			"[00:00:03] obs-build: bootstrap chroot on QCOW2 overlay",
			"[00:00:10] obs-build: compiling C sources with gcc",
		},
	}
}

type tickMsg time.Time

func (m DashboardModel) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		}
	case tickMsg:
		return m, tickCmd()
	}
	return m, nil
}

func (m DashboardModel) View() string {
	var s strings.Builder

	header := headerStyle.Render(" bsgo ") + " " +
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Render("Cloud-Native Build Service Dashboard") +
		subtleStyle.Render(" (NATS JetStream: Connected • Postgres: Synced • S3: Ready)")
	s.WriteString(header + "\n\n")

	var jobPanel strings.Builder
	jobPanel.WriteString(titleStyle.Render("Active & Pending Jobs") + "\n")

	for i, j := range m.jobs {
		cursor := "  "
		if m.cursor == i {
			cursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("▶ ")
		}
		fmt.Fprintf(&jobPanel, "%s%-12s %s  %-20s %-8s p:%-3d\n",
			cursor, j.JobID, buildingBadge.Render(),
			lipgloss.NewStyle().Bold(true).Render(j.Package),
			subtleStyle.Render(j.Architecture), j.Priority)
	}

	var workerPanel strings.Builder
	workerPanel.WriteString(titleStyle.Render("Worker Fleet Capacity") + "\n")

	for _, w := range m.workers {
		wStatus := successBadge.SetString("IDLE").Render()
		if w.Status == "BUSY" {
			wStatus = buildingBadge.SetString("BUSY").Render()
		}
		fmt.Fprintf(&workerPanel, "  • %-16s %-8s %s  (%d cores, %dGB)\n    %s\n",
			w.WorkerID, subtleStyle.Render(w.Architecture), wStatus,
			w.Hardware.TotalVCPUs, w.Hardware.TotalMemoryMB/1024,
			subtleStyle.Render("running: "+w.ActiveJobID),
		)
	}

	leftCol := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(1, 2).Width(58).Render(jobPanel.String())
	rightCol := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(1, 2).Width(46).Render(workerPanel.String())

	s.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol))
	s.WriteString("\n")

	if m.viewLog && len(m.jobs) > 0 {
		curJob := m.jobs[m.cursor]
		var logBox strings.Builder
		logBox.WriteString(lipgloss.NewStyle().Bold(true).Render("Live NATS Log Stream: bsgo.logs."+curJob.JobID) + "\n")
		for _, l := range m.logLines {
			logBox.WriteString(subtleStyle.Render(l) + "\n")
		}
		s.WriteString(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(1, 2).Width(106).Render(logBox.String()))
		s.WriteString("\n")
	}

	s.WriteString(subtleStyle.Render("  [↑/↓] Navigate  •  [Enter/l] Toggle Logs  •  [q] Quit"))

	return s.String()
}
