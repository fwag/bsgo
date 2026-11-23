package checker_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
)

func TestBuildtimeServiceAddsBdeps(t *testing.T) {
	tests := []struct {
		name    string
		service string
		want    []string
	}{
		{"buildtime", `<services><service name="tar" mode="buildtime"/></services>`, []string{"obs-service-tar"}},
		{"localonly", `<services><service name="tar" mode="localonly"/></services>`, nil},
		{"no service file", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t, []bininfo.BinInfo{binary("obs-service-tar", "0")})
			files := map[string]string{"p.spec": spec("p", nil)}
			if tt.service != "" {
				files["_service"] = tt.service
			}
			w.addPackage("p", files)
			w.run()
			var got []string
			for _, b := range w.wantJobs("p")[0].Bdeps {
				got = append(got, b.Name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bdeps %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBuildtimeServiceStaysOutOfMetaAndBlocking: like OBS (btdeps in
// BSSched::BuildJob), the service package joins the job's build expansion
// flagged notmeta, but is neither part of the meta nor a blocking dependency.
func TestBuildtimeServiceStaysOutOfMetaAndBlocking(t *testing.T) {
	const service = `<services><service name="tar" mode="buildtime"/></services>`
	w := newWorld(t, []bininfo.BinInfo{binary("obs-service-tar", "0"), binary("dep", "0")})
	// svc produces obs-service-tar but has not built yet.
	w.pkg("svc", nil, "obs-service-tar")
	w.addPackage("p", map[string]string{"p.spec": spec("p", []string{"dep"}), "_service": service})
	w.run()

	var job *checker.JobSpec
	for _, j := range w.sink.take() {
		if j.Package == "p" {
			job = j
		}
	}
	if job == nil {
		t.Fatalf("no job for p; state %s %q", w.state("p").State, w.state("p").Details)
	}
	notMeta := map[string]bool{}
	for _, b := range job.Bdeps {
		notMeta[b.Name] = b.NotMeta
	}
	if nm, ok := notMeta["obs-service-tar"]; !ok || !nm {
		t.Errorf("obs-service-tar bdep present=%v notmeta=%v, want present and notmeta", ok, nm)
	}
	if nm, ok := notMeta["dep"]; !ok || nm {
		t.Errorf("dep bdep present=%v notmeta=%v, want present and in the meta", ok, nm)
	}
	w.finish(job)
	for _, l := range w.metaLines("p") {
		if strings.Contains(l, "obs-service-tar") {
			t.Errorf("meta mentions the service package: %q", l)
		}
	}
}
