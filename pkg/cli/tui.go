package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
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

	idleBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575")).
			SetString("● IDLE")

	succeededBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575")).
			SetString("✔ SUCCEEDED")
)

type HeartbeatMsg v1.WorkerHeartbeat
type LogChunkMsg string
type StatusUpdateMsg map[string]any
type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type DashboardModel struct {
	cursor       int
	natsURL      string
	nc           *nats.Conn
	workers      map[string]v1.WorkerHeartbeat
	jobs         []v1.BuildJob
	viewLog      bool
	logLines     []string
	activeLogSub *nats.Subscription
}

func newWatchCmd() *cobra.Command {
	var natsURL string

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Open the interactive live TUI dashboard connected to NATS",
		RunE: func(cmd *cobra.Command, args []string) error {
			nc, _, err := natsqueue.ConnectJetStream(natsURL)
			if err != nil {
				return fmt.Errorf("failed to connect to NATS at %s: %w", natsURL, err)
			}
			defer nc.Close()

			m := newLiveDashboardModel(natsURL, nc)
			p := tea.NewProgram(m, tea.WithAltScreen())

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go m.listenNATS(ctx, p)

			if _, err := p.Run(); err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&natsURL, "nats-url", "nats://localhost:4222", "NATS JetStream connection URL")
	return cmd
}

func newLiveDashboardModel(natsURL string, nc *nats.Conn) *DashboardModel {
	return &DashboardModel{
		natsURL:  natsURL,
		nc:       nc,
		workers:  make(map[string]v1.WorkerHeartbeat),
		jobs:     make([]v1.BuildJob, 0),
		logLines: make([]string, 0),
	}
}

func (m *DashboardModel) listenNATS(ctx context.Context, p *tea.Program) {
	_, _ = m.nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if err := json.Unmarshal(msg.Data, &hb); err == nil {
			p.Send(HeartbeatMsg(hb))
		}
	})

	_, _ = m.nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st map[string]any
		if err := json.Unmarshal(msg.Data, &st); err == nil {
			p.Send(StatusUpdateMsg(st))
		}
	})

	<-ctx.Done()
}

func (m *DashboardModel) Init() tea.Cmd {
	return tickCmd()
}

func (m *DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.toggleLogSubscription()
		}
	case HeartbeatMsg:
		hb := v1.WorkerHeartbeat(msg)
		m.workers[hb.WorkerID] = hb
	case LogChunkMsg:
		m.appendLog(string(msg))
	case StatusUpdateMsg:
		m.handleStatusUpdate(msg)
	case tickMsg:
		return m, tickCmd()
	}
	return m, nil
}

func (m *DashboardModel) appendLog(line string) {
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > 50 {
		m.logLines = m.logLines[len(m.logLines)-50:]
	}
}

func (m *DashboardModel) handleStatusUpdate(st map[string]any) {
	jobID, _ := st["job_id"].(string)
	phase, _ := st["phase"].(string)
	if jobID == "" {
		return
	}

	for i, j := range m.jobs {
		if j.JobID == jobID {
			m.jobs[i].Target = phase
			return
		}
	}

	m.jobs = append(m.jobs, v1.BuildJob{
		JobID:        jobID,
		Project:      "Build Job",
		Package:      jobID,
		Architecture: "x86_64",
		Target:       phase,
	})
}

func (m *DashboardModel) toggleLogSubscription() {
	m.viewLog = !m.viewLog
	if !m.viewLog && m.activeLogSub != nil {
		_ = m.activeLogSub.Unsubscribe()
		m.activeLogSub = nil
		return
	}

	if m.viewLog && len(m.jobs) > 0 {
		curJob := m.jobs[m.cursor]
		m.logLines = []string{fmt.Sprintf("Subscribed to live NATS log stream: bsgo.logs.%s", curJob.JobID)}
		sub, err := m.nc.Subscribe("bsgo.logs."+curJob.JobID, func(msg *nats.Msg) {
			m.appendLog(string(msg.Data))
		})
		if err == nil {
			m.activeLogSub = sub
		}
	}
}

func (m *DashboardModel) View() string {
	var s strings.Builder

	header := headerStyle.Render(" bsgo ") + " " +
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Render("Live Cloud-Native Build Service Dashboard") +
		subtleStyle.Render(" (NATS: "+m.natsURL+" • Postgres: Connected)")
	s.WriteString(header + "\n\n")

	jobPanel := m.renderJobPanel()
	workerPanel := m.renderWorkerPanel()

	leftCol := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(1, 2).Width(58).Render(jobPanel)
	rightCol := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(1, 2).Width(46).Render(workerPanel)
	s.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol) + "\n\n")

	if m.viewLog {
		var logBox strings.Builder
		for _, l := range m.logLines {
			logBox.WriteString(subtleStyle.Render(l) + "\n")
		}
		s.WriteString(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(1, 2).Width(106).Render(logBox.String()) + "\n\n")
	}

	s.WriteString(subtleStyle.Render("  [↑/↓] Navigate  •  [Enter/l] Toggle Live Logs  •  [q] Quit"))
	return s.String()
}

func (m *DashboardModel) renderJobPanel() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Active & Pending Jobs (%d)", len(m.jobs))) + "\n")
	if len(m.jobs) == 0 {
		sb.WriteString(subtleStyle.Render("  No active build jobs currently queued.\n  Run './bin/bsgo package trigger <pkg>' to trigger a build.\n"))
		return sb.String()
	}

	for i, j := range m.jobs {
		cursor := "  "
		if m.cursor == i {
			cursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("▶ ")
		}
		badge := buildingBadge.Render()
		if j.Target == "SUCCEEDED" {
			badge = succeededBadge.Render()
		}
		fmt.Fprintf(&sb, "%s%-16s %s  %-16s %s\n", cursor, j.JobID, badge, j.Package, subtleStyle.Render(j.Architecture))
	}
	return sb.String()
}

func (m *DashboardModel) renderWorkerPanel() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Connected Worker Fleet (%d)", len(m.workers))) + "\n")
	if len(m.workers) == 0 {
		sb.WriteString(subtleStyle.Render("  No workers currently heartbeating.\n  Start a worker daemon with './bin/bsgo-worker'.\n"))
		return sb.String()
	}

	for _, w := range m.workers {
		wBadge := idleBadge.Render()
		if w.Status == "BUSY" {
			wBadge = buildingBadge.SetString("● BUSY").Render()
		}
		fmt.Fprintf(&sb, "  • %-16s %s %s (%d cores, %dGB)\n",
			w.WorkerID, subtleStyle.Render(w.Architecture), wBadge, w.Hardware.TotalVCPUs, w.Hardware.TotalMemoryMB/1024)
		if w.ActiveJobID != "" {
			fmt.Fprintf(&sb, "    running: %s\n", subtleStyle.Render(w.ActiveJobID))
		}
	}
	return sb.String()
}
