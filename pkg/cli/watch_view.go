package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// watchAllStates is the --state value selecting every state.
const watchAllStates = "all"

// watchStateKeys maps a TUI key to the build state it selects. The empty
// state means "all states".
var watchStateKeys = map[string]string{
	"b": storage.StateBuilding,
	"s": storage.StateScheduled,
	"f": storage.StateFailed,
	"u": storage.StateUnresolvable,
	"k": storage.StateBlocked,
	"a": "",
}

// watchCountOrder is the order in which per-state counts are listed.
var watchCountOrder = []string{
	storage.StateBuilding, storage.StateScheduled, storage.StateFailed,
	storage.StateUnresolvable, storage.StateBlocked, storage.StateSucceeded,
}

// watchView is the pure view model of `bsgo watch`: the last snapshot of the
// build state table plus the selected state filter, search text and mode.
type watchView struct {
	// State is the selected build state; empty selects all states.
	State  string
	Search string
	Matrix bool
	Rows   []PackageStateEntry
	Counts map[string]int
}

// ApplyKey applies a state or mode key. It reports whether the key was
// handled, in which case the caller refreshes the snapshot because the set of
// rows to fetch changed.
func (v *watchView) ApplyKey(key string) bool {
	if key == "m" {
		v.Matrix = !v.Matrix
		return true
	}
	state, ok := watchStateKeys[key]
	if !ok {
		return false
	}
	v.Matrix = false
	v.State = state
	return true
}

// FetchState is the state the store query is narrowed to: the matrix needs
// every state, the list only the selected one.
func (v *watchView) FetchState() string {
	if v.Matrix {
		return ""
	}
	return v.State
}

// Visible returns the rows of the selected state matching the search,
// sorted. byElapsed puts the longest-building packages first.
func (v *watchView) Visible(byElapsed bool) []PackageStateEntry {
	return sortEntries(filterEntries(v.Rows, v.State, v.Search), byElapsed)
}

// parseWatchState validates a --state value; "all" and "" select every state.
func parseWatchState(s string) (string, error) {
	switch {
	case s == "" || s == watchAllStates:
		return "", nil
	case storage.IsValidBuildState(s):
		return s, nil
	}
	return "", fmt.Errorf("invalid --state %q (valid: %s, %s)", s, watchAllStates, strings.Join(storage.ValidBuildStates, ", "))
}

// entryName is the display name of a package unit: package[:flavor].
func entryName(e PackageStateEntry) string {
	if e.Flavor == "" {
		return e.Package
	}
	return e.Package + ":" + e.Flavor
}

func filterEntries(rows []PackageStateEntry, state, search string) []PackageStateEntry {
	needle := strings.ToLower(search)
	out := make([]PackageStateEntry, 0, len(rows))
	for _, e := range rows {
		if state != "" && e.State != state {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(entryName(e)), needle) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// sortEntries returns a sorted copy: by package, flavor, repo, arch; with
// byElapsed the longest-building entries come first.
func sortEntries(rows []PackageStateEntry, byElapsed bool) []PackageStateEntry {
	out := append([]PackageStateEntry(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if byElapsed && a.BuildingSeconds != b.BuildingSeconds {
			return a.BuildingSeconds > b.BuildingSeconds
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		if a.Flavor != b.Flavor {
			return a.Flavor < b.Flavor
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Arch < b.Arch
	})
	return out
}

// attachWorkers sets Worker on building entries whose job is known.
func attachWorkers(rows []PackageStateEntry, workers map[string]string) []PackageStateEntry {
	out := append([]PackageStateEntry(nil), rows...)
	for i := range out {
		if w, ok := workers[out[i].JobID]; ok && out[i].State == storage.StateBuilding {
			out[i].Worker = w
		}
	}
	return out
}

// workerByJob maps each active job id to the worker reporting it.
func workerByJob(heartbeats map[string]v1.WorkerHeartbeat) map[string]string {
	out := make(map[string]string)
	for id, hb := range heartbeats {
		jobs := append([]string(nil), hb.ActiveJobIDs...)
		if hb.ActiveJobID != "" {
			jobs = append(jobs, hb.ActiveJobID)
		}
		for _, j := range jobs {
			out[j] = id
		}
	}
	return out
}

// formatElapsed renders seconds as 9s, 47m12s or 3h05m.
func formatElapsed(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func shortJobID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// stateLetters maps build states to the letter codes of OBS (osc results).
// States are data: any state not listed here is shown as stateUnknownLetter,
// so states the backend starts emitting later appear without a code change.
var stateLetters = map[string]string{
	"succeeded":    ".",
	"disabled":     " ",
	"unresolvable": "U",
	"failed":       "F",
	"broken":       "B",
	"blocked":      "b",
	"building":     "%",
	"finished":     "f",
	"scheduled":    "s",
	"locked":       "L",
	"excluded":     "x",
	"dispatching":  "d",
	"signing":      "S",
}

const stateUnknownLetter = "?"

// stateLegendOrder is the order of the legend.
var stateLegendOrder = []string{
	"succeeded", "disabled", "unresolvable", "failed", "broken", "blocked",
	"building", "finished", "scheduled", "locked", "excluded", "dispatching", "signing",
}

// stateLetter is the OBS letter code of a state.
func stateLetter(state string) string {
	if l, ok := stateLetters[state]; ok {
		return l
	}
	return stateUnknownLetter
}

// stateToken is the letter as printed in summaries: the space of disabled is
// shown as an underscore so that it stays visible.
func stateToken(state string) string {
	if l := stateLetter(state); l != " " {
		return l
	}
	return "_"
}

// stateLegend explains the letter codes on one line.
func stateLegend() string {
	parts := make([]string, 0, len(stateLegendOrder)+1)
	for _, s := range stateLegendOrder {
		parts = append(parts, stateToken(s)+"="+s)
	}
	parts = append(parts, stateUnknownLetter+"=no state or unknown")
	return strings.Join(parts, " ")
}

// orderedStates lists the states of counts: the usual ones first (even when
// zero if includeZero), then any other state sorted by name.
func orderedStates(counts map[string]int, known []string, includeZero bool) []string {
	var out []string
	seen := make(map[string]bool)
	for _, s := range known {
		seen[s] = true
		if includeZero || counts[s] > 0 {
			out = append(out, s)
		}
	}
	var extra []string
	for s, n := range counts {
		if !seen[s] && n > 0 {
			extra = append(extra, s)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// formatCounts lists the per-state counts on one plain line.
func formatCounts(counts map[string]int) string {
	states := orderedStates(counts, watchCountOrder, true)
	parts := make([]string, 0, len(states))
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%s=%d", s, counts[s]))
	}
	return strings.Join(parts, " ")
}

func sortSummaries(list []RepoArchSummary) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Repo != list[j].Repo {
			return list[i].Repo < list[j].Repo
		}
		return list[i].Arch < list[j].Arch
	})
}

// formatSummaryLine renders "<repo> <arch>: <letter><count> ..." like the
// right column of osc results; a known repository state follows in brackets.
func formatSummaryLine(s RepoArchSummary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s", s.Repo, s.Arch)
	if s.RepoState != "" {
		fmt.Fprintf(&sb, " [%s]", s.RepoState)
	}
	sb.WriteString(":")
	for _, st := range orderedStates(s.Counts, stateLegendOrder, false) {
		fmt.Fprintf(&sb, " %s%d", stateToken(st), s.Counts[st])
	}
	return sb.String()
}

// formatSummaryLines renders one line per repo/arch, each newline terminated.
func formatSummaryLines(list []RepoArchSummary) string {
	var sb strings.Builder
	for _, s := range list {
		sb.WriteString(formatSummaryLine(s) + "\n")
	}
	return sb.String()
}

// fleetJob is one active job of a worker.
type fleetJob struct {
	JobID    string
	Name     string // package[:flavor], empty when the job is not in the state table
	RepoArch string
	Elapsed  string
}

// fleetWorker is one worker with all of its active jobs.
type fleetWorker struct {
	ID, Arch, Status string
	// HeartbeatAge is the seconds since the last heartbeat, -1 when unknown.
	HeartbeatAge int
	Jobs         []fleetJob
}

// workerStaleAfter is how long after its last heartbeat a worker is dropped.
const workerStaleAfter = 60 * time.Second

// buildFleet lists the live workers sorted by id with every active job. active
// maps job ids to their state table entry for names and elapsed time.
func buildFleet(heartbeats map[string]v1.WorkerHeartbeat, active map[string]PackageStateEntry, now time.Time) []fleetWorker {
	out := make([]fleetWorker, 0, len(heartbeats))
	for id, hb := range heartbeats {
		if hb.TimestampUnix > 0 && now.Sub(time.Unix(hb.TimestampUnix, 0)) > workerStaleAfter {
			continue
		}
		w := fleetWorker{ID: id, Arch: hb.Architecture, Status: hb.Status, HeartbeatAge: -1}
		if hb.TimestampUnix > 0 {
			w.HeartbeatAge = max(0, int(now.Unix()-hb.TimestampUnix))
		}
		jobs := append([]string(nil), hb.ActiveJobIDs...)
		if hb.ActiveJobID != "" && !containsString(jobs, hb.ActiveJobID) {
			jobs = append(jobs, hb.ActiveJobID)
		}
		sort.Strings(jobs)
		for _, j := range jobs {
			w.Jobs = append(w.Jobs, fleetJobFor(j, active))
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func fleetJobFor(jobID string, active map[string]PackageStateEntry) fleetJob {
	j := fleetJob{JobID: jobID}
	if e, ok := active[jobID]; ok {
		j.Name = entryName(e)
		j.RepoArch = e.Repo + "/" + e.Arch
		j.Elapsed = formatElapsed(e.BuildingSeconds)
	}
	return j
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func elapsedCell(e PackageStateEntry) string {
	if e.State != storage.StateBuilding {
		return "-"
	}
	return formatElapsed(e.BuildingSeconds)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatEntriesTable renders rows as a plain, undecorated table.
func formatEntriesTable(rows []PackageStateEntry) string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PACKAGE\tREPO/ARCH\tSTATE\tELAPSED\tJOB\tWORKER")
	for _, e := range rows {
		fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%s\t%s\t%s\n", entryName(e), e.Repo, e.Arch,
			e.State, elapsedCell(e), dashIfEmpty(shortJobID(e.JobID)), dashIfEmpty(e.Worker))
	}
	_ = tw.Flush() // writing to a strings.Builder cannot fail
	return strings.TrimRight(sb.String(), "\n")
}

// matrixRow aggregates the entries of one architecture.
type matrixRow struct {
	Arch    string
	Total   int
	ByState map[string]int
}

// matrixRows groups rows by architecture, sorted by architecture name.
func matrixRows(rows []PackageStateEntry) []matrixRow {
	byArch := make(map[string]*matrixRow)
	for _, e := range rows {
		r, ok := byArch[e.Arch]
		if !ok {
			r = &matrixRow{Arch: e.Arch, ByState: make(map[string]int)}
			byArch[e.Arch] = r
		}
		r.Total++
		r.ByState[e.State]++
	}
	out := make([]matrixRow, 0, len(byArch))
	for _, r := range byArch {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Arch < out[j].Arch })
	return out
}
