package e2e_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/publisher"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// storeBlobs lets the indexer and the publisher share one object store.
type storeBlobs struct{ *publisher.MemoryStore }

func (b storeBlobs) GetObject(ctx context.Context, key string) ([]byte, error) {
	return b.ReadObject(ctx, key)
}

func (b storeBlobs) PutObject(_ context.Context, key string, data []byte) error {
	b.Set(key, data)
	return nil
}

type allPolicy struct{}

func (allPolicy) Policy(context.Context, buildrepo.Prpa) (publisher.Policy, error) {
	return publisher.Policy{Publish: true, RPMMD: true}, nil
}

// serialCheck wraps the real createrepo_c executor and fails the test when two
// metadata generations of this test run at the same time.
type serialCheck struct {
	t      *testing.T
	inner  publisher.CreateRepoExecutor
	active atomic.Int32
	runs   atomic.Int32
	// reusedOld is whether the most recent run was incremental.
	reusedOld atomic.Bool
}

func (s *serialCheck) Run(ctx context.Context, opts publisher.CreateRepoOptions) error {
	if s.active.Add(1) > 1 {
		s.t.Error("two publishers generated metadata of the same prpa concurrently")
	}
	defer s.active.Add(-1)
	s.runs.Add(1)
	s.reusedOld.Store(opts.OldRepoDir != "")
	time.Sleep(150 * time.Millisecond) // widen the race window
	return s.inner.Run(ctx, opts)
}

// buildNoarchRPM builds a tiny real RPM with rpmbuild and returns its file name
// and content.
func buildNoarchRPM(t *testing.T, name, version string) (string, []byte) {
	t.Helper()
	top := t.TempDir()
	spec := filepath.Join(top, name+".spec")
	text := fmt.Sprintf(`Name: %s
Version: %s
Release: 1
Summary: test package
License: MIT
BuildArch: noarch
%%description
test package
%%install
mkdir -p %%{buildroot}/usr/share/%s
echo %s > %%{buildroot}/usr/share/%s/version
%%files
/usr/share/%s/version
`, name, version, name, version, name, name)
	if err := os.WriteFile(spec, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("rpmbuild", "-bb", "--define", "_topdir "+top, "--define", "_rpmdir "+top+"/RPMS",
		"--define", "_build_name_fmt %{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}.rpm", spec).CombinedOutput()
	if err != nil {
		t.Skipf("rpmbuild cannot build test packages here: %v: %s", err, out)
	}
	file := fmt.Sprintf("%s-%s-1.noarch.rpm", name, version)
	data, err := os.ReadFile(filepath.Join(top, "RPMS", file))
	if err != nil {
		t.Fatal(err)
	}
	return file, data
}

func finishJob(ctx context.Context, t *testing.T, ix *indexer.Indexer, store *publisher.MemoryStore, p buildrepo.Prpa, unit string, rpms map[string][]byte) {
	t.Helper()
	prefix := buildrepo.ArtifactPrefix(p, unit)
	var infos []bininfo.BinInfo
	var arts []indexer.Artifact
	for file, data := range rpms {
		h := sha256.Sum256(data)
		sha := hex.EncodeToString(h[:])
		name, rest, _ := strings.Cut(strings.TrimSuffix(file, ".noarch.rpm"), "-")
		version, release, _ := strings.Cut(rest, "-")
		infos = append(infos, bininfo.BinInfo{
			Name: name, Version: version, Release: release, Arch: "noarch",
			Filename: file, SHA256: sha, Size: int64(len(data)),
		})
		store.Set(prefix+file, data)
		arts = append(arts, indexer.Artifact{Filename: file, S3Key: prefix + file, SHA256: sha, Size: int64(len(data))})
	}
	raw, err := bininfo.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	store.Set(prefix+bininfo.FileName, raw)
	arts = append(arts, indexer.Artifact{Filename: bininfo.FileName, S3Key: prefix + bininfo.FileName})
	if _, err := ix.Integrate(ctx, indexer.Finished{Prpa: p, Unit: unit, Artifacts: arts}); err != nil {
		t.Fatal(err)
	}
}

func publishedFiles(t *testing.T, store *publisher.MemoryStore, prefix string) []string {
	t.Helper()
	repomd, err := store.ReadObject(context.Background(), prefix+"repodata/repomd.xml")
	if err != nil {
		return nil
	}
	_, rest, ok := strings.Cut(string(repomd), `<data type="primary">`)
	if !ok {
		return nil
	}
	_, href, _ := strings.Cut(rest, `<location href="`)
	href, _, _ = strings.Cut(href, `"`)
	raw, err := store.ReadObject(context.Background(), prefix+href)
	if err != nil {
		t.Fatalf("repomd.xml references %s, which is not published", href)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	xml, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, chunk := range strings.Split(string(xml), `<location href="`)[1:] {
		f, _, _ := strings.Cut(chunk, `"`)
		files = append(files, f)
	}
	return files
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPublisherReplicasEndToEnd runs two publisher replicas on a NATS KV
// lease against one object store, with real createrepo_c and real RPMs: the
// indexer integrates a build, the repo-changed event reaches the replicas,
// exactly one publishes, and a rebuilt package replaces its old NEVR.
func TestPublisherReplicasEndToEnd(t *testing.T) {
	for _, tool := range []string{"createrepo_c", "rpmbuild"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, natsURL := runEmbeddedNATS(t)
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	store := publisher.NewMemoryStore()
	pointers := storage.NewMemoryStore()
	reader := buildrepo.NewReader(pointers, storeBlobs{store})
	p := buildrepo.Prpa{Project: "SUSE:SLFO:1.2", Repo: "standard", Arch: "x86_64"}
	gen := &serialCheck{t: t, inner: publisher.NewRealCreateRepoExecutor()}

	var wg sync.WaitGroup
	for _, owner := range []string{"replica-1", "replica-2"} {
		kv, err := coord.NewNATSBuckets(ctx, js, 30*time.Second, "bsgo-publisher-dirty", "bsgo-publisher-leases")
		if err != nil {
			t.Fatal(err)
		}
		pub := &publisher.Publisher{
			Store: store, Index: reader, Policy: allPolicy{}, CreateRepo: gen, TempRoot: t.TempDir(),
		}
		svc := publisher.NewService(pub, publisher.ServiceConfig{
			Coord: kv, Owner: owner, Debounce: 200 * time.Millisecond, MaxDelay: 2 * time.Second, Poll: 50 * time.Millisecond,
			OnError: func(prpa string, err error) { t.Errorf("%s: %v", prpa, err) },
		})
		if _, err := nc.Subscribe(natsqueue.SubjectEventsRepoChanged, func(msg *nats.Msg) {
			pr, err := indexer.DecodeEvent(msg.Data)
			if err != nil {
				t.Error(err)
				return
			}
			if err := svc.MarkChanged(ctx, pr); err != nil {
				t.Error(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); svc.Run(ctx) }()
	}
	defer wg.Wait()
	defer cancel()

	ix := &indexer.Indexer{
		Pointers: pointers, Blobs: storeBlobs{store}, Reader: reader, Leases: &coord.Memory{}, Owner: "indexer",
		Notify: natsNotifierE2E{nc: nc},
	}
	prefix := "repos/SUSE:SLFO:1.2/standard/x86_64/"

	bash52, data52 := buildNoarchRPM(t, "bash", "5.2")
	curl, dataCurl := buildNoarchRPM(t, "curl", "8.0")
	finishJob(ctx, t, ix, store, p, "bash", map[string][]byte{bash52: data52})
	finishJob(ctx, t, ix, store, p, "curl", map[string][]byte{curl: dataCurl})
	waitFor(t, "first publication", func() bool { return len(publishedFiles(t, store, prefix)) == 2 })

	bash53, data53 := buildNoarchRPM(t, "bash", "5.3")
	finishJob(ctx, t, ix, store, p, "bash", map[string][]byte{bash53: data53})
	waitFor(t, "rebuilt package replaces the old NEVR", func() bool {
		files := publishedFiles(t, store, prefix)
		return len(files) == 2 && strings.Contains(strings.Join(files, " "), bash53)
	})
	waitFor(t, "stale RPM removal", func() bool { return !store.Has(prefix + bash52) })
	if !store.Has(prefix+bash53) || !store.Has(prefix+curl) {
		t.Errorf("published objects: %v", store.Keys(prefix))
	}
	if gen.runs.Load() < 2 {
		t.Errorf("createrepo_c ran %d times", gen.runs.Load())
	}
	if !gen.reusedOld.Load() {
		t.Error("the republish did not build on the published repodata")
	}
}

type natsNotifierE2E struct{ nc *nats.Conn }

func (n natsNotifierE2E) RepoChanged(_ context.Context, p buildrepo.Prpa) error {
	data, err := indexer.EncodeEvent(p)
	if err != nil {
		return err
	}
	return n.nc.Publish(natsqueue.SubjectEventsRepoChanged, data)
}
