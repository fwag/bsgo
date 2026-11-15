package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type fakeLogs struct {
	followed string
	stopped  bool
}

func (f *fakeLogs) Saved(string) []string { return []string{"saved line"} }
func (f *fakeLogs) Follow(job string, _ func(string)) (func() error, error) {
	f.followed = job
	return func() error { f.stopped = true; return nil }, nil
}

func newTestWatchModel(logs logSource) *watchModel {
	m := newWatchModel(context.Background(), fixedSource{rows: sampleEntries()}, logs, watchParams{Project: "p", Interval: 1}, "building")
	m.applySnapshot(snapshotMsg{snap: watchSnapshot{
		Counts: map[string]int{"building": 3, "failed": 1, "scheduled": 1, "unresolvable": 1},
		Rows:   sampleEntries(),
	}})
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(m *watchModel, keys ...string) {
	for _, k := range keys {
		m.Update(key(k))
	}
}

func TestWatchModelStateSwitching(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{"default is building, longest first", nil, []string{"glibc@x86_64", "zlib@x86_64", "python:py312@x86_64"}},
		{"failed", []string{"f"}, []string{"glibc@aarch64"}},
		{"all, longest building first", []string{"a"}, []string{"glibc@x86_64", "zlib@x86_64", "python:py312@x86_64", "bash@aarch64", "glibc@aarch64", "python:py311@x86_64"}},
		{"search narrows", []string{"/", "z", "l", "enter"}, []string{"zlib@x86_64"}},
		{"search esc clears", []string{"/", "z", "esc"}, []string{"glibc@x86_64", "zlib@x86_64", "python:py312@x86_64"}},
		{"keys are text while searching", []string{"/", "f", "enter"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestWatchModel(nil)
			send(m, tt.keys...)
			if got := names(m.rows()); strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWatchModelViewShowsCountsAndWorker(t *testing.T) {
	m := newTestWatchModel(nil)
	m.Update(watchHeartbeatMsg(v1.WorkerHeartbeat{WorkerID: "worker-7", ActiveJobIDs: []string{"job-glibc-5678"}}))
	out := m.View()
	for _, want := range []string{"building 3", "failed 1", "glibc", "1h01m", "job-glib", "worker-7", "[m] matrix"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	send(m, "m")
	if out := m.View(); !strings.Contains(out, "ARCH") || !strings.Contains(out, "aarch64") {
		t.Errorf("matrix view missing:\n%s", out)
	}
}

func TestWatchModelLogs(t *testing.T) {
	logs := &fakeLogs{}
	m := newTestWatchModel(logs)
	send(m, "enter")
	if logs.followed != "job-glibc-5678" || m.logJob == "" {
		t.Fatalf("expected logs of the selected job, followed=%q", logs.followed)
	}
	m.Update(watchLogMsg("live line"))
	if v := m.View(); !strings.Contains(v, "live line") || !strings.Contains(v, "saved line") {
		t.Errorf("log lines missing:\n%s", v)
	}
	send(m, "esc")
	if !logs.stopped || m.logJob != "" {
		t.Error("closing logs must stop the subscription")
	}
}

func TestWatchModelCursorAndSnapshotError(t *testing.T) {
	m := newTestWatchModel(nil)
	send(m, "down", "down", "down", "down")
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want clamped to 2", m.cursor)
	}
	m.applySnapshot(snapshotMsg{err: context.DeadlineExceeded})
	if !strings.Contains(m.View(), "error:") || len(m.rows()) != 3 {
		t.Error("an error must be shown and the last good rows kept")
	}
}

func TestWatchModelFleetSummaryAndLegend(t *testing.T) {
	m := newTestWatchModel(nil)
	m.applySnapshot(snapshotMsg{
		snap: watchSnapshot{
			Counts:    map[string]int{"building": 3, "quarantined": 2},
			Summaries: []RepoArchSummary{{Repo: "standard", Arch: "x86_64", Counts: map[string]int{"building": 3, "succeeded": 9}}},
			Rows:      sampleEntries(),
		},
		active: []PackageStateEntry{sampleEntries()[1]},
	})
	m.Update(watchHeartbeatMsg(v1.WorkerHeartbeat{WorkerID: "worker-7", Architecture: "x86_64", Status: "BUSY",
		ActiveJobIDs: []string{"job-glibc-5678", "job-unknown"}}))
	out := m.View()
	for _, want := range []string{"standard x86_64: .9 %3", "quarantined 2", "Worker fleet (1)", "job-glib glibc standard/x86_64 1h01m", "job-unkn"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Legend:") {
		t.Error("legend must be hidden by default")
	}
	send(m, "?")
	if !strings.Contains(m.View(), "Legend: .=succeeded") {
		t.Errorf("legend not shown:\n%s", m.View())
	}
}

func TestStoreStateSourceSummaries(t *testing.T) {
	store := storage.NewMemoryStore()
	ctx := context.Background()
	prj := &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "p"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Architectures: []string{"x86_64", "aarch64"}},
			"images":   {Architectures: []string{"x86_64"}},
		}}
	if err := store.CreateProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	err := store.UpsertBuildStates(ctx, []*storage.PackageBuildState{
		{Project: "p", Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateBuilding},
		{Project: "p", Repo: "standard", Arch: "x86_64", Package: "b", State: storage.StateSucceeded},
		{Project: "p", Repo: "images", Arch: "x86_64", Package: "a", State: storage.StateFailed},
	})
	if err != nil {
		t.Fatal(err)
	}
	src := storeStateSource{store: store, now: time.Now}
	snap, err := src.Snapshot(ctx, "p", storage.BuildStateFilter{State: storage.StateBuilding})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, s := range snap.Summaries {
		lines = append(lines, formatSummaryLine(s))
	}
	want := "images x86_64: F1|standard aarch64:|standard x86_64: .1 %1"
	if got := strings.Join(lines, "|"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(snap.Rows) != 1 || snap.Counts["succeeded"] != 1 {
		t.Errorf("rows must be narrowed by state, counts not: %+v", snap)
	}
	snap, err = src.Snapshot(ctx, "p", storage.BuildStateFilter{Repo: "images"})
	if err != nil || len(snap.Summaries) != 1 {
		t.Errorf("repo filter must narrow summaries: %+v, %v", snap.Summaries, err)
	}
}
