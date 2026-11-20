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
	"github.com/openSUSE/bsgo/pkg/storage"
)

// watchMatrixColumns are always shown in the matrix; other states follow once present.
var watchMatrixColumns = []string{
	storage.StateSucceeded, storage.StateBuilding, storage.StateScheduled,
	storage.StateFailed, storage.StateUnresolvable, storage.StateBlocked,
}

var (
	watchHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FAFAFA")).
				Background(lipgloss.Color("#7D56F4")).
				Padding(0, 1)
	watchCursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	watchStateColors = map[string]string{
		storage.StateBuilding:     "#00BFFF",
		storage.StateScheduled:    "#FFA500",
		storage.StateSucceeded:    "#04B575",
		storage.StateFailed:       "#FF3333",
		storage.StateUnresolvable: "#E5C07B",
		storage.StateBlocked:      "#C678DD",
	}
)

const (
	watchFooter     = "[b]uilding [s]cheduled [f]ailed [u]nresolvable bloc[k]ed [a]ll  [/] search  [Enter/l] logs  [m] matrix  [?] legend  [q] quit"
	watchMaxLogs    = 200
	watchMaxSummary = 6
	watchFetchTTL   = 10 * time.Second
)

type (
	snapshotMsg struct {
		snap watchSnapshot
		// active are the building rows, for the worker fleet panel.
		active []PackageStateEntry
		err    error
	}
	watchTickMsg      time.Time
	watchNudgeMsg     struct{}
	watchHeartbeatMsg v1.WorkerHeartbeat
	watchLogMsg       string
)

// subscribeWatchFeeds forwards worker heartbeats and job status messages to
// the program. Status messages only nudge a refresh; the state itself always
// comes from the store.
func subscribeWatchFeeds(nc *nats.Conn, send func(tea.Msg)) error {
	if _, err := nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if json.Unmarshal(msg.Data, &hb) == nil {
			send(watchHeartbeatMsg(hb))
		}
	}); err != nil {
		return fmt.Errorf("failed subscribing to worker heartbeats: %w", err)
	}
	if _, err := nc.Subscribe(natsqueue.SubjectJobStatus, func(*nats.Msg) { send(watchNudgeMsg{}) }); err != nil {
		return fmt.Errorf("failed subscribing to job status: %w", err)
	}
	return nil
}

// watchModel is the bubbletea model; all filtering lives in watchView.
type watchModel struct {
	ctx      context.Context
	src      stateSource
	logs     logSource
	send     func(tea.Msg)
	project  string
	repo     string
	arch     string
	interval time.Duration

	view      watchView
	cursor    int
	width     int
	height    int
	loading   bool
	searching bool
	err       string
	note      string
	showHelp  bool
	workers   map[string]v1.WorkerHeartbeat
	summaries []RepoArchSummary
	active    map[string]PackageStateEntry

	logJob   string
	logLines []string
	stopLog  func() error
}

func newWatchModel(ctx context.Context, src stateSource, logs logSource, p watchParams, state string) *watchModel {
	return &watchModel{
		ctx: ctx, src: src, logs: logs, send: func(tea.Msg) {},
		project: p.Project, repo: p.Repo, arch: p.Arch, interval: p.Interval,
		view:    watchView{State: state, Counts: map[string]int{}},
		width:   110,
		height:  30,
		workers: make(map[string]v1.WorkerHeartbeat),
		active:  make(map[string]PackageStateEntry),
	}
}

func (m *watchModel) Init() tea.Cmd {
	return tea.Batch(m.fetch(), m.tick())
}

func (m *watchModel) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return watchTickMsg(t) })
}

// fetch starts one snapshot query unless one is already running.
func (m *watchModel) fetch() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	filter := storage.BuildStateFilter{Repo: m.repo, Arch: m.arch, State: m.view.FetchState()}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, watchFetchTTL)
		defer cancel()
		snap, active, err := snapshotWithActive(ctx, m.src, m.project, filter)
		return snapshotMsg{snap: snap, active: active, err: err}
	}
}

// snapshotWithActive returns the snapshot of filter and the building rows. When
// the filter shows another state the building rows need a second query.
func snapshotWithActive(ctx context.Context, src stateSource, project string, filter storage.BuildStateFilter) (watchSnapshot, []PackageStateEntry, error) {
	snap, err := src.Snapshot(ctx, project, filter)
	if err != nil {
		return snap, nil, err
	}
	if filter.State == "" || filter.State == storage.StateBuilding {
		return snap, filterEntries(snap.Rows, storage.StateBuilding, ""), nil
	}
	filter.State = storage.StateBuilding
	building, err := src.Snapshot(ctx, project, filter)
	return snap, building.Rows, err
}

func (m *watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		return m.handleKey(msg)
	case snapshotMsg:
		m.applySnapshot(msg)
	case watchTickMsg:
		return m, tea.Batch(m.fetch(), m.tick())
	case watchNudgeMsg:
		return m, m.fetch()
	case watchHeartbeatMsg:
		m.workers[msg.WorkerID] = v1.WorkerHeartbeat(msg)
	case watchLogMsg:
		m.appendLog(string(msg))
	}
	return m, nil
}

func (m *watchModel) applySnapshot(msg snapshotMsg) {
	m.loading = false
	if msg.err != nil {
		m.err = msg.err.Error()
		return
	}
	m.err = ""
	m.view.Rows = msg.snap.Rows
	m.view.Counts = msg.snap.Counts
	m.summaries = msg.snap.Summaries
	m.active = make(map[string]PackageStateEntry, len(msg.active))
	for _, e := range msg.active {
		m.active[e.JobID] = e
	}
	m.clampCursor()
}

func (m *watchModel) rows() []PackageStateEntry {
	return attachWorkers(m.view.Visible(true), workerByJob(m.workers))
}

func (m *watchModel) clampCursor() {
	m.cursor = max(0, min(m.cursor, len(m.rows())-1))
}

func (m *watchModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "ctrl+c":
		m.closeLogs()
		return m, tea.Quit
	case m.searching:
		m.handleSearchKey(msg)
	case m.logJob != "":
		m.handleLogKey(key)
	case key == "q":
		return m, tea.Quit
	default:
		return m, m.handleListKey(key)
	}
	return m, nil
}

func (m *watchModel) handleSearchKey(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEnter:
		m.searching = false
	case tea.KeyEsc:
		m.searching = false
		m.view.Search = ""
	case tea.KeyBackspace:
		if r := []rune(m.view.Search); len(r) > 0 {
			m.view.Search = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.view.Search += string(msg.Runes)
	}
	m.clampCursor()
}

func (m *watchModel) handleLogKey(key string) {
	switch key {
	case "esc", "enter", "l", "q":
		m.closeLogs()
	}
}

func (m *watchModel) handleListKey(key string) tea.Cmd {
	switch key {
	case "/":
		m.searching = true
	case "?":
		m.showHelp = !m.showHelp
	case "up", "ctrl+p":
		m.cursor = max(0, m.cursor-1)
	case "down", "ctrl+n":
		m.cursor = max(0, min(m.cursor+1, len(m.rows())-1))
	case "enter", "l":
		m.openLogs()
	default:
		if m.view.ApplyKey(key) {
			m.cursor = 0
			m.loading = false
			return m.fetch()
		}
	}
	return nil
}

func (m *watchModel) appendLog(line string) {
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > watchMaxLogs {
		m.logLines = m.logLines[len(m.logLines)-watchMaxLogs:]
	}
}

func (m *watchModel) openLogs() {
	rows := m.rows()
	if m.logs == nil || m.cursor >= len(rows) || rows[m.cursor].JobID == "" {
		return
	}
	e := rows[m.cursor]
	m.logJob = e.JobID
	m.logLines = nil
	m.appendLog(fmt.Sprintf("--- logs of job %s (%s) ---", e.JobID, entryName(e)))
	for _, l := range m.logs.Saved(e.JobID) {
		m.appendLog(l)
	}
	stop, err := m.logs.Follow(e.JobID, func(l string) { m.send(watchLogMsg(l)) })
	if err != nil {
		m.appendLog(err.Error())
		return
	}
	m.stopLog = stop
}

func (m *watchModel) closeLogs() {
	if m.stopLog != nil {
		if err := m.stopLog(); err != nil {
			m.err = err.Error()
		}
		m.stopLog = nil
	}
	m.logJob = ""
}

func (m *watchModel) View() string {
	var s strings.Builder
	s.WriteString(m.renderHeader() + "\n\n")
	if m.view.Matrix {
		s.WriteString(m.renderMatrix())
	} else {
		s.WriteString(m.renderList())
	}
	s.WriteString("\n" + m.renderFleet())
	if m.showHelp {
		s.WriteString("\n" + subtleStyle.Render("  Legend: "+stateLegend()) + "\n")
	}
	if m.logJob != "" {
		s.WriteString("\n" + m.renderLogs())
	}
	s.WriteString("\n" + m.renderFooter())
	return s.String()
}

func (m *watchModel) renderHeader() string {
	scope := m.project
	if m.repo != "" || m.arch != "" {
		scope += fmt.Sprintf(" (%s/%s)", dashIfEmpty(m.repo), dashIfEmpty(m.arch))
	}
	states := orderedStates(m.view.Counts, watchCountOrder, true)
	parts := make([]string, 0, len(states))
	for _, st := range states {
		label := fmt.Sprintf("%s %d", st, m.view.Counts[st])
		if st == m.view.State && !m.view.Matrix {
			label = "[" + label + "]"
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(watchStateColors[st])).Render(label))
	}
	head := watchHeaderStyle.Render(" bsgo watch ") + " " + scope + "\n" + strings.Join(parts, "  ")
	return head + "\n" + subtleStyle.Render(m.summaryBlock())
}

// summaryBlock lists the repo/arch summaries, at most watchMaxSummary lines.
func (m *watchModel) summaryBlock() string {
	lines := make([]string, 0, len(m.summaries))
	for _, s := range m.summaries {
		lines = append(lines, formatSummaryLine(s))
	}
	if len(lines) > watchMaxSummary {
		lines = append(lines[:watchMaxSummary-1], fmt.Sprintf("... %d more repositories", len(lines)-watchMaxSummary+1))
	}
	return strings.Join(lines, "\n")
}

// renderFleet lists every worker with all of its active jobs.
func (m *watchModel) renderFleet() string {
	fleet := buildFleet(m.workers, m.active, time.Now())
	var lines []string
	for _, w := range fleet {
		lines = append(lines, fmt.Sprintf("  %s %s %s%s", w.ID, w.Arch, w.Status, formatHeartbeatAge(w.HeartbeatAge)))
		for _, j := range w.Jobs {
			lines = append(lines, "    "+formatFleetJob(j))
		}
	}
	if len(lines) == 0 {
		return subtleStyle.Render("  Worker fleet: no workers heartbeating") + "\n"
	}
	if limit := max(4, m.height/4); len(lines) > limit {
		lines = append(lines[:limit-1], fmt.Sprintf("    ... %d more lines", len(lines)-limit+1))
	}
	title := fmt.Sprintf("  Worker fleet (%d)", len(fleet))
	return titleStyle.MarginBottom(0).Render(title) + "\n" + strings.Join(lines, "\n") + "\n"
}

func formatHeartbeatAge(age int) string {
	if age < 0 {
		return ""
	}
	return fmt.Sprintf(" (heartbeat %ds ago)", age)
}

func formatFleetJob(j fleetJob) string {
	if j.Name == "" {
		return shortJobID(j.JobID)
	}
	return fmt.Sprintf("%s %s %s %s", shortJobID(j.JobID), j.Name, j.RepoArch, j.Elapsed)
}

func (m *watchModel) renderList() string {
	rows := m.rows()
	var sb strings.Builder
	if len(rows) == 0 {
		return subtleStyle.Render("  No packages in this state.") + "\n"
	}
	visible := max(3, m.height-10)
	start := max(0, min(m.cursor-visible+1, len(rows)-visible))
	end := min(len(rows), start+visible)
	fmt.Fprintf(&sb, "  %-34s %-22s %-13s %-9s %-9s %s\n", "PACKAGE", "REPO/ARCH", "STATE", "ELAPSED", "JOB", "WORKER")
	for i := start; i < end; i++ {
		e := rows[i]
		prefix := "  "
		if i == m.cursor {
			prefix = watchCursorStyle.Render("▶ ")
		}
		state := lipgloss.NewStyle().Foreground(lipgloss.Color(watchStateColors[e.State])).Render(fmt.Sprintf("%s %-11s", stateToken(e.State), e.State))
		fmt.Fprintf(&sb, "%s%-34s %-22s %s %-9s %-9s %s\n", prefix, truncate(entryName(e), 34),
			truncate(e.Repo+"/"+e.Arch, 22), state, elapsedCell(e), dashIfEmpty(shortJobID(e.JobID)), dashIfEmpty(e.Worker))
	}
	if len(rows) > end-start {
		fmt.Fprintf(&sb, "%s\n", subtleStyle.Render(fmt.Sprintf("  %d-%d of %d", start+1, end, len(rows))))
	}
	return sb.String()
}

func (m *watchModel) renderMatrix() string {
	rows := matrixRows(m.view.Rows)
	if len(rows) == 0 {
		return subtleStyle.Render("  No data.") + "\n"
	}
	cols := orderedStates(m.view.Counts, watchMatrixColumns, true)
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %-10s %7s", "ARCH", "TOTAL")
	for _, c := range cols {
		fmt.Fprintf(&sb, " %12s", c)
	}
	sb.WriteString("\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %-10s %7d", r.Arch, r.Total)
		for _, c := range cols {
			fmt.Fprintf(&sb, " %12d", r.ByState[c])
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m *watchModel) renderLogs() string {
	lines := m.logLines
	if n := max(3, m.height/4); len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
		Render(subtleStyle.Render(strings.Join(lines, "\n"))) + "\n"
}

func (m *watchModel) renderFooter() string {
	switch {
	case m.searching:
		return "  search: " + m.view.Search + "_   (Enter keeps, Esc clears)"
	case m.err != "":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FF3333")).Render("  error: " + m.err)
	}
	footer := subtleStyle.Render("  " + watchFooter)
	if m.view.Search != "" {
		footer += "  search=" + m.view.Search
	}
	if m.note != "" {
		footer += "\n" + subtleStyle.Render("  "+m.note)
	}
	return footer
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
