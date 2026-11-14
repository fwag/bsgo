package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

	unresolvableBadge = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E5C07B")).
				SetString("⊘ UNRESOLVABLE")
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
	cursor        int
	termWidth     int
	termHeight    int
	natsURL       string
	nc            *nats.Conn
	js            jetstream.JetStream
	workers       map[string]v1.WorkerHeartbeat
	Jobs          []v1.BuildJob
	viewLog       bool
	logLines      []string
	activeLogSub  *nats.Subscription
	ProjectFilter string
	ArchFilter    string
	ShowMatrix    bool
}

func newWatchCmd() *cobra.Command {
	defaultNATSURL := os.Getenv("NATS_URL")
	if defaultNATSURL == "" {
		defaultNATSURL = "nats://localhost:4222"
	}
	var natsURL string
	var projectFilter string
	var archFilter string

	cmd := &cobra.Command{
		Use:   "watch [project]",
		Short: "Open the interactive live TUI dashboard connected to NATS",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				projectFilter = args[0]
			}

			nc, js, err := natsqueue.ConnectJetStream(natsURL)
			if err != nil {
				return fmt.Errorf("failed to connect to NATS at %s: %w", natsURL, err)
			}
			defer nc.Close()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			_ = natsqueue.SetupStreams(ctx, js)

			m := newLiveDashboardModel(natsURL, nc, js)
			if projectFilter != "" {
				m.ProjectFilter = projectFilter
				m.ShowMatrix = true
			}
			if archFilter != "" {
				m.ArchFilter = archFilter
			}
			p := tea.NewProgram(m, tea.WithAltScreen())

			go m.listenNATS(ctx, p)

			if _, err := p.Run(); err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&natsURL, "nats-url", defaultNATSURL, "NATS JetStream connection URL")
	cmd.Flags().StringVarP(&projectFilter, "project", "p", "", "Filter dashboard to a specific project")
	cmd.Flags().StringVarP(&archFilter, "arch", "a", "", "Filter dashboard to a specific architecture")
	return cmd
}

func newLiveDashboardModel(natsURL string, nc *nats.Conn, js jetstream.JetStream) *DashboardModel {
	m := &DashboardModel{
		natsURL:    natsURL,
		nc:         nc,
		js:         js,
		workers:    make(map[string]v1.WorkerHeartbeat),
		Jobs:       make([]v1.BuildJob, 0),
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

	loadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	m.replayTriggers(loadCtx)
	m.replayJobStatuses(loadCtx)
}

func (m *DashboardModel) replayTriggers(ctx context.Context) {
	m.replayStream(ctx, natsqueue.StreamTriggers, func(data []byte) {
		var trig v1.BuildTrigger
		if err := json.Unmarshal(data, &trig); err == nil {
			m.handleTrigger(trig)
		}
	})
}

func (m *DashboardModel) replayJobStatuses(ctx context.Context) {
	m.replayStream(ctx, natsqueue.StreamJobStatus, func(data []byte) {
		var st map[string]any
		if err := json.Unmarshal(data, &st); err == nil {
			m.handleStatusUpdate(st)
		}
	})
}

func (m *DashboardModel) replayStream(ctx context.Context, streamName string, handler func([]byte)) {
	str, err := m.js.Stream(ctx, streamName)
	if err != nil {
		return
	}
	info, err := str.Info(ctx)
	if err != nil || info.State.Msgs == 0 {
		return
	}

	cons, err := m.js.OrderedConsumer(ctx, streamName, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return
	}

	it, err := cons.Messages()
	if err != nil {
		return
	}
	defer it.Stop()

	total := int(info.State.Msgs)
	for i := 0; i < total; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := it.Next()
		if err != nil {
			break
		}
		handler(msg.Data())
	}
}

func (m *DashboardModel) handleTrigger(trig v1.BuildTrigger) {
	if trig.TriggerID == "" {
		return
	}
	for i, j := range m.Jobs {
		if j.JobID == trig.TriggerID {
			if trig.Package != "" {
				m.Jobs[i].Package = trig.Package
			}
			if trig.Project != "" {
				m.Jobs[i].Project = trig.Project
			}
			return
		}
	}

	arch := trig.Architecture
	if arch == "" {
		arch = "x86_64"
	}
	m.Jobs = append(m.Jobs, v1.BuildJob{
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
		case "tab", "v":
			m.ShowMatrix = !m.ShowMatrix
		case "a":
			m.CycleArchFilter()
		case "p":
			m.CycleProjectFilter()
		case "c":
			m.ProjectFilter = ""
			m.ArchFilter = ""
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			filtered := m.GetFilteredJobs()
			if m.cursor < len(filtered)-1 {
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

	for i, j := range m.Jobs {
		if j.JobID == jobID {
			m.Jobs[i].Target = phase
			if pkgName != "package" {
				m.Jobs[i].Package = pkgName
			}
			if projectName != "" {
				m.Jobs[i].Project = projectName
			}
			return
		}
	}

	m.Jobs = append(m.Jobs, v1.BuildJob{
		JobID:        jobID,
		Project:      projectName,
		Package:      pkgName,
		Architecture: arch,
		Target:       phase,
	})
}

func (m *DashboardModel) toggleLogSubscription() {
	m.viewLog = !m.viewLog
	if !m.viewLog {
		if m.activeLogSub != nil {
			_ = m.activeLogSub.Unsubscribe()
			m.activeLogSub = nil
		}
		return
	}

	filtered := m.GetFilteredJobs()
	if len(filtered) == 0 {
		return
	}

	if m.cursor >= len(filtered) {
		m.cursor = len(filtered) - 1
	}
	curJob := filtered[m.cursor]
	m.logLines = []string{fmt.Sprintf("─── Build Logs: %s (%s) ───", curJob.JobID, curJob.Package)}

	for _, l := range readSavedJobLogs(curJob.JobID) {
		m.appendLog(l)
	}

	sub, err := m.nc.Subscribe("bsgo.logs."+curJob.JobID, func(msg *nats.Msg) {
		m.appendLog(string(msg.Data))
	})
	if err == nil {
		m.activeLogSub = sub
	}
}

func readSavedJobLogs(jobID string) []string {
	savedLog := filepath.Clean(filepath.Join("/tmp/bsgo-worker/jobs", filepath.Clean(jobID), "build.log"))
	data, err := os.ReadFile(savedLog)
	if err != nil || len(data) == 0 {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (m *DashboardModel) View() string {
	var s strings.Builder

	filterInfo := ""
	if m.ProjectFilter != "" || m.ArchFilter != "" {
		pStr := m.ProjectFilter
		if pStr == "" {
			pStr = "All"
		}
		aStr := m.ArchFilter
		if aStr == "" {
			aStr = "All"
		}
		filterInfo = fmt.Sprintf(" • Filter: %s/%s", pStr, aStr)
	}

	header := headerStyle.Render(" bsgo ") + " " +
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Render("Live Cloud-Native Build Service Dashboard") +
		subtleStyle.Render(" (NATS: "+m.natsURL+filterInfo+")")
	s.WriteString(header + "\n\n")

	if m.ShowMatrix {
		matrixBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			Width(106).
			Render(m.RenderMatrixPanel())
		s.WriteString(matrixBox + "\n\n")
	}

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

	s.WriteString(subtleStyle.Render("  [↑/↓] Navigate  •  [Enter/l] Logs  •  [Tab/v] Matrix  •  [p] Project  •  [a] Arch  •  [c] Reset  •  [q] Quit"))
	return s.String()
}

func (m *DashboardModel) renderJobPanel() string {
	var sb strings.Builder
	filteredJobs := m.GetFilteredJobs()
	active, pending, succ, unres, fail := m.countJobStates(filteredJobs)
	title := formatJobPanelTitle(len(filteredJobs), active, pending, succ, unres, fail)
	if m.ProjectFilter != "" {
		title += fmt.Sprintf(" [%s]", m.ProjectFilter)
	}
	sb.WriteString(titleStyle.Render(title) + "\n")

	if len(filteredJobs) == 0 {
		sb.WriteString(subtleStyle.Render("  No build jobs match the selected filter.\n"))
		return sb.String()
	}

	const maxVisible = 7
	startIdx := 0
	if m.cursor >= maxVisible {
		startIdx = m.cursor - maxVisible + 1
	}
	endIdx := startIdx + maxVisible
	if endIdx > len(filteredJobs) {
		endIdx = len(filteredJobs)
	}

	if startIdx > 0 {
		fmt.Fprintf(&sb, "%s\n", subtleStyle.Render(fmt.Sprintf("  ▲ %d more above", startIdx)))
	}

	for i := startIdx; i < endIdx; i++ {
		j := filteredJobs[i]
		cursor := "  "
		if m.cursor == i {
			cursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("▶ ")
		}

		var badge string
		switch j.Target {
		case "BUILDING":
			badge = buildingBadge.Render()
		case "SUCCEEDED":
			badge = succeededBadge.Render()
		case "UNRESOLVABLE":
			badge = unresolvableBadge.Render()
		case "FAILED", "ABORTED":
			badge = failedBadge.SetString("✖ " + j.Target).Render()
		default:
			badge = pendingBadge.Render()
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

	if endIdx < len(filteredJobs) {
		fmt.Fprintf(&sb, "%s\n", subtleStyle.Render(fmt.Sprintf("  ▼ %d more below", len(filteredJobs)-endIdx)))
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

func (m *DashboardModel) countJobStates(jobs []v1.BuildJob) (active, pending, succ, unres, fail int) {
	for _, j := range jobs {
		switch j.Target {
		case "BUILDING":
			active++
		case "SUCCEEDED":
			succ++
		case "UNRESOLVABLE":
			unres++
		case "FAILED", "ABORTED":
			fail++
		default:
			pending++
		}
	}
	return
}

func formatJobPanelTitle(total, active, pending, succ, unres, fail int) string {
	if total == 0 {
		return "Build Jobs (0)"
	}
	if active == 0 && pending == 0 {
		return fmt.Sprintf("Build Jobs (%d) • Completed (✔ %d, ⊘ %d, ✖ %d)", total, succ, unres, fail)
	}
	return fmt.Sprintf("Build Jobs (%d) • ◐ %d active, ○ %d queued, ✔ %d, ⊘ %d", total, active, pending, succ, unres)
}

func (m *DashboardModel) GetFilteredJobs() []v1.BuildJob {
	if m.ProjectFilter == "" && m.ArchFilter == "" {
		return m.Jobs
	}
	var res []v1.BuildJob
	for _, j := range m.Jobs {
		if m.ProjectFilter != "" && j.Project != m.ProjectFilter {
			continue
		}
		if m.ArchFilter != "" && j.Architecture != m.ArchFilter {
			continue
		}
		res = append(res, j)
	}
	return res
}

type ArchMatrixRow struct {
	Arch         string
	Total        int
	Succeeded    int
	Building     int
	Failed       int
	Unresolvable int
	Pending      int
}

func (m *DashboardModel) ComputeMatrixRows() []ArchMatrixRow {
	stats := make(map[string]*ArchMatrixRow)
	var archList []string

	for _, j := range m.Jobs {
		if m.ProjectFilter != "" && j.Project != m.ProjectFilter {
			continue
		}
		arch := j.Architecture
		if arch == "" {
			arch = "x86_64"
		}
		row, exists := stats[arch]
		if !exists {
			row = &ArchMatrixRow{Arch: arch}
			stats[arch] = row
			archList = append(archList, arch)
		}
		row.Total++
		switch j.Target {
		case "BUILDING":
			row.Building++
		case "SUCCEEDED":
			row.Succeeded++
		case "UNRESOLVABLE":
			row.Unresolvable++
		case "FAILED", "ABORTED":
			row.Failed++
		default:
			row.Pending++
		}
	}

	sort.Strings(archList)
	var rows []ArchMatrixRow
	for _, a := range archList {
		rows = append(rows, *stats[a])
	}
	return rows
}

func (m *DashboardModel) RenderMatrixPanel() string {
	var sb strings.Builder
	title := "Architecture Status Matrix"
	if m.ProjectFilter != "" {
		title = fmt.Sprintf("Architecture Status Matrix: %s", m.ProjectFilter)
	}
	sb.WriteString(titleStyle.Render(title) + "\n\n")

	rows := m.ComputeMatrixRows()
	if len(rows) == 0 {
		sb.WriteString(subtleStyle.Render("  No data available for the selected filter.\n"))
		return sb.String()
	}

	fmt.Fprintf(&sb, "  %-10s %6s %13s %12s %10s %16s %11s\n",
		"ARCH", "TOTAL", "✔ SUCCEEDED", "◐ BUILDING", "✖ FAILED", "⊘ UNRESOLVABLE", "○ PENDING")

	var totTotal, totSucc, totBuild, totFail, totUnres, totPend int
	for _, r := range rows {
		totTotal += r.Total
		totSucc += r.Succeeded
		totBuild += r.Building
		totFail += r.Failed
		totUnres += r.Unresolvable
		totPend += r.Pending

		fmt.Fprintf(&sb, "  %-10s %6d %13d %12d %10d %16d %11d\n",
			r.Arch, r.Total, r.Succeeded, r.Building, r.Failed, r.Unresolvable, r.Pending)
	}

	sb.WriteString("  " + strings.Repeat("─", 84) + "\n")
	fmt.Fprintf(&sb, "  %-10s %6d %13d %12d %10d %16d %11d\n",
		"TOTAL", totTotal, totSucc, totBuild, totFail, totUnres, totPend)

	return sb.String()
}

func (m *DashboardModel) CycleArchFilter() {
	arches := m.getDistinctArches()
	if len(arches) == 0 {
		return
	}
	if m.ArchFilter == "" {
		m.ArchFilter = arches[0]
		return
	}
	for i, a := range arches {
		if a == m.ArchFilter {
			if i+1 < len(arches) {
				m.ArchFilter = arches[i+1]
			} else {
				m.ArchFilter = ""
			}
			return
		}
	}
	m.ArchFilter = ""
}

func (m *DashboardModel) CycleProjectFilter() {
	projects := m.getDistinctProjects()
	if len(projects) == 0 {
		return
	}
	if m.ProjectFilter == "" {
		m.ProjectFilter = projects[0]
		return
	}
	for i, p := range projects {
		if p == m.ProjectFilter {
			if i+1 < len(projects) {
				m.ProjectFilter = projects[i+1]
			} else {
				m.ProjectFilter = ""
			}
			return
		}
	}
	m.ProjectFilter = ""
}

func (m *DashboardModel) getDistinctArches() []string {
	seen := make(map[string]bool)
	var list []string
	for _, j := range m.Jobs {
		a := j.Architecture
		if a == "" {
			a = "x86_64"
		}
		if !seen[a] {
			seen[a] = true
			list = append(list, a)
		}
	}
	sort.Strings(list)
	return list
}

func (m *DashboardModel) getDistinctProjects() []string {
	seen := make(map[string]bool)
	var list []string
	for _, j := range m.Jobs {
		p := j.Project
		if p != "" && !seen[p] {
			seen[p] = true
			list = append(list, p)
		}
	}
	sort.Strings(list)
	return list
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
