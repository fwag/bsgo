package vrev_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe/history"
	"github.com/openSUSE/bsgo/pkg/scheduler/vrev"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const prj = "test:Proj"

type env struct {
	t     *testing.T
	store *storage.MemoryStore
	now   time.Time
	logs  []string
	// calls records the resolved packages in order.
	calls []string
	// outcome decides the result per package@commit; nil error and zero
	// Result mean version 1.0 with git vrev 1.
	outcome func(req history.Request, memo history.Memo) (*history.Result, error)
	mu      sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, store: storage.NewMemoryStore(), now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	e.store.Clock = func() time.Time { return e.now }
	err := e.store.UpsertProject(context.Background(), &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: prj},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) pin(pkg, commit string) {
	e.t.Helper()
	pm := &manifest.PackageManifest{
		SchemaVersion: "v1", Package: manifest.PackageInfo{Name: pkg},
		Source: manifest.PackageSource{Git: "https://git.example/" + pkg, Commit: commit},
		Build:  manifest.PackageBuild{Enabled: true},
	}
	if err := e.store.UpsertPackage(context.Background(), prj, pm); err != nil {
		e.t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
}

func (e *env) resolver(workers int) *vrev.Resolver {
	return vrev.New(e.store, vrev.Options{
		Owner: "test", Workers: workers, Now: func() time.Time { return e.now },
		Logf: func(f string, a ...any) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.logs = append(e.logs, fmt.Sprintf(f, a...))
		},
		Resolve: func(_ context.Context, req history.Request, memo history.Memo) (*history.Result, error) {
			e.mu.Lock()
			e.calls = append(e.calls, req.Lookup)
			e.mu.Unlock()
			if e.outcome != nil {
				return e.outcome(req, memo)
			}
			return result(req.Commit, "1.0", 1), nil
		},
	})
}

func result(commit, version string, vrev int) *history.Result {
	return &history.Result{
		Point:   history.Point{Commit: commit, Version: version, VT: map[string]int{version: vrev}},
		GitVrev: vrev, Walked: 3, BlobsFetched: 2,
	}
}

func (e *env) revision(pkg, commit string) *storage.PackageRevision {
	e.t.Helper()
	r, err := e.store.RevisionFor(context.Background(), prj, pkg, commit)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestWantedRevisionsResolveFirst(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"a", "b", "c", "d"} {
		e.pin(p, "c1"+p)
	}
	ctx := context.Background()
	if err := e.store.WantRevisions(ctx, []storage.RevisionRef{{Project: prj, Package: "d", Commit: "c1d"}, {Project: prj, Package: "c", Commit: "c1c"}}); err != nil {
		t.Fatal(err)
	}
	r := e.resolver(1)
	for r.Step(ctx) {
	}
	got := strings.Join(e.calls, ",")
	if got != "c,d,a,b" && got != "d,c,a,b" {
		t.Errorf("order %s: wanted packages must come before the backfill", got)
	}
	if !strings.HasPrefix(e.calls[0], "c") && !strings.HasPrefix(e.calls[0], "d") {
		t.Errorf("first resolved %s", e.calls[0])
	}
	for _, p := range []string{"a", "b", "c", "d"} {
		if rev := e.revision(p, "c1"+p); !rev.Resolved() || rev.Vrev != 1 {
			t.Errorf("%s: %+v", p, rev)
		}
	}
}

func TestStaleWantIsIgnored(t *testing.T) {
	e := newEnv(t)
	e.pin("a", "c1a")
	e.pin("b", "c1b")
	ctx := context.Background()
	if err := e.store.WantRevisions(ctx, []storage.RevisionRef{{Project: prj, Package: "b", Commit: "c1b"}}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(storage.WantedWindow + time.Minute)
	r := e.resolver(1)
	r.Step(ctx)
	if e.calls[0] != "a" {
		t.Errorf("a want older than the window must not jump the queue, resolved %s first", e.calls[0])
	}
}

func TestFailureBacksOffAndNeverBlocksOthers(t *testing.T) {
	e := newEnv(t)
	e.pin("a", "c1a")
	e.pin("b", "c1b")
	e.outcome = func(req history.Request, _ history.Memo) (*history.Result, error) {
		if req.Lookup == "a" {
			return nil, errors.New("fetch: connection reset")
		}
		return result(req.Commit, "1.0", 1), nil
	}
	ctx := context.Background()
	r := e.resolver(1)
	r.Step(ctx)
	r.Step(ctx)
	if r.Step(ctx) {
		t.Fatal("a is backing off and b is done: nothing to claim")
	}
	a := e.revision("a", "c1a")
	if a.State != storage.RevisionPending || a.Attempts != 1 || !strings.Contains(a.LastError, "connection reset") {
		t.Errorf("a: %+v", a)
	}
	if !e.revision("b", "c1b").Resolved() {
		t.Error("a failure must not hold back other packages")
	}
	e.now = e.now.Add(61 * time.Second)
	r.Step(ctx)
	if a = e.revision("a", "c1a"); a.Attempts != 2 || !a.NextTryAt.After(e.now.Add(110*time.Second)) {
		t.Errorf("second backoff must double to 2m: %+v", a)
	}
	if got := e.logs[0]; !strings.Contains(got, "test:Proj/a c1a: failed (attempt 1, retry in 1m0s)") {
		t.Errorf("log %q", got)
	}
}

func TestRateLimitPausesAllWorkers(t *testing.T) {
	e := newEnv(t)
	e.pin("a", "c1a")
	e.pin("b", "c1b")
	e.outcome = func(req history.Request, _ history.Memo) (*history.Result, error) {
		return nil, errors.New("fetch: HTTP 429 Too Many Requests")
	}
	ctx := context.Background()
	r := e.resolver(1)
	r.Step(ctx)
	if a := e.revision("a", "c1a"); a.NextTryAt.Sub(e.now) < 5*time.Minute {
		t.Errorf("rate limited revision retries at least after 5m, got %s", a.NextTryAt.Sub(e.now))
	}
	done := make(chan struct{})
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	go func() { r.Run(cctx); close(done) }()
	<-done
	if len(e.calls) != 1 {
		t.Errorf("workers must pause after a rate limit, calls %v", e.calls)
	}
}

func TestSupersededRevisionIsSkippedAndLaterOneResolves(t *testing.T) {
	e := newEnv(t)
	e.pin("a", "old")
	e.pin("a", "new")
	e.outcome = func(req history.Request, _ history.Memo) (*history.Result, error) {
		if req.Commit == "old" {
			return nil, errors.New("fetch: remote: upload-pack: not our ref")
		}
		return result(req.Commit, "1.0", 5), nil
	}
	ctx := context.Background()
	r := e.resolver(1)
	for range 3 {
		r.Step(ctx)
		e.now = e.now.Add(time.Hour)
	}
	if old := e.revision("a", "old"); old.State != storage.RevisionSkipped {
		t.Fatalf("old: %+v", old)
	}
	r.Step(ctx)
	if n := e.revision("a", "new"); !n.Resolved() || n.Vrev != 5 {
		t.Errorf("new: %+v", n)
	}
}

func TestMemoLetsLaterPinStartFromEarlierOne(t *testing.T) {
	e := newEnv(t)
	e.pin("a", "c1")
	var base string
	e.outcome = func(req history.Request, memo history.Memo) (*history.Result, error) {
		p, err := memo.Nearest(context.Background(), req.Key(), []string{req.Commit, "c1"})
		if err != nil {
			return nil, err
		}
		if p != nil {
			base = p.Commit
			return result(req.Commit, p.Version, p.VT[p.Version]+2), nil
		}
		return result(req.Commit, "1.0", 3), nil
	}
	ctx := context.Background()
	r := e.resolver(1)
	r.Step(ctx)
	e.pin("a", "c2")
	r.Step(ctx)
	if base != "c1" {
		t.Errorf("second resolution did not see the memo of c1 (base %q)", base)
	}
	if rev := e.revision("a", "c2"); rev.Vrev != 5 {
		t.Errorf("vrev %d, want 5", rev.Vrev)
	}
	if want := "test:Proj/a c2: version 1.0 vrev 5 (git 5) walked 3 blobs 2 in "; !strings.HasPrefix(e.logs[len(e.logs)-1], want) {
		t.Errorf("log %q, want prefix %q", e.logs[len(e.logs)-1], want)
	}
}

func TestZeroWorkersDisablesRun(t *testing.T) {
	e := newEnv(t)
	e.pin("a", "c1")
	e.resolver(0).Run(context.Background())
	if len(e.calls) != 0 {
		t.Errorf("disabled resolver resolved %v", e.calls)
	}
}
