package e2e_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// TestMultiArchBaselibsExport runs the OBS baselibs flow: the i586 build of
// glibc produces glibc-32bit as an x86_64 RPM, the ExportFilter moves it into
// the x86_64 build repository, and only then can a package that needs it be
// built for x86_64.
func TestMultiArchBaselibsExport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store := storage.NewMemoryStore()
	project := "e2e:MultiArch"
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: project},
		Targets: map[string]manifest.BuildTarget{"standard": {Architectures: []string{"x86_64", "i586"}}},
		PublicConfig: &manifest.ProjectConfig{
			ExportFilter: []string{`\.x86_64\.rpm$ x86_64`, `-ia32-.*\.rpm$`},
		},
	}
	if err := store.UpsertProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	fx := &fixture{files: map[string]fetch.Files{}}
	fx.add("glibc", map[string]string{"glibc.spec": specFile("glibc")})
	fx.add("app", map[string]string{"app.spec": "ExclusiveArch: x86_64\n" + specFile("app", "glibc-32bit")})
	for _, p := range fx.pkgs {
		if err := store.UpsertPackage(ctx, project, p); err != nil {
			t.Fatal(err)
		}
	}
	resolveRevisions(t, store, project)

	blobs := buildrepo.NewMemoryBlobs()
	reader := buildrepo.NewReader(store, blobs)
	coordinator := &coord.Memory{}
	events := &checker.Events{Projects: store, Marker: coordinator}
	ix := &indexer.Indexer{
		Pointers: store, Blobs: blobs, Reader: reader, Leases: coordinator, Owner: "e2e",
		Notify: directNotifier{events}, Exports: indexer.ManifestExports{Projects: store},
	}
	handler := &jobstatus.Handler{Store: store, Integrator: ix, Dirty: events}
	sink := &collectingSink{}
	chk := checker.New(checker.Deps{Store: store, Recipes: memRecipes{fx.files}, Indexes: reader, Sink: sink})
	defer chk.Close()

	x86 := buildrepo.Prpa{Project: project, Repo: "standard", Arch: "x86_64"}
	i586 := buildrepo.Prpa{Project: project, Repo: "standard", Arch: "i586"}
	jobIDs := map[string]string{}
	pass := func(p buildrepo.Prpa) []string {
		t.Helper()
		if _, err := chk.Run(ctx, p); err != nil {
			t.Fatal(err)
		}
		var pkgs []string
		for _, j := range sink.take() {
			pkgs = append(pkgs, j.Package+"@"+j.Arch)
			jobIDs[j.Package+"@"+j.Arch] = j.JobID
		}
		return pkgs
	}

	// x86_64: glibc builds, app is unresolvable until glibc-32bit exists.
	got := strings.Join(pass(x86), ",")
	if got != "glibc@x86_64" {
		t.Fatalf("first x86_64 pass scheduled %q, want glibc@x86_64", got)
	}
	jobs := pass(i586)
	if len(jobs) != 1 || jobs[0] != "glibc@i586" {
		t.Fatalf("i586 pass scheduled %v, want glibc@i586", jobs)
	}
	finishI586Glibc(ctx, t, handler, blobs, project, jobIDs["glibc@i586"])

	idx, err := reader.Index(ctx, x86)
	if err != nil {
		t.Fatal(err)
	}
	units := idx.Units()
	if len(units[buildrepo.ImportUnit("glibc", "i586")]) != 1 {
		t.Fatalf("x86_64 build repository units %v, want the glibc:import:i586 unit with glibc-32bit", units)
	}
	own, err := reader.Index(ctx, i586)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(own.Units()["glibc"]); n != 1 {
		t.Errorf("i586 repository holds %d glibc binaries, want only the i686 one", n)
	}

	// The import marked x86_64 dirty; the next pass schedules app.
	dirty, err := coordinator.Dirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirtyHas(dirty, x86.String()) {
		t.Errorf("x86_64 prpa not marked dirty after the import: %v", dirty)
	}
	if got := pass(x86); !contains(got, "app@x86_64") {
		t.Errorf("after the import x86_64 scheduled %v, want app@x86_64", got)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func dirtyHas(marks []coord.Mark, prpa string) bool {
	for _, m := range marks {
		if m.Prpa == prpa {
			return true
		}
	}
	return false
}

func finishI586Glibc(ctx context.Context, t *testing.T, h *jobstatus.Handler, blobs *buildrepo.MemoryBlobs, project, jobID string) {
	t.Helper()
	p := buildrepo.Prpa{Project: project, Repo: "standard", Arch: "i586"}
	prefix := buildrepo.ArtifactPrefix(p, "glibc")
	bins := []bininfo.BinInfo{
		{Name: "glibc", Version: "1", Release: "1", Arch: "i686", Filename: "glibc-1-1.i686.rpm"},
		{Name: "glibc-32bit", Version: "1", Release: "1", Arch: "x86_64", Filename: "glibc-32bit-1-1.x86_64.rpm"},
		{Name: "glibc-ia32", Version: "1", Release: "1", Arch: "i586", Filename: "glibc-ia32-1-1.i586.rpm"},
	}
	arts := []indexer.Artifact{}
	for i := range bins {
		bins[i].SHA256, bins[i].Size = sum(bins[i].Filename), 10
		arts = append(arts, indexer.Artifact{Filename: bins[i].Filename, S3Key: prefix + bins[i].Filename, SHA256: bins[i].SHA256, Size: 10})
	}
	raw, err := bininfo.Marshal(bins)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.PutObject(ctx, prefix+bininfo.FileName, raw); err != nil {
		t.Fatal(err)
	}
	arts = append(arts, indexer.Artifact{Filename: bininfo.FileName, S3Key: prefix + bininfo.FileName})
	for _, phase := range []string{"BUILDING", "SUCCEEDED"} {
		msg := map[string]any{
			"job_id": jobID, "project": project, "package": "glibc", "target": "standard",
			"architecture": "i586", "phase": phase,
		}
		if phase == "SUCCEEDED" {
			msg["artifacts"] = arts
		}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Handle(ctx, data); err != nil {
			t.Fatalf("status %s: %v", phase, err)
		}
	}
}
