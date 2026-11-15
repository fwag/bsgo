package cli

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
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
