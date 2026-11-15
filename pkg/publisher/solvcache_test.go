package publisher_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func newProjectStore(t *testing.T) storage.Store {
	t.Helper()
	store := storage.NewMemoryStore()
	prj := &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "prj"}}
	if err := store.CreateProject(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	return store
}

func putBinInfo(t *testing.T, s3 *MockS3Client, pkg string, infos ...bininfo.BinInfo) {
	t.Helper()
	data, err := bininfo.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	s3.FilesOnS3["artifacts/prj/standard/x86_64/"+pkg+"/"+bininfo.FileName] = data
}

func solvPackages(t *testing.T, store storage.Store, args ...string) map[string]string {
	t.Helper()
	rec, err := store.GetTargetSolv(context.Background(), "prj", "standard", "x86_64")
	if err != nil || rec == nil {
		t.Fatalf("no target solv: %v", err)
	}
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "t", Data: rec.SolvBlob}})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	res, err := pp.Expand(nil, args...)
	if err != nil {
		t.Fatalf("expand %v: %v", args, err)
	}
	out := map[string]string{}
	for _, p := range res.Packages {
		out[p.Name] = p.EVR
	}
	return out
}

func TestPublisherSolvCacheFromBinInfo(t *testing.T) {
	ctx := context.Background()
	s3 := NewMockS3Client()
	store := newProjectStore(t)
	pub := publisher.NewPublisher(&MockCreateRepoExecutor{}, s3, publisher.NewLocalLockManager(), t.TempDir())
	pub.SetStore(store)
	req := publisher.PublishRequest{RequestID: "r", Project: "prj", Target: "standard", Architecture: "x86_64", Package: "foo"}

	bar := bininfo.BinInfo{Name: "bar", Version: "1", Release: "1", Arch: "x86_64", Filename: "bar-1-1.x86_64.rpm", SHA256: "b1"}
	putBinInfo(t, s3, "bar", bar)
	putBinInfo(t, s3, "foo",
		bininfo.BinInfo{Name: "foo", Version: "1", Release: "1", Arch: "x86_64", Filename: "foo-1-1.x86_64.rpm", SHA256: "f1",
			Requires: []bininfo.Dep{{Name: "libold.so.1()(64bit)"}}},
		bininfo.BinInfo{Name: "libold1", Version: "1", Release: "1", Arch: "x86_64", Filename: "libold1-1-1.x86_64.rpm", SHA256: "o1",
			Provides: []bininfo.Dep{{Name: "libold.so.1()(64bit)"}}},
		bininfo.BinInfo{Name: "foo-debuginfo", Version: "1", Release: "1", Arch: "x86_64", Filename: "foo-debuginfo-1-1.x86_64.rpm"},
	)
	if _, err := pub.ProcessPublishRequest(ctx, req); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	got := solvPackages(t, store, "foo", "bar")
	if got["foo"] != "1-1" || got["libold1"] != "1-1" || got["bar"] != "1-1" {
		t.Fatalf("first set wrong: %v", got)
	}
	if _, ok := got["foo-debuginfo"]; ok {
		t.Error("debuginfo must not be in the standard index")
	}

	// A rebuild of foo replaces its whole binary set: libold1 is gone.
	putBinInfo(t, s3, "foo", bininfo.BinInfo{Name: "foo", Version: "2", Release: "1", Arch: "x86_64",
		Filename: "foo-2-1.x86_64.rpm", SHA256: "f2"})
	if _, err := pub.ProcessPublishRequest(ctx, req); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	got = solvPackages(t, store, "foo", "bar")
	if got["foo"] != "2-1" || got["bar"] != "1-1" {
		t.Fatalf("replace semantics violated: %v", got)
	}
	rec, _ := store.GetTargetSolv(ctx, "prj", "standard", "x86_64")
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "t", Data: rec.SolvBlob}})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	if _, err := pp.Expand(nil, "libold1"); err == nil || !strings.Contains(err.Error(), "nothing provides") {
		t.Errorf("stale libold1 still indexed: %v", err)
	}
}

func TestPublisherSolvCacheBadBinInfo(t *testing.T) {
	s3 := NewMockS3Client()
	s3.FilesOnS3["artifacts/prj/standard/x86_64/foo/"+bininfo.FileName] = []byte("{not json")
	pub := publisher.NewPublisher(&MockCreateRepoExecutor{}, s3, publisher.NewLocalLockManager(), t.TempDir())
	pub.SetStore(newProjectStore(t))
	req := publisher.PublishRequest{RequestID: "r", Project: "prj", Target: "standard", Architecture: "x86_64"}
	if _, err := pub.ProcessPublishRequest(context.Background(), req); err == nil {
		t.Fatal("expected a corrupt bininfo.json to fail the publish")
	}
}
