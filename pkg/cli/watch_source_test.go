package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type fakeStatusLog struct {
	msgs map[uint64]string // seq -> JSON; missing seq = deleted
	last uint64
	gets int
	err  error
}

func (f *fakeStatusLog) Last(context.Context) (uint64, error) { return f.last, f.err }

func (f *fakeStatusLog) Get(_ context.Context, seq uint64) ([]byte, time.Time, bool, error) {
	f.gets++
	m, ok := f.msgs[seq]
	return []byte(m), time.Date(2026, 11, 15, 10, 0, int(seq), 0, time.UTC), ok, nil
}

func statusJSON(job, pkg, phase string) string {
	return fmt.Sprintf(`{"job_id":%q,"project":"p","package":%q,"target":"standard","architecture":"x86_64","worker_id":"w1","phase":%q}`, job, pkg, phase)
}

func TestLoadHistory(t *testing.T) {
	log := &fakeStatusLog{last: 8, msgs: map[uint64]string{
		1: statusJSON("old", "glibc", "SUCCEEDED"),
		2: statusJSON("j1", "glibc", "BUILDING"),
		3: statusJSON("j1", "glibc", "FAILED"),
		4: "not json",
		5: statusJSON("other", "zlib", "BUILDING"),
		// 6 deleted
		7: `{"job_id":"j2","project":"other-project","package":"glibc","phase":"BUILDING"}`,
		8: statusJSON("j3", "glibc", "BUILDING"),
	}}
	tests := []struct {
		name  string
		q     historyQuery
		want  []string
		gets  int
		phase map[string]string
	}{
		{"newest first, deduped, other package skipped", historyQuery{Project: "p", Package: "glibc", Limit: 10},
			[]string{"j3", "j1", "old"}, 8, map[string]string{"j1": "FAILED"}},
		{"limit stops the scan early", historyQuery{Project: "p", Package: "glibc", Limit: 1}, []string{"j3"}, 1, nil},
		{"arch filter", historyQuery{Project: "p", Package: "glibc", Arch: "s390x", Limit: 10}, nil, 8, nil},
		{"repo filter", historyQuery{Project: "p", Package: "glibc", Repo: "standard", Limit: 2}, []string{"j3", "j1"}, 6, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log.gets = 0
			got, err := loadHistory(context.Background(), log, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, e := range got {
				ids = append(ids, e.JobID)
				if want, ok := tt.phase[e.JobID]; ok && e.Phase != want {
					t.Errorf("job %s phase = %s, want %s (latest message wins)", e.JobID, e.Phase, want)
				}
			}
			if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Errorf("jobs = %v, want %v", ids, tt.want)
			}
			if log.gets != tt.gets {
				t.Errorf("read %d messages, want %d", log.gets, tt.gets)
			}
		})
	}
}

func TestLoadHistoryError(t *testing.T) {
	_, err := loadHistory(context.Background(), &fakeStatusLog{err: errors.New("boom")}, historyQuery{Limit: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateWatchProject(t *testing.T) {
	store := storage.NewMemoryStore()
	for _, n := range []string{"zeta", "alpha"} {
		if err := store.CreateProject(context.Background(), &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: n}}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		project string
		wantErr string
	}{
		{"alpha", ""},
		{"", "a project is required: bsgo watch <project> (known projects: alpha, zeta)"},
		{"nope", `unknown project "nope" (known projects: alpha, zeta)`},
	}
	for _, tt := range tests {
		err := validateWatchProject(context.Background(), store, tt.project)
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error %v", tt.project, err)
		case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
			t.Errorf("%q: error = %v, want %q", tt.project, err, tt.wantErr)
		}
	}
	err := validateWatchProject(context.Background(), storage.NewMemoryStore(), "x")
	if err == nil || !strings.Contains(err.Error(), "known projects: none") {
		t.Errorf("empty store error = %v", err)
	}
}

func TestResolveWatchProject(t *testing.T) {
	tests := []struct {
		args    []string
		flag    string
		want    string
		wantErr bool
	}{
		{[]string{"a"}, "", "a", false},
		{nil, "b", "b", false},
		{[]string{"a"}, "a", "a", false},
		{[]string{"a"}, "b", "", true},
		{nil, "", "", false},
	}
	for _, tt := range tests {
		got, err := resolveWatchProject(tt.args, tt.flag)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("resolveWatchProject(%v,%q) = %q, %v", tt.args, tt.flag, got, err)
		}
	}
}
