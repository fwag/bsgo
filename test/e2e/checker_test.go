package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

const bootBase = "http://boot.example/1.1/"

type memRecipes struct{ files map[string]fetch.Files }

func (m memRecipes) Fetch(_ context.Context, url, commit string) (fetch.Files, error) {
	f, ok := m.files[url+"@"+commit]
	if !ok {
		return nil, fmt.Errorf("unknown source %s@%s", url, commit)
	}
	return f, nil
}

type staticDoD struct{ blob solv.RepoBlob }

func (s staticDoD) Get(context.Context, string) (solv.RepoBlob, error) { return s.blob, nil }

type collectingSink struct {
	mu   sync.Mutex
	jobs []*checker.JobSpec
}

func (s *collectingSink) Emit(_ context.Context, j *checker.JobSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, j)
	return nil
}
func (s *collectingSink) Cancel(context.Context, string, string) error { return nil }
func (s *collectingSink) take() []*checker.JobSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.jobs
	s.jobs = nil
	return out
}

type directNotifier struct{ events *checker.Events }

func (n directNotifier) RepoChanged(ctx context.Context, p buildrepo.Prpa) error {
	return n.events.BinariesIntegrated(ctx, p)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func bootRepoSolv(t *testing.T, names ...string) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">`)
	for _, n := range names {
		fmt.Fprintf(&b, `<package type="rpm"><name>%[1]s</name><arch>x86_64</arch><version epoch="0" ver="0" rel="1"/>`+
			`<checksum type="sha256" pkgid="YES">%[2]s</checksum><location href="x86_64/%[1]s-0-1.x86_64.rpm"/>`+
			`<format><rpm:provides><rpm:entry name="%[1]s" flags="EQ" epoch="0" ver="0" rel="1"/></rpm:provides></format></package>`,
			n, sum("boot"+n))
	}
	b.WriteString(`</metadata>`)
	blob, err := solv.RPMMDToSolv("boot", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func specFile(name string, requires ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name: %s\nVersion: 1\nRelease: 0\nSummary: s\nLicense: MIT\n", name)
	for _, r := range requires {
		fmt.Fprintf(&b, "BuildRequires: %s\n", r)
	}
	b.WriteString("%description\nx\n")
	return b.String()
}

type fixture struct {
	files map[string]fetch.Files
	pkgs  []*manifest.PackageManifest
}

func (f *fixture) add(name string, files map[string]string) {
	ff := fetch.Files{}
	var key strings.Builder
	for n, c := range files {
		ff[n] = []byte(c)
		key.WriteString(n + c)
	}
	commit := sum(name + key.String())[:40]
	url := "https://git.example/" + name
	f.files[url+"@"+commit] = ff
	f.pkgs = append(f.pkgs, &manifest.PackageManifest{
		SchemaVersion: "v1", Package: manifest.PackageInfo{Name: name},
		Source: manifest.PackageSource{Git: url, Commit: commit}, Build: manifest.PackageBuild{Enabled: true},
	})
}

// TestCheckerEndToEnd drives a small project through the checker, the indexer
// and the job status handler against PostgreSQL and NATS KV, with a fake job
// runner that "builds" by emitting synthetic bininfo.
func TestCheckerEndToEnd(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://bsgo:password@localhost:5432/bsgodb?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := postgres.NewPostgresStore(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping checker integration test: %v", err)
	}
	defer store.Close()

	_, natsURL := runEmbeddedNATS(t)
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	coordinator, err := coord.NewNATS(ctx, js, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	project := fmt.Sprintf("e2e:Checker%d", time.Now().UnixNano())
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: project},
		Targets: map[string]manifest.BuildTarget{"standard": {
			Architectures: []string{"x86_64"},
			Dependencies:  []manifest.DependencySource{{URL: bootBase, Name: "boot:1.1/standard"}},
		}},
	}
	if err := store.UpsertProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	fx := &fixture{files: map[string]fetch.Files{}}
	fx.add("a", map[string]string{"a.spec": specFile("a")})
	fx.add("b", map[string]string{"b.spec": specFile("b", "a")})
	fx.add("c", map[string]string{"c.spec": specFile("c", "b")})
	fx.add("x", map[string]string{"x.spec": specFile("x", "y")})
	fx.add("y", map[string]string{"y.spec": specFile("y", "x")})
	fx.add("m", map[string]string{"m.spec": specFile("m"), "_multibuild": "<multibuild><flavor>mini</flavor></multibuild>"})
	fx.add("u", map[string]string{"u.spec": specFile("u", "does-not-exist")})
	for _, p := range fx.pkgs {
		if err := store.UpsertPackage(ctx, project, p); err != nil {
			t.Fatal(err)
		}
	}

	blobs := buildrepo.NewMemoryBlobs()
	reader := buildrepo.NewReader(store, blobs)
	events := &checker.Events{Projects: store, Marker: coordinator}
	ix := &indexer.Indexer{Pointers: store, Blobs: blobs, Reader: reader, Leases: coordinator, Owner: "e2e", Notify: directNotifier{events}}
	handler := &jobstatus.Handler{Store: store, Integrator: ix, Dirty: events}
	sink := &collectingSink{}
	chk := checker.New(checker.Deps{
		Store: store, Recipes: memRecipes{fx.files}, Indexes: reader,
		DoD:  staticDoD{solv.RepoBlob{Name: "boot", Data: bootRepoSolv(t, "x", "y")}},
		Sink: sink,
	})
	defer chk.Close()
	runner := &coord.Runner{
		Coord: coordinator, Owner: "e2e",
		Process: func(ctx context.Context, prpa string) (time.Duration, error) {
			p, err := buildrepo.ParsePrpa(prpa)
			if err != nil {
				return 0, err
			}
			res, err := chk.Run(ctx, p)
			if err != nil {
				return 0, err
			}
			return res.RetryAfter, nil
		},
	}
	if err := events.All(ctx); err != nil {
		t.Fatal(err)
	}

	built := map[string]int{}
	var order []string
	for range 40 {
		if _, err := runner.Step(ctx); err != nil {
			t.Fatal(err)
		}
		jobs := sink.take()
		for _, j := range jobs {
			built[j.Package]++
			order = append(order, j.Package)
			fakeBuild(ctx, t, handler, blobs, j)
		}
		if dirty, _ := coordinator.Dirty(ctx); len(jobs) == 0 && len(dirty) == 0 {
			break
		}
	}

	for _, u := range []string{"a", "b", "c", "x", "y", "m", "m:mini"} {
		if built[u] != 1 {
			t.Errorf("unit %s built %d times, want exactly once (order %v)", u, built[u], order)
		}
	}
	if built["u"] != 0 {
		t.Errorf("unresolvable package was built: %v", order)
	}
	assertBefore(t, order, "a", "b")
	assertBefore(t, order, "b", "c")

	states, err := store.ListBuildStates(ctx, project, storage.BuildStateFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, st := range states {
		got[buildrepo.UnitName(st.Package, st.Flavor)] = st.State + "|" + st.Details
	}
	for _, u := range []string{"a", "b", "c", "x", "y", "m", "m:mini"} {
		if got[u] != storage.StateSucceeded+"|" {
			t.Errorf("%s is %q, want succeeded", u, got[u])
		}
	}
	if !strings.HasPrefix(got["u"], storage.StateUnresolvable+"|nothing provides does-not-exist") {
		t.Errorf("u is %q, want unresolvable with the OBS text", got["u"])
	}
	idx, err := reader.Index(ctx, buildrepo.Prpa{Project: project, Repo: "standard", Arch: "x86_64"})
	if err != nil || len(idx.Units()) != 7 {
		t.Errorf("build repository units %v (%v), want the 7 built units", idx.Units(), err)
	}
}

func assertBefore(t *testing.T, order []string, first, second string) {
	t.Helper()
	pos := map[string]int{}
	for i, u := range order {
		if _, ok := pos[u]; !ok {
			pos[u] = i
		}
	}
	if pos[first] >= pos[second] {
		t.Errorf("%s must be built before %s, got %v", first, second, order)
	}
}

// fakeBuild plays worker and status consumer: the job "builds" one binary
// named like its unit and reports success with the artifact list.
func fakeBuild(ctx context.Context, t *testing.T, h *jobstatus.Handler, blobs *buildrepo.MemoryBlobs, j *checker.JobSpec) {
	t.Helper()
	status := func(phase string, extra map[string]any) {
		msg := map[string]any{
			"job_id": j.JobID, "project": j.Project, "package": j.Package, "target": j.Repo,
			"architecture": j.Arch, "phase": phase,
		}
		for k, v := range extra {
			msg[k] = v
		}
		data, _ := json.Marshal(msg)
		if err := h.Handle(ctx, data); err != nil {
			t.Fatalf("status %s of %s: %v", phase, j.Package, err)
		}
	}
	status("BUILDING", nil)

	name := strings.ReplaceAll(j.Package, ":", "-")
	bi := bininfo.BinInfo{
		Name: name, Version: "1", Release: j.Release, Arch: j.Arch,
		Filename: name + "-1-" + j.Release + "." + j.Arch + ".rpm", SHA256: sum(j.JobID + name), Size: 10,
	}
	prefix := buildrepo.ArtifactPrefix(buildrepo.Prpa{Project: j.Project, Repo: j.Repo, Arch: j.Arch}, j.Package)
	raw, err := bininfo.Marshal([]bininfo.BinInfo{bi})
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.PutObject(ctx, prefix+bininfo.FileName, raw); err != nil {
		t.Fatal(err)
	}
	status("SUCCEEDED", map[string]any{
		"source_commit": j.SourceCommit, "meta": j.Meta, "release": j.Release,
		"artifacts": []indexer.Artifact{
			{Filename: bininfo.FileName, S3Key: prefix + bininfo.FileName},
			{Filename: bi.Filename, S3Key: prefix + bi.Filename, SHA256: bi.SHA256, Size: bi.Size},
		},
	})
}
