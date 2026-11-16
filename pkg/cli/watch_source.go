package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// RepoArchSummary is the per repository and architecture line of the header,
// like the right column of `osc results`: state counts of every package unit.
type RepoArchSummary struct {
	Repo string `json:"repo"`
	Arch string `json:"arch"`
	// RepoState is the state of the repository itself (published, blocked,
	// ...); empty until the backend recorded one.
	RepoState string         `json:"repo_state,omitempty"`
	Counts    map[string]int `json:"counts"`
}

// watchSnapshot is the current state of a project: per-state counts (ignoring
// the state filter), the per repo/arch summaries and the rows matching the filter.
type watchSnapshot struct {
	Counts    map[string]int
	Summaries []RepoArchSummary
	Rows      []PackageStateEntry
}

// stateSource reads the authoritative build state table.
type stateSource interface {
	Snapshot(ctx context.Context, project string, filter storage.BuildStateFilter) (watchSnapshot, error)
}

// storeStateSource serves snapshots from the control-plane store.
type storeStateSource struct {
	store storage.Store
	now   func() time.Time
}

func (s storeStateSource) Snapshot(ctx context.Context, project string, filter storage.BuildStateFilter) (watchSnapshot, error) {
	countFilter := filter
	countFilter.State = ""
	counts, err := s.store.CountBuildStates(ctx, project, countFilter)
	if err != nil {
		return watchSnapshot{}, fmt.Errorf("failed counting build states: %w", err)
	}
	summaries, err := s.summaries(ctx, project, countFilter)
	if err != nil {
		return watchSnapshot{}, err
	}
	states, err := s.store.ListBuildStates(ctx, project, filter)
	if err != nil {
		return watchSnapshot{}, fmt.Errorf("failed listing build states: %w", err)
	}
	now := s.now()
	rows := make([]PackageStateEntry, 0, len(states))
	for _, st := range states {
		rows = append(rows, stateEntry(st, now))
	}
	return watchSnapshot{Counts: counts, Summaries: summaries, Rows: rows}, nil
}

// summaries counts the states of every repo/arch the project builds for,
// honouring the repo and arch of filter.
func (s storeStateSource) summaries(ctx context.Context, project string, filter storage.BuildStateFilter) ([]RepoArchSummary, error) {
	prj, err := s.store.GetProject(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("failed loading project %s: %w", project, err)
	}
	out := []RepoArchSummary{}
	for repo, target := range prj.Targets {
		for _, arch := range target.Architectures {
			f := storage.BuildStateFilter{Repo: repo, Arch: arch}
			if (filter.Repo != "" && filter.Repo != repo) || (filter.Arch != "" && filter.Arch != arch) {
				continue
			}
			counts, err := s.store.CountBuildStates(ctx, project, f)
			if err != nil {
				return nil, fmt.Errorf("failed counting build states of %s/%s: %w", repo, arch, err)
			}
			repoState, err := s.repoState(ctx, project, repo, arch)
			if err != nil {
				return nil, err
			}
			out = append(out, RepoArchSummary{Repo: repo, Arch: arch, RepoState: repoState, Counts: counts})
		}
	}
	sortSummaries(out)
	return out, nil
}

// repoState returns the recorded repository state, empty when there is none.
func (s storeStateSource) repoState(ctx context.Context, project, repo, arch string) (string, error) {
	st, err := s.store.GetPrpaState(ctx, project, repo, arch)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed reading repository state of %s/%s: %w", repo, arch, err)
	}
	return st.State, nil
}

// validateWatchProject fails unless project exists; the error lists the
// known projects so an agent can correct its call.
func validateWatchProject(ctx context.Context, store storage.Store, project string) error {
	projects, err := store.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("failed listing projects: %w", err)
	}
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.Project.Name == project {
			return nil
		}
		names = append(names, p.Project.Name)
	}
	sort.Strings(names)
	known := "none"
	if len(names) > 0 {
		known = strings.Join(names, ", ")
	}
	if project == "" {
		return fmt.Errorf("a project is required: bsgo watch <project> (known projects: %s)", known)
	}
	return fmt.Errorf("unknown project %q (known projects: %s)", project, known)
}

// logSource provides build logs of a job: what is saved plus a live follow.
type logSource interface {
	Saved(jobID string) []string
	// Follow calls emit for every new log line until stop is called.
	Follow(jobID string, emit func(string)) (stop func() error, err error)
}

// natsLogSource follows bsgo.logs.<jobid> on NATS.
type natsLogSource struct {
	nc *nats.Conn
}

func (n natsLogSource) Saved(jobID string) []string { return readSavedJobLogs(jobID) }

func (n natsLogSource) Follow(jobID string, emit func(string)) (func() error, error) {
	sub, err := n.nc.Subscribe("bsgo.logs."+jobID, func(msg *nats.Msg) { emit(string(msg.Data)) })
	if err != nil {
		return nil, fmt.Errorf("failed subscribing to logs of job %s: %w", jobID, err)
	}
	return sub.Unsubscribe, nil
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

// HistoryEntry is the latest known status of one earlier job of a package.
type HistoryEntry struct {
	JobID        string `json:"job_id"`
	Phase        string `json:"phase"`
	Repo         string `json:"repo,omitempty"`
	Arch         string `json:"arch,omitempty"`
	Worker       string `json:"worker,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Reason       string `json:"reason,omitempty"`
	LastUpdateAt string `json:"last_update_at"`
}

// historyMaxScan bounds how many JOB_STATUS messages one history query reads.
const historyMaxScan = 20000

// statusLog reads the JOB_STATUS stream by sequence number.
type statusLog interface {
	// Last is the highest sequence number in the stream (0 when empty).
	Last(ctx context.Context) (uint64, error)
	// Get returns the message at seq; ok is false if it no longer exists.
	Get(ctx context.Context, seq uint64) (data []byte, at time.Time, ok bool, err error)
}

// jetStreamStatusLog adapts the JOB_STATUS JetStream stream.
type jetStreamStatusLog struct {
	stream jetstream.Stream
}

func (j jetStreamStatusLog) Last(ctx context.Context) (uint64, error) {
	info, err := j.stream.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed reading JOB_STATUS stream info: %w", err)
	}
	return info.State.LastSeq, nil
}

func (j jetStreamStatusLog) Get(ctx context.Context, seq uint64) ([]byte, time.Time, bool, error) {
	msg, err := j.stream.GetMsg(ctx, seq)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("failed reading JOB_STATUS message %d: %w", seq, err)
	}
	return msg.Data, msg.Time, true, nil
}

// historyQuery selects the jobs of one package.
type historyQuery struct {
	Project, Package string
	Repo, Arch       string
	Limit            int
}

type statusMessage struct {
	JobID        string `json:"job_id"`
	Project      string `json:"project"`
	Package      string `json:"package"`
	Target       string `json:"target"`
	Architecture string `json:"architecture"`
	WorkerID     string `json:"worker_id"`
	Phase        string `json:"phase"`
	ExitCode     *int   `json:"exit_code"`
	Reason       string `json:"reason"`
}

// parseHistoryMessage decodes one status message; ok is false when it is
// malformed or belongs to another package or filter.
func parseHistoryMessage(data []byte, at time.Time, q historyQuery) (HistoryEntry, bool) {
	var m statusMessage
	if err := json.Unmarshal(data, &m); err != nil || m.JobID == "" {
		return HistoryEntry{}, false
	}
	if m.Project != q.Project || m.Package != q.Package {
		return HistoryEntry{}, false
	}
	if (q.Repo != "" && m.Target != q.Repo) || (q.Arch != "" && m.Architecture != q.Arch) {
		return HistoryEntry{}, false
	}
	return HistoryEntry{
		JobID:        m.JobID,
		Phase:        m.Phase,
		Repo:         m.Target,
		Arch:         m.Architecture,
		Worker:       m.WorkerID,
		ExitCode:     m.ExitCode,
		Reason:       m.Reason,
		LastUpdateAt: at.UTC().Format("2006-01-02T15:04:05Z"),
	}, true
}

// loadHistory walks the stream from the newest message backwards, reading at
// most historyMaxScan messages, and returns the latest status of up to
// q.Limit distinct jobs of the package, newest first.
func loadHistory(ctx context.Context, log statusLog, q historyQuery) ([]HistoryEntry, error) {
	last, err := log.Last(ctx)
	if err != nil {
		return nil, err
	}
	var out []HistoryEntry
	seen := make(map[string]bool)
	scanned := 0
	for seq := last; seq >= 1 && scanned < historyMaxScan && len(out) < q.Limit; seq-- {
		scanned++
		data, at, ok, err := log.Get(ctx, seq)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		entry, match := parseHistoryMessage(data, at, q)
		if !match || seen[entry.JobID] {
			continue
		}
		seen[entry.JobID] = true
		out = append(out, entry)
	}
	return out, nil
}

func formatHistoryTable(entries []HistoryEntry) string {
	rows := make([]string, 0, len(entries)+1)
	rows = append(rows, fmt.Sprintf("%-10s %-12s %-14s %-10s %-20s %s", "JOB", "PHASE", "REPO", "ARCH", "UPDATED", "WORKER"))
	for _, e := range entries {
		rows = append(rows, fmt.Sprintf("%-10s %-12s %-14s %-10s %-20s %s",
			shortJobID(e.JobID), e.Phase, dashIfEmpty(e.Repo), dashIfEmpty(e.Arch), e.LastUpdateAt, dashIfEmpty(e.Worker)))
	}
	return strings.Join(rows, "\n")
}
