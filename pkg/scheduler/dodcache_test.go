package scheduler_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/openSUSE/bsgo/pkg/dod"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type countingGetter struct {
	mu        sync.Mutex
	responses map[string][]byte
	counts    map[string]int
}

func (g *countingGetter) Get(_ context.Context, url string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[url]++
	if data, ok := g.responses[url]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("mock 404: %s", url)
}

type spySolver struct {
	mu    sync.Mutex
	repos []map[string][]byte
}

func (s *spySolver) Resolve(_ context.Context, req solv.ExpansionRequest) (*solv.ExpansionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos = append(s.repos, req.RPMMDRepos)
	return &solv.ExpansionResult{}, nil
}

func TestSchedulerUsesIngestedRepoBlob(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	extURL := "http://mirror.invalid/oss"
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "ext"},
		Targets: map[string]manifest.BuildTarget{"standard": {
			Architectures: []string{"x86_64"},
			Dependencies:  []manifest.DependencySource{{URL: extURL, Type: "rpm-md"}},
		}},
	}
	if err := store.CreateProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "app"},
		Source:        manifest.PackageSource{Git: "https://git/app.git", Recipe: "app.spec"},
		Build:         manifest.PackageBuild{Enabled: true, Requires: []string{"libext"}},
	}
	if err := store.UpsertPackage(ctx, "ext", pkg); err != nil {
		t.Fatal(err)
	}

	primary := []byte(`<metadata xmlns="http://linux.duke.edu/metadata/common"><package type="rpm"><name>libext</name><arch>x86_64</arch>` +
		`<version epoch="0" ver="1" rel="1"/></package></metadata>`)
	sum := sha256.Sum256(primary)
	hexSum := hex.EncodeToString(sum[:])
	repomd := fmt.Sprintf(`<repomd><data type="primary"><checksum type="sha256">%s</checksum><location href="repodata/primary.xml"/></data></repomd>`, hexSum)
	getter := &countingGetter{
		responses: map[string][]byte{
			extURL + "/repodata/repomd.xml":  []byte(repomd),
			extURL + "/repodata/primary.xml": primary,
		},
		counts: map[string]int{},
	}

	spy := &spySolver{}
	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, _ []byte) error { return nil })
	sched := scheduler.NewScheduler(store, spy, pub, scheduler.Config{S3Bucket: "artifacts"})
	sched.SetRepoCache(dod.NewRepoCache(getter, newMockDoDS3Store(), store, dod.RepoCacheConfig{}))

	for i := 0; i < 3; i++ {
		trig := v1.NewTrigger("ext", "app", "https://git/app.git", fmt.Sprintf("c%d", i), "main", "alice")
		if _, err := sched.ProcessTrigger(ctx, trig); err != nil {
			t.Fatalf("ProcessTrigger %d: %v", i, err)
		}
	}
	if got := getter.counts[extURL+"/repodata/primary.xml"]; got != 1 {
		t.Errorf("primary.xml fetched %d times, want 1", got)
	}
	if len(spy.repos) == 0 {
		t.Fatal("solver never called")
	}
	for _, repos := range spy.repos {
		blob := repos[extURL]
		if len(blob) < 4 || string(blob[:4]) != "SOLV" {
			t.Errorf("solver got %d bytes that are not a solv blob", len(blob))
		}
	}
}
