package indexer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type recorder struct{ prpas []buildrepo.Prpa }

func (r *recorder) RepoChanged(_ context.Context, p buildrepo.Prpa) error {
	r.prpas = append(r.prpas, p)
	return nil
}

type env struct {
	t     testing.TB
	store *storage.MemoryStore
	blobs *buildrepo.MemoryBlobs
	rec   *recorder
	ix    *indexer.Indexer
	prpa  buildrepo.Prpa
}

func newEnv(t testing.TB) *env {
	t.Helper()
	store := storage.NewMemoryStore()
	blobs := buildrepo.NewMemoryBlobs()
	rec := &recorder{}
	return &env{
		t: t, store: store, blobs: blobs, rec: rec,
		prpa: buildrepo.Prpa{Project: "p", Repo: "standard", Arch: "x86_64"},
		ix: &indexer.Indexer{
			Pointers: store, Blobs: blobs, Reader: buildrepo.NewReader(store, blobs),
			Leases: &coord.Memory{}, Owner: "test", Notify: rec,
		},
	}
}

func rpm(name, ver string) bininfo.BinInfo {
	sum := sha256.Sum256([]byte(name + ver))
	return bininfo.BinInfo{
		Name: name, Version: ver, Release: "1", Arch: "x86_64",
		Filename: name + "-" + ver + "-1.x86_64.rpm", SHA256: hex.EncodeToString(sum[:]), Size: 10,
		Provides: []bininfo.Dep{{Name: name + "-feature"}},
	}
}

// upload stores bininfo.json for unit and returns the artifact list the
// worker would report.
func (e *env) upload(unit string, infos ...bininfo.BinInfo) []indexer.Artifact {
	e.t.Helper()
	prefix := buildrepo.ArtifactPrefix(e.prpa, unit)
	data, err := bininfo.Marshal(infos)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.blobs.PutObject(context.Background(), prefix+bininfo.FileName, data); err != nil {
		e.t.Fatal(err)
	}
	arts := []indexer.Artifact{{Filename: bininfo.FileName, S3Key: prefix + bininfo.FileName}}
	for _, b := range infos {
		arts = append(arts, indexer.Artifact{Filename: b.Filename, S3Key: prefix + b.Filename, SHA256: b.SHA256, Size: b.Size})
	}
	return arts
}

func (e *env) integrate(unit string, arts []indexer.Artifact) (bool, error) {
	return e.ix.Integrate(context.Background(), indexer.Finished{Prpa: e.prpa, Unit: unit, Artifacts: arts})
}

func (e *env) names() []string {
	e.t.Helper()
	idx, err := e.ix.Reader.Index(context.Background(), e.prpa)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, en := range idx.Entries {
		out = append(out, en.Unit+":"+en.Filename())
	}
	return out
}

func TestIntegrateReplacesUnitSet(t *testing.T) {
	e := newEnv(t)
	steps := []struct {
		name        string
		unit        string
		bins        []bininfo.BinInfo
		wantChanged bool
		want        string
	}{
		{"first build, debug packages are left out", "a",
			[]bininfo.BinInfo{rpm("a", "1"), rpm("a-devel", "1"), rpm("a-debuginfo", "1"), rpm("a-debugsource", "1")},
			true, "a:a-1-1.x86_64.rpm,a:a-devel-1-1.x86_64.rpm"},
		{"another unit joins", "b", []bininfo.BinInfo{rpm("b", "1")},
			true, "a:a-1-1.x86_64.rpm,a:a-devel-1-1.x86_64.rpm,b:b-1-1.x86_64.rpm"},
		{"rebuild replaces the old set", "a", []bininfo.BinInfo{rpm("a", "2")},
			true, "a:a-2-1.x86_64.rpm,b:b-1-1.x86_64.rpm"},
		{"identical rebuild changes nothing", "a", []bininfo.BinInfo{rpm("a", "2")},
			false, "a:a-2-1.x86_64.rpm,b:b-1-1.x86_64.rpm"},
		{"a build without binaries empties the unit", "b", nil, true, "a:a-2-1.x86_64.rpm"},
		{"emptying an unknown unit changes nothing", "b", nil, false, "a:a-2-1.x86_64.rpm"},
		{"a build with an empty bininfo empties the unit", "a", []bininfo.BinInfo{}, true, ""},
		{"an empty bininfo on an empty repository changes nothing", "a", []bininfo.BinInfo{}, false, ""},
	}
	for _, tc := range steps {
		before := len(e.rec.prpas)
		// A build without binaries reports only its log and provenance.
		arts := []indexer.Artifact{{Filename: "build.log", S3Key: "k/build.log"}, {Filename: "_slsa_provenance.json", S3Key: "k/prov"}}
		if tc.bins != nil {
			arts = e.upload(tc.unit, tc.bins...)
		}
		changed, err := e.integrate(tc.unit, arts)
		if err != nil || changed != tc.wantChanged {
			t.Fatalf("%s: changed=%v err=%v, want changed=%v", tc.name, changed, err, tc.wantChanged)
		}
		if got := strings.Join(e.names(), ","); got != tc.want {
			t.Errorf("%s: index %s, want %s", tc.name, got, tc.want)
		}
		if announced := len(e.rec.prpas) - before; (announced == 1) != tc.wantChanged {
			t.Errorf("%s: %d repo changed events, want one exactly when the index changed", tc.name, announced)
		}
	}
}

func TestIntegrateIndexCarriesRealProvides(t *testing.T) {
	e := newEnv(t)
	if _, err := e.integrate("a", e.upload("a", rpm("a", "1"))); err != nil {
		t.Fatal(err)
	}
	idx, err := e.ix.Reader.Index(context.Background(), e.prpa)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Solv) == 0 || idx.Hash == "" {
		t.Fatalf("empty index %+v", idx)
	}
	if _, ok := idx.Lookup("a", "1-1", "x86_64", ""); !ok {
		t.Error("entry of a not found by identity")
	}
	ptr, err := e.store.GetBuildRepoIndex(context.Background(), "p", "standard", "x86_64")
	if err != nil || ptr.ContentHash != idx.Hash || !strings.HasSuffix(ptr.SolvKey, idx.Hash+".solv") {
		t.Errorf("pointer %+v does not address the content hash %s", ptr, idx.Hash)
	}
}

func TestIntegrateRejectsInconsistentUploads(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(arts []indexer.Artifact) []indexer.Artifact
	}{
		{"checksum differs from the uploaded file", func(a []indexer.Artifact) []indexer.Artifact {
			a[1].SHA256 = "00"
			return a
		}},
		{"listed rpm was not uploaded", func(a []indexer.Artifact) []indexer.Artifact { return a[:1] }},
		{"rpm without bininfo", func(a []indexer.Artifact) []indexer.Artifact { return a[1:] }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			arts := tc.mutate(e.upload("a", rpm("a", "1")))
			if _, err := e.integrate("a", arts); err == nil {
				t.Fatal("want an error")
			}
			if len(e.names()) != 0 {
				t.Error("a rejected upload must not reach the index")
			}
		})
	}
}

func TestEventRoundTrip(t *testing.T) {
	p := buildrepo.Prpa{Project: "a:b", Repo: "std", Arch: "s390x"}
	data, err := indexer.EncodeEvent(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := indexer.DecodeEvent(data)
	if err != nil || got != p {
		t.Errorf("got %+v, %v", got, err)
	}
	if _, err := indexer.DecodeEvent([]byte(`{"project":"a"}`)); err == nil {
		t.Error("an incomplete event must be rejected")
	}
}
