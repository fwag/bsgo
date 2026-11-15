package cli

import (
	"reflect"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func sampleEntries() []PackageStateEntry {
	return []PackageStateEntry{
		{Package: "zlib", Repo: "standard", Arch: "x86_64", State: "building", JobID: "job-zlib-1234", BuildingSeconds: 30},
		{Package: "glibc", Repo: "standard", Arch: "x86_64", State: "building", JobID: "job-glibc-5678", BuildingSeconds: 3700},
		{Package: "glibc", Repo: "standard", Arch: "aarch64", State: "failed", JobID: "j3"},
		{Package: "python", Flavor: "py311", Repo: "standard", Arch: "x86_64", State: "scheduled"},
		{Package: "python", Flavor: "py312", Repo: "standard", Arch: "x86_64", State: "building", BuildingSeconds: 5},
		{Package: "bash", Repo: "standard", Arch: "aarch64", State: "unresolvable"},
	}
}

func names(rows []PackageStateEntry) []string {
	var out []string
	for _, e := range rows {
		out = append(out, entryName(e)+"@"+e.Arch)
	}
	return out
}

func TestWatchViewVisible(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		search    string
		byElapsed bool
		want      []string
	}{
		{"building by elapsed", "building", "", true, []string{"glibc@x86_64", "zlib@x86_64", "python:py312@x86_64"}},
		{"building by name", "building", "", false, []string{"glibc@x86_64", "python:py312@x86_64", "zlib@x86_64"}},
		{"failed", "failed", "", false, []string{"glibc@aarch64"}},
		{"all sorted", "", "", false, []string{"bash@aarch64", "glibc@aarch64", "glibc@x86_64", "python:py311@x86_64", "python:py312@x86_64", "zlib@x86_64"}},
		{"search is case-insensitive", "", "PY", false, []string{"python:py311@x86_64", "python:py312@x86_64"}},
		{"search matches flavor", "", "py312", false, []string{"python:py312@x86_64"}},
		{"search with state", "building", "li", false, []string{"glibc@x86_64", "zlib@x86_64"}},
		{"no match", "scheduled", "zlib", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := watchView{State: tt.state, Search: tt.search, Rows: sampleEntries()}
			if got := names(v.Visible(tt.byElapsed)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWatchViewApplyKey(t *testing.T) {
	tests := []struct {
		key        string
		start      watchView
		wantState  string
		wantMatrix bool
		wantFetch  string
		handled    bool
	}{
		{"b", watchView{State: "failed"}, "building", false, "building", true},
		{"s", watchView{}, "scheduled", false, "scheduled", true},
		{"f", watchView{}, "failed", false, "failed", true},
		{"u", watchView{}, "unresolvable", false, "unresolvable", true},
		{"k", watchView{}, "blocked", false, "blocked", true},
		{"a", watchView{State: "building"}, "", false, "", true},
		{"m", watchView{State: "building"}, "building", true, "", true},
		{"b", watchView{State: "failed", Matrix: true}, "building", false, "building", true},
		{"x", watchView{State: "failed"}, "failed", false, "failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.key+"/"+tt.start.State, func(t *testing.T) {
			v := tt.start
			if got := v.ApplyKey(tt.key); got != tt.handled {
				t.Errorf("handled = %v, want %v", got, tt.handled)
			}
			if v.State != tt.wantState || v.Matrix != tt.wantMatrix || v.FetchState() != tt.wantFetch {
				t.Errorf("state=%q matrix=%v fetch=%q", v.State, v.Matrix, v.FetchState())
			}
		})
	}
}

func TestMatrixRows(t *testing.T) {
	got := matrixRows(sampleEntries())
	if len(got) != 2 || got[0].Arch != "aarch64" || got[1].Arch != "x86_64" {
		t.Fatalf("unexpected rows: %+v", got)
	}
	if got[1].Total != 4 || got[1].ByState["building"] != 3 || got[0].ByState["failed"] != 1 {
		t.Errorf("unexpected counts: %+v", got)
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := map[int64]string{0: "0s", 9: "9s", 60: "1m00s", 2832: "47m12s", 3600: "1h00m", 11100: "3h05m"}
	for in, want := range tests {
		if got := formatElapsed(in); got != want {
			t.Errorf("formatElapsed(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseWatchState(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false}, {"all", "", false}, {"building", "building", false},
		{"blocked", "blocked", false}, {"bogus", "", true},
	}
	for _, tt := range tests {
		got, err := parseWatchState(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseWatchState(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestWorkerByJobAndAttach(t *testing.T) {
	hbs := map[string]v1.WorkerHeartbeat{
		"w1": {WorkerID: "w1", ActiveJobIDs: []string{"job-zlib-1234"}},
		"w2": {WorkerID: "w2", ActiveJobID: "job-glibc-5678"},
	}
	rows := attachWorkers(sampleEntries(), workerByJob(hbs))
	got := map[string]string{}
	for _, e := range rows {
		if e.Worker != "" {
			got[e.Package] = e.Worker
		}
	}
	want := map[string]string{"zlib": "w1", "glibc": "w2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFormatEntriesTableAndCounts(t *testing.T) {
	rows := []PackageStateEntry{{Package: "glibc", Repo: "standard", Arch: "x86_64", State: storage.StateBuilding, JobID: "job-glibc-5678", BuildingSeconds: 2832, Worker: "w2"}}
	want := "PACKAGE  REPO/ARCH        STATE     ELAPSED  JOB       WORKER\n" +
		"glibc    standard/x86_64  building  47m12s   job-glib  w2"
	if got := formatEntriesTable(rows); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := formatCounts(map[string]int{"building": 2, "failed": 1}); got !=
		"building=2 scheduled=0 failed=1 unresolvable=0 blocked=0 succeeded=0" {
		t.Errorf("formatCounts = %q", got)
	}
}
