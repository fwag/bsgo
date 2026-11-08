package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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

	pendingBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFA500")).
			SetString("○ PENDING")

	succeededBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575")).
			SetString("✔ SUCCEEDED")

	failedBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF3333")).
			SetString("✖ FAILED")
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
	termWidth    int
	termHeight   int
	natsURL      string
	nc           *nats.Conn
	js           jetstream.JetStream
	workers      map[string]v1.WorkerHeartbeat
	jobs         []v1.BuildJob
	viewLog      bool
	logLines     []string
	activeLogSub *nats.Subscription
}

func newWatchCmd() *cobra.Command {
	defaultNATSURL := os.Getenv("NATS_URL")
	if defaultNATSURL == "" {
		defaultNATSURL = "nats://localhost:4222"
	}
	var natsURL string

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Open the interactive live TUI dashboard connected to NATS",
		RunE: func(cmd *cobra.Command, args []string) error {
			nc, js, err := natsqueue.ConnectJetStream(natsURL)
			if err != nil {
				return fmt.Errorf("failed to connect to NATS at %s: %w", natsURL, err)
			}
			defer nc.Close()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			_ = natsqueue.SetupStreams(ctx, js)

			m := newLiveDashboardModel(natsURL, nc, js)
			p := tea.NewProgram(m, tea.WithAltScreen())

			go m.listenNATS(ctx, p)

			if _, err := p.Run(); err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&natsURL, "nats-url", defaultNATSURL, "NATS JetStream connection URL")
	return cmd
}

func newLiveDashboardModel(natsURL string, nc *nats.Conn, js jetstream.JetStream) *DashboardModel {
	m := &DashboardModel{
		natsURL:    natsURL,
		nc:         nc,
		js:         js,
		workers:    make(map[string]v1.WorkerHeartbeat),
		jobs:       make([]v1.BuildJob, 0),
		logLines:   make([]string, 0),
		termWidth:  110,
		termHeight: 30,
	}
	if js != nil {
		m.loadHistoricalJobs(context.Background())
	}
	return m
}

func (m *DashboardModel) loadHistoricalJobs(ctx context.Context) {
	if m.js == nil {
		return
	}

	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	m.replayTriggers(loadCtx)
	m.replayJobStatuses(loadCtx)
}

func (m *DashboardModel) replayTriggers(ctx context.Context) {
	cons, err := m.js.OrderedConsumer(ctx, natsqueue.StreamTriggers, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return
	}

	batch, err := cons.FetchNoWait(1000)
	if err != nil {
		return
	}

	for msg := range batch.Messages() {
		var trig v1.BuildTrigger
		if err := json.Unmarshal(msg.Data(), &trig); err == nil {
			m.handleTrigger(trig)
		}
	}
}

func (m *DashboardModel) replayJobStatuses(ctx context.Context) {
	cons, err := m.js.OrderedConsumer(ctx, natsqueue.StreamJobStatus, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return
	}

	batch, err := cons.FetchNoWait(1000)
	if err != nil {
		return
	}

	for msg := range batch.Messages() {
		var st map[string]any
		if err := json.Unmarshal(msg.Data(), &st); err == nil {
			m.handleStatusUpdate(st)
		}
	}
}

func (m *DashboardModel) handleTrigger(trig v1.BuildTrigger) {
	if trig.TriggerID == "" {
		return
	}
	for i, j := range m.jobs {
		if j.JobID == trig.TriggerID {
			if trig.Package != "" {
				m.jobs[i].Package = trig.Package
			}
			if trig.Project != "" {
				m.jobs[i].Project = trig.Project
			}
			return
		}
	}

	arch := trig.Architecture
	if arch == "" {
		arch = "x86_64"
	}
	m.jobs = append(m.jobs, v1.BuildJob{
		JobID:        trig.TriggerID,
		Project:      trig.Project,
		Package:      trig.Package,
		Architecture: arch,
		Target:       "PENDING",
	})
}

func (m *DashboardModel) listenNATS(ctx context.Context, p *tea.Program) {
	_, _ = m.nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if err := json.Unmarshal(msg.Data, &hb); err == nil {
			p.Send(HeartbeatMsg(hb))
		}
	})

	_, _ = m.nc.Subscribe("bsgo.triggers.>", func(msg *nats.Msg) {
		var trig v1.BuildTrigger
		if err := json.Unmarshal(msg.Data, &trig); err == nil {
			arch := trig.Architecture
			if arch == "" {
				arch = "x86_64"
			}
			statusMsg := map[string]any{
				"job_id":       trig.TriggerID,
				"project":      trig.Project,
				"package":      trig.Package,
				"phase":        "PENDING",
				"architecture": arch,
			}
			p.Send(StatusUpdateMsg(statusMsg))
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
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
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
	if len(m.logLines) > 100 {
		m.logLines = m.logLines[len(m.logLines)-100:]
	}
}

func (m *DashboardModel) handleStatusUpdate(st map[string]any) {
	jobID, _ := st["job_id"].(string)
	phase, _ := st["phase"].(string)
	if jobID == "" {
		return
	}

	pkgName, _ := st["package"].(string)
	if pkgName == "" {
		pkgName = "package"
	}
	projectName, _ := st["project"].(string)
	arch, _ := st["architecture"].(string)
	if arch == "" {
		arch = "x86_64"
	}

	for i, j := range m.jobs {
		if j.JobID == jobID {
			m.jobs[i].Target = phase
			if pkgName != "package" {
				m.jobs[i].Package = pkgName
			}
			if projectName != "" {
				m.jobs[i].Project = projectName
			}
			return
		}
	}

	m.jobs = append(m.jobs, v1.BuildJob{
		JobID:        jobID,
		Project:      projectName,
		Package:      pkgName,
		Architecture: arch,
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
		m.logLines = []string{fmt.Sprintf("─── Build Logs: %s (%s) ───", curJob.JobID, curJob.Package)}

		cleanJobID := filepath.Clean(curJob.JobID)
		savedLog := filepath.Clean(filepath.Join("/tmp/bsgo-worker/jobs", cleanJobID, "build.log"))
		if data, err := os.ReadFile(savedLog); err == nil && len(data) > 0 {
			lines := strings.Split(string(data), "\n")
			for _, l := range lines {
				if strings.TrimSpace(l) != "" {
					m.appendLog(l)
				}
			}
		}

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

	panelHeight := 10
	leftCol := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		Width(60).
		Height(panelHeight).
		Render(jobPanel)

	rightCol := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		Width(44).
		Height(panelHeight).
		Render(workerPanel)

	s.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol) + "\n\n")

	if m.viewLog {
		logBox := m.renderLogBox()
		s.WriteString(lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			Width(106).
			Height(8).
			Render(logBox) + "\n\n")
	}

	s.WriteString(subtleStyle.Render("  [↑/↓] Navigate  •  [Enter/l] Toggle Live Logs  •  [q] Quit"))
	return s.String()
}

func (m *DashboardModel) renderJobPanel() string {
	var sb strings.Builder
	active, pending, succ, fail := m.countJobStates()
	sb.WriteString(titleStyle.Render(formatJobPanelTitle(len(m.jobs), active, pending, succ, fail)) + "\n")

	if len(m.jobs) == 0 {
		sb.WriteString(subtleStyle.Render("  No build jobs queued or completed.\n  Run './bin/bsgo package trigger <pkg>' to trigger a build.\n"))
		return sb.String()
	}

	const maxVisible = 7
	startIdx := 0
	if m.cursor >= maxVisible {
		startIdx = m.cursor - maxVisible + 1
	}
	endIdx := startIdx + maxVisible
	if endIdx > len(m.jobs) {
		endIdx = len(m.jobs)
	}

	if startIdx > 0 {
		fmt.Fprintf(&sb, "%s\n", subtleStyle.Render(fmt.Sprintf("  ▲ %d more above", startIdx)))
	}

	for i := startIdx; i < endIdx; i++ {
		j := m.jobs[i]
		cursor := "  "
		if m.cursor == i {
			cursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("▶ ")
		}

		badge := buildingBadge.Render()
		switch j.Target {
		case "SUCCEEDED":
			badge = succeededBadge.Render()
		case "PENDING", "QUEUED":
			badge = pendingBadge.Render()
		case "FAILED", "ABORTED":
			badge = failedBadge.SetString("✖ " + j.Target).Render()
		}

		displayName := j.Package
		if j.Project != "" && j.Project != "Build Job" {
			displayName = j.Project + "/" + j.Package
		}
		if len(displayName) > 22 {
			displayName = displayName[:19] + "..."
		}

		displayJobID := j.JobID
		if len(displayJobID) > 16 {
			displayJobID = displayJobID[:13] + "..."
		}
		fmt.Fprintf(&sb, "%s%-16s %s %-22s\n", cursor, displayJobID, badge, displayName)
	}

	if endIdx < len(m.jobs) {
		fmt.Fprintf(&sb, "%s\n", subtleStyle.Render(fmt.Sprintf("  ▼ %d more below", len(m.jobs)-endIdx)))
	}

	return sb.String()
}

func (m *DashboardModel) renderLogBox() string {
	var sb strings.Builder
	const maxLogLines = 7
	linesToShow := m.logLines
	if len(linesToShow) > maxLogLines {
		linesToShow = linesToShow[len(linesToShow)-maxLogLines:]
	}

	for _, l := range linesToShow {
		sb.WriteString(subtleStyle.Render(l) + "\n")
	}
	return sb.String()
}

func (m *DashboardModel) countJobStates() (active, pending, succ, fail int) {
	for _, j := range m.jobs {
		switch j.Target {
		case "BUILDING":
			active++
		case "PENDING", "QUEUED":
			pending++
		case "SUCCEEDED":
			succ++
		case "FAILED", "ABORTED":
			fail++
		}
	}
	return
}

func formatJobPanelTitle(total, active, pending, succ, fail int) string {
	if total == 0 {
		return "Build Jobs (0)"
	}
	if active == 0 && pending == 0 {
		return fmt.Sprintf("Build Jobs (%d) • Completed (✔ %d, ✖ %d)", total, succ, fail)
	}
	return fmt.Sprintf("Build Jobs (%d) • ◐ %d active, ○ %d queued", total, active, pending)
}

func (m *DashboardModel) renderWorkerPanel() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Worker Fleet (%d)", len(m.workers))) + "\n")
	if len(m.workers) == 0 {
		sb.WriteString(subtleStyle.Render("  No workers heartbeating.\n  Start worker: './bin/bsgo-worker'.\n"))
		return sb.String()
	}

	for _, w := range m.workers {
		wBadge := idleBadge.Render()
		if w.Status == "BUSY" {
			wBadge = buildingBadge.SetString("● BUSY").Render()
		}
		fmt.Fprintf(&sb, "  • %-14s %s %s (%dc, %dGB)\n",
			w.WorkerID, subtleStyle.Render(w.Architecture), wBadge, w.Hardware.TotalVCPUs, w.Hardware.TotalMemoryMB/1024)
		if w.ActiveJobID != "" {
			fmt.Fprintf(&sb, "    running: %s\n", subtleStyle.Render(w.ActiveJobID))
		}
	}
	return sb.String()
}
