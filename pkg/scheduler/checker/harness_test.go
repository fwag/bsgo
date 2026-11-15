package checker_test

import (
	"context"
	"crypto/sha1" // #nosec G505 -- test-only derivation of git-like commit ids
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const (
	testProject = "test:Proj"
	testRepo    = "standard"
	testArch    = "x86_64"
	bootURL     = "http://boot.example/1.1/"
)

type fakeRecipes struct {
	mu    sync.Mutex
	files map[string]fetch.Files
	err   error
}

func (f *fakeRecipes) Fetch(_ context.Context, url, commit string) (fetch.Files, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	files, ok := f.files[url+"@"+commit]
	if !ok {
		return nil, fmt.Errorf("no such commit %s", commit)
	}
	return files, nil
}

type fakeIndexes struct {
	mu  sync.Mutex
	idx map[string]*buildrepo.Index
}

func (f *fakeIndexes) Index(_ context.Context, p checker.Prpa) (*buildrepo.Index, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.idx[p.String()]; ok {
		return i, nil
	}
	return &buildrepo.Index{}, nil
}

type fakeDoD struct{ blob solv.RepoBlob }

func (f *fakeDoD) Get(_ context.Context, url string) (solv.RepoBlob, error) {
	if url != bootURL {
		return solv.RepoBlob{}, fmt.Errorf("unexpected url %s", url)
	}
	return f.blob, nil
}

type recordingSink struct {
	mu        sync.Mutex
	jobs      []*checker.JobSpec
	cancelled []string
}

func (s *recordingSink) Emit(_ context.Context, j *checker.JobSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, j)
	return nil
}

func (s *recordingSink) Cancel(_ context.Context, id, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = append(s.cancelled, id)
	return nil
}

func (s *recordingSink) take() []*checker.JobSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.jobs
	s.jobs = nil
	return out
}

// world is a complete in-memory scheduler environment for one project.
type world struct {
	t       *testing.T
	store   *storage.MemoryStore
	recipes *fakeRecipes
	indexes *fakeIndexes
	sink    *recordingSink
	chk     *checker.Checker
	prpa    checker.Prpa
	// entries is the content of the own build repository by unit.
	entries map[string][]buildrepo.Entry
	prj     *manifest.ProjectManifest
}

type worldOpt func(*world)

func newWorld(t *testing.T, boot []bininfo.BinInfo, opts ...worldOpt) *world {
	t.Helper()
	w := &world{
		t: t, store: storage.NewMemoryStore(),
		recipes: &fakeRecipes{files: map[string]fetch.Files{}},
		indexes: &fakeIndexes{idx: map[string]*buildrepo.Index{}},
		sink:    &recordingSink{},
		prpa:    checker.Prpa{Project: testProject, Repo: testRepo, Arch: testArch},
		entries: map[string][]buildrepo.Entry{},
	}
	target := manifest.BuildTarget{Architectures: []string{testArch}}
	var dd *fakeDoD
	if boot != nil {
		target.Dependencies = []manifest.DependencySource{{URL: bootURL, Name: "boot:1.1/standard"}}
		dd = &fakeDoD{blob: solv.RepoBlob{Name: "boot", Data: rpmmdSolv(t, boot)}}
	}
	w.prj = &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: testProject},
		Targets:       map[string]manifest.BuildTarget{testRepo: target},
	}
	for _, o := range opts {
		o(w)
	}
	w.saveProject()
	var d checker.DoD
	if dd != nil {
		d = dd
	}
	w.chk = checker.New(checker.Deps{
		Store: w.store, Recipes: w.recipes, Indexes: w.indexes, DoD: d, Sink: w.sink,
		Pools: solv.NewPoolCache(4),
	})
	t.Cleanup(func() { w.chk.Close() })
	return w
}

func (w *world) saveProject() {
	w.t.Helper()
	if err := w.store.UpsertProject(context.Background(), w.prj); err != nil {
		w.t.Fatal(err)
	}
}

func commitOf(content string) string {
	sum := sha1.Sum([]byte(content)) // #nosec G401 -- not a security use
	return hex.EncodeToString(sum[:])
}

// spec renders a minimal spec file.
func spec(name string, requires []string, subpackages ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name: %s\nVersion: 1\nRelease: 0\nSummary: s\nLicense: MIT\n", name)
	for _, r := range requires {
		fmt.Fprintf(&b, "BuildRequires: %s\n", r)
	}
	b.WriteString("%description\nx\n")
	for _, s := range subpackages {
		fmt.Fprintf(&b, "%%package -n %s\nSummary: s\n%%description -n %s\nx\n", s, s)
	}
	return b.String()
}

// addPackage registers package name with the given files and returns its commit.
func (w *world) addPackage(name string, files map[string]string, mutate ...func(*manifest.PackageManifest)) string {
	w.t.Helper()
	var all []string
	for n, c := range files {
		all = append(all, n+"="+c)
	}
	sort.Strings(all)
	commit := commitOf(strings.Join(all, "\x00"))
	url := "https://git.example/" + name
	ff := fetch.Files{}
	for n, c := range files {
		ff[n] = []byte(c)
	}
	w.recipes.mu.Lock()
	w.recipes.files[url+"@"+commit] = ff
	w.recipes.mu.Unlock()
	pm := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: name},
		Source:        manifest.PackageSource{Git: url, Commit: commit},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	for _, m := range mutate {
		m(pm)
	}
	if err := w.store.UpsertPackage(context.Background(), w.prj.Project.Name, pm); err != nil {
		w.t.Fatal(err)
	}
	return commit
}

// pkg adds a spec-only package named name requiring requires and producing
// subpackages besides its main package.
func (w *world) pkg(name string, requires []string, subpackages ...string) string {
	return w.addPackage(name, map[string]string{name + ".spec": spec(name, requires, subpackages...)})
}

func (w *world) run() *checker.Result {
	w.t.Helper()
	res, err := w.chk.Run(context.Background(), w.prpa)
	if err != nil {
		w.t.Fatalf("pass failed: %v", err)
	}
	return res
}

// state returns the stored state of a unit ("pkg" or "pkg:flavor").
func (w *world) state(unit string) *storage.PackageBuildState {
	w.t.Helper()
	pkg, flavor := buildrepo.SplitUnit(unit)
	list, err := w.store.ListBuildStates(context.Background(), testProject, storage.BuildStateFilter{})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, st := range list {
		if st.Package == pkg && st.Flavor == flavor {
			return st
		}
	}
	w.t.Fatalf("no state for %s", unit)
	return nil
}

func (w *world) wantState(unit, state, details string) {
	w.t.Helper()
	st := w.state(unit)
	if st.State != state || st.Details != details {
		w.t.Errorf("%s: got %s %q, want %s %q", unit, st.State, details, state, details)
	}
}

func (w *world) wantJobs(want ...string) []*checker.JobSpec {
	w.t.Helper()
	jobs := w.sink.take()
	var got []string
	for _, j := range jobs {
		got = append(got, j.Package)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		w.t.Errorf("emitted jobs %v, want %v", got, want)
	}
	return jobs
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// binary describes a built binary package providing only itself.
func binary(name, version string) bininfo.BinInfo {
	return bininfo.BinInfo{
		Name: name, Version: version, Release: "1", Arch: "x86_64",
		Filename: fmt.Sprintf("%s-%s-1.x86_64.rpm", name, version),
		SHA256:   sha(name + version), Size: 100,
	}
}

// finish emulates a successful job: the worker's binaries replace the unit's
// set in the build repository and the status consumer records success.
func (w *world) finish(job *checker.JobSpec, outputs ...bininfo.BinInfo) {
	w.t.Helper()
	var es []buildrepo.Entry
	for _, b := range outputs {
		b.SHA256 = sha(job.JobID + b.Name)
		es = append(es, buildrepo.Entry{Unit: job.Package, Key: buildrepo.ArtifactPrefix(w.prpa, job.Package) + b.Filename, BinInfo: b})
	}
	w.entries[job.Package] = es
	w.publishIndex()
	pkg, flavor := buildrepo.SplitUnit(job.Package)
	err := w.store.UpsertBuildState(context.Background(), &storage.PackageBuildState{
		Project: job.Project, Repo: job.Repo, Arch: job.Arch, Package: pkg, Flavor: flavor,
		State: storage.StateSucceeded, SourceCommit: job.SourceCommit, Meta: job.Meta, JobID: job.JobID,
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) publishIndex() {
	w.t.Helper()
	var all []buildrepo.Entry
	var infos []bininfo.BinInfo
	for _, es := range w.entries {
		for _, e := range es {
			all = append(all, e)
			if buildrepo.UseForBuild(e.BinInfo) {
				infos = append(infos, e.BinInfo)
			}
		}
	}
	blob, err := solv.BuildSolv(testArch, infos)
	if err != nil {
		w.t.Fatal(err)
	}
	raw, err := buildrepo.EncodeEntries(all)
	if err != nil {
		w.t.Fatal(err)
	}
	w.indexes.mu.Lock()
	w.indexes.idx[w.prpa.String()] = &buildrepo.Index{Hash: buildrepo.ContentHash(blob, raw), Solv: blob, Entries: all}
	w.indexes.mu.Unlock()
}

// rpmmdSolv renders binaries as rpm-md primary metadata and converts them to
// a solv blob, like the DoD cache does.
func rpmmdSolv(t *testing.T, bins []bininfo.BinInfo) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">`)
	for _, bi := range bins {
		fmt.Fprintf(&b, `<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="0" ver="%s" rel="%s"/>`+
			`<checksum type="sha256" pkgid="YES">%s</checksum><location href="%s/%s"/>`+
			`<format><rpm:provides><rpm:entry name="%s" flags="EQ" epoch="0" ver="%s" rel="%s"/></rpm:provides></format></package>`,
			bi.Name, bi.Arch, bi.Version, bi.Release, bi.SHA256, bi.Arch, bi.Filename, bi.Name, bi.Version, bi.Release)
	}
	b.WriteString(`</metadata>`)
	blob, err := solv.RPMMDToSolv("boot", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return blob
}
