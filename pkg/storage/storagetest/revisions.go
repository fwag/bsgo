// Package storagetest holds store behaviour tests shared by every
// storage.Store implementation, so the in-memory and the PostgreSQL stores
// are held to the same table of cases.
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const claimCap = 5000

type fixture struct {
	t       *testing.T
	s       storage.Store
	project string
}

func newFixture(t *testing.T, s storage.Store, prefix, name string) *fixture {
	t.Helper()
	f := &fixture{t: t, s: s, project: prefix + ":" + name}
	err := s.UpsertProject(context.Background(), &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: f.project},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) pin(pkg, url, commit string) {
	f.t.Helper()
	pm := &manifest.PackageManifest{
		SchemaVersion: "v1", Package: manifest.PackageInfo{Name: pkg},
		Source: manifest.PackageSource{Git: url, Commit: commit}, Build: manifest.PackageBuild{Enabled: true},
	}
	if err := f.s.UpsertPackage(context.Background(), f.project, pm); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) revisions(pkg string) []storage.PackageRevision {
	f.t.Helper()
	list, err := f.s.ListRevisions(context.Background(), f.project, []string{pkg})
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

// lowestPending returns the lowest pending revision of pkg.
func (f *fixture) lowestPending(pkg string) storage.PackageRevision {
	f.t.Helper()
	for _, r := range f.revisions(pkg) {
		if r.State == storage.RevisionPending {
			return r
		}
	}
	f.t.Fatalf("%s has no pending revision", pkg)
	return storage.PackageRevision{}
}

// resolve finishes the lowest pending revision of pkg and returns its vrev.
func (f *fixture) resolve(pkg, version string, git int) int {
	f.t.Helper()
	done, err := f.s.FinishRevision(context.Background(), f.lowestPending(pkg), storage.RevisionResult{Version: version, GitVrev: git})
	if err != nil {
		f.t.Fatal(err)
	}
	return done.Vrev
}

// claimed claims revisions, batch at a time, until want of this project were
// seen (rows of other projects in a shared database are leased and skipped).
// Use batch 1 to observe the claim order.
func (f *fixture) claimed(want, batch int, lease time.Duration) []storage.PackageRevision {
	f.t.Helper()
	var mine []storage.PackageRevision
	for range claimCap {
		if len(mine) == want {
			return mine
		}
		got, err := f.s.ClaimRevisions(context.Background(), "test", batch, lease)
		if err != nil {
			f.t.Fatal(err)
		}
		if len(got) == 0 {
			break
		}
		for _, r := range got {
			if r.Project == f.project {
				mine = append(mine, r)
			}
		}
	}
	return mine
}

// Revisions runs the revision log, resolution and history memo cases against
// s. prefix makes the projects of the run unique in a shared database;
// cleanup, when not nil, removes the revisions of the projects of the run
// (the claim cases see every pending revision of a shared database).
func Revisions(t *testing.T, s storage.Store, prefix string, cleanup func(prefix string)) {
	if cleanup != nil {
		cleanup(prefix)
		t.Cleanup(func() { cleanup(prefix) })
	}
	cases := []struct {
		name string
		run  func(t *testing.T, f *fixture)
	}{
		{"append only when the pin changes", appendOnChange},
		{"revision for a commit is the latest one", latestForCommit},
		{"floor keeps the vrev monotonic per version", floorCases},
		{"finish only the lowest pending revision", finishInOrder},
		{"claim gives one row per package and respects leases", claimCases},
		{"wanted revisions are claimed first", wantedFirst},
		{"skipped superseded revision does not block", skippedDoesNotBlock},
		{"reset keeps the old vrev as minimum", resetCases},
		{"admin set", setCases},
		{"history memo", historyCases},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newFixture(t, s, prefix, tc.name))
		})
	}
}

func appendOnChange(t *testing.T, f *fixture) {
	steps := []struct {
		url, commit string
		want        int
	}{
		{"u1", "c1", 1},
		{"u1", "c1", 1},
		{"u1", "c2", 2},
		{"u2", "c2", 3},
		{"u2", "", 3},
		{"u2", "c2", 3},
	}
	for i, st := range steps {
		f.pin("p", st.url, st.commit)
		if got := len(f.revisions("p")); got != st.want {
			t.Errorf("step %d: %d revisions, want %d", i, got, st.want)
		}
	}
	for i, r := range f.revisions("p") {
		if r.Seq != i+1 || r.State != storage.RevisionPending || r.Vrev != 0 {
			t.Errorf("revision %d: %+v", i, r)
		}
	}
}

func latestForCommit(t *testing.T, f *fixture) {
	for _, c := range []string{"A", "B", "A"} {
		f.pin("p", "u", c)
	}
	ctx := context.Background()
	if r, err := f.s.RevisionFor(ctx, f.project, "p", "A"); err != nil || r.Seq != 3 {
		t.Errorf("A: %+v, %v; want seq 3", r, err)
	}
	if r, err := f.s.RevisionFor(ctx, f.project, "p", "B"); err != nil || r.Seq != 2 {
		t.Errorf("B: %+v, %v; want seq 2", r, err)
	}
	if _, err := f.s.RevisionFor(ctx, f.project, "p", "Z"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown commit: %v, want ErrNotFound", err)
	}
	if _, err := f.s.RevisionFor(ctx, f.project, "other", "A"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown package: %v, want ErrNotFound", err)
	}
}

func floorCases(t *testing.T, f *fixture) {
	type step struct {
		version string
		git     int
		want    int
	}
	scenarios := []struct {
		name  string
		steps []step
	}{
		{"linear growth", []step{{"1.0", 5, 5}, {"1.0", 9, 9}}},
		{"force push drops the git count", []step{{"1.0", 5, 5}, {"1.0", 2, 6}, {"1.0", 9, 9}}},
		{"re-pin to an older commit", []step{{"1.0", 3, 3}, {"1.0", 4, 4}, {"1.0", 3, 5}}},
		{"revert of a bump continues", []step{{"1.0", 3, 3}, {"2.0", 1, 1}, {"1.0", 2, 4}}},
		{"other version restarts", []step{{"1.0", 7, 7}, {"2.0", 1, 1}}},
		{"known to unknown", []step{{"1.0", 3, 3}, {storage.UnknownVersion, 1, 4}}},
		{"unknown back to known", []step{{"1.0", 3, 3}, {storage.UnknownVersion, 1, 4}, {"1.0", 2, 5}}},
		{"unknown to unknown", []step{{storage.UnknownVersion, 2, 2}, {storage.UnknownVersion, 1, 3}}},
	}
	for _, sc := range scenarios {
		pkg := "p-" + sc.name
		for i, st := range sc.steps {
			f.pin(pkg, "u", fmt.Sprintf("c%d", i))
			if got := f.resolve(pkg, st.version, st.git); got != st.want {
				t.Errorf("%s step %d (%s git %d): vrev %d, want %d", sc.name, i, st.version, st.git, got, st.want)
			}
		}
	}
}

func finishInOrder(t *testing.T, f *fixture) {
	f.pin("p", "u", "c1")
	f.pin("p", "u", "c2")
	revs := f.revisions("p")
	ctx := context.Background()
	if _, err := f.s.FinishRevision(ctx, revs[1], storage.RevisionResult{Version: "1", GitVrev: 1}); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("finishing seq 2 before seq 1: %v, want ErrConflict", err)
	}
	if got := f.resolve("p", "1", 1); got != 1 {
		t.Errorf("seq 1 vrev %d", got)
	}
	if _, err := f.s.FinishRevision(ctx, revs[0], storage.RevisionResult{Version: "1", GitVrev: 1}); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("finishing twice: %v, want ErrConflict", err)
	}
	if _, err := f.s.FinishRevision(ctx, revs[1], storage.RevisionResult{}); err == nil {
		t.Error("an empty result must be rejected")
	}
}

func claimCases(t *testing.T, f *fixture) {
	f.pin("a", "u", "a1")
	f.pin("a", "u", "a2")
	f.pin("b", "u", "b1")
	got := f.claimed(2, 1000, 300*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("claimed %d revisions, want one per package", len(got))
	}
	for _, r := range got {
		if r.Seq != 1 || r.ClaimedBy != "test" {
			t.Errorf("claimed %+v: want the lowest seq leased to test", r)
		}
	}
	if again := f.claimed(1, 1000, time.Minute); len(again) != 0 {
		t.Errorf("leased revisions claimed again: %+v", again)
	}
	time.Sleep(400 * time.Millisecond)
	if again := f.claimed(2, 1000, time.Minute); len(again) != 2 {
		t.Errorf("after the lease expired %d revisions claimed, want 2", len(again))
	}
}

func wantedFirst(t *testing.T, f *fixture) {
	for _, p := range []string{"a", "b", "c", "d"} {
		f.pin(p, "u", "c1"+p)
	}
	err := f.s.WantRevisions(context.Background(), []storage.RevisionRef{
		{Project: f.project, Package: "d", Commit: "c1d"},
		{Project: f.project, Package: "unknown", Commit: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.claimed(4, 1, time.Minute)
	if len(got) != 4 || got[0].Package != "d" {
		t.Errorf("claim order %v: the wanted package must come first", names(got))
	}
}

func names(revs []storage.PackageRevision) []string {
	var out []string
	for _, r := range revs {
		out = append(out, r.Package)
	}
	return out
}

func skippedDoesNotBlock(t *testing.T, f *fixture) {
	f.pin("p", "u", "old")
	f.pin("p", "u", "new")
	ctx := context.Background()
	for i := 1; i <= storage.MaxRevisionAttempts; i++ {
		old, err := f.s.FailRevision(ctx, f.lowestPending("p"), "gone", 0)
		if err != nil {
			t.Fatal(err)
		}
		wantState := storage.RevisionPending
		if i == storage.MaxRevisionAttempts {
			wantState = storage.RevisionSkipped
		}
		if old.State != wantState || old.Attempts != i || old.LastError != "gone" {
			t.Errorf("attempt %d: %+v, want state %s", i, old, wantState)
		}
		if old.State == storage.RevisionPending && !old.NextTryAt.After(time.Now()) {
			t.Errorf("attempt %d: no backoff: %v", i, old.NextTryAt)
		}
	}
	if got := f.resolve("p", "1.0", 5); got != 5 {
		t.Errorf("later revision vrev %d, want 5 (a skipped revision is no floor)", got)
	}
	// The latest revision keeps retrying instead of being skipped.
	f.pin("p", "u", "latest")
	for range storage.MaxRevisionAttempts + 1 {
		last, err := f.s.FailRevision(ctx, f.lowestPending("p"), "slow", 0)
		if err != nil || last.State != storage.RevisionPending {
			t.Fatalf("latest revision: %+v, %v; want pending", last, err)
		}
	}
}

func resetCases(t *testing.T, f *fixture) {
	ctx := context.Background()
	f.pin("p", "u", "c1")
	f.resolve("p", "1.0", 10)
	f.pin("q", "u", "c1")
	f.resolve("q", "1.0", 10)
	done, err := f.s.ResetRevisions(ctx, f.project, []string{"p"}, false)
	if err != nil || len(done) != 1 || done[0] != "p" {
		t.Fatalf("reset: %v, %v", done, err)
	}
	if r := f.revisions("p")[0]; r.State != storage.RevisionPending || r.Vrev != 0 || r.MinVrev != 10 {
		t.Errorf("after reset: %+v", r)
	}
	if again, _ := f.s.ResetRevisions(ctx, f.project, []string{"p"}, false); len(again) != 0 {
		t.Errorf("a pending revision is not reset again: %v", again)
	}
	if got := f.resolve("p", "1.0", 4); got != 10 {
		t.Errorf("recomputed vrev %d, want the old 10 (min)", got)
	}
	if done, _ = f.s.ResetRevisions(ctx, f.project, nil, true); len(done) != 2 {
		t.Fatalf("reset of all: %v", done)
	}
	if got := f.resolve("q", "1.0", 4); got != 4 {
		t.Errorf("allow-decrease vrev %d, want 4", got)
	}
}

func setCases(t *testing.T, f *fixture) {
	ctx := context.Background()
	f.pin("p", "u", "c1")
	if _, err := f.s.SetRevisionVrev(ctx, f.project, "p", "c1", "", 7); err == nil {
		t.Error("a revision without version needs one")
	}
	if _, err := f.s.SetRevisionVrev(ctx, f.project, "p", "zzz", "1.0", 7); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("wrong commit: %v, want ErrConflict", err)
	}
	if _, err := f.s.SetRevisionVrev(ctx, f.project, "none", "", "1.0", 7); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown package: %v, want ErrNotFound", err)
	}
	r, err := f.s.SetRevisionVrev(ctx, f.project, "p", "c1", "1.0", 7)
	if err != nil || r.Vrev != 7 || r.Source != storage.RevisionSourceAdmin || !r.Resolved() {
		t.Fatalf("set: %+v, %v", r, err)
	}
	f.pin("p", "u", "c2")
	if got := f.resolve("p", "1.0", 3); got != 8 {
		t.Errorf("revision after an admin vrev: %d, want 8", got)
	}
}

func historyCases(t *testing.T, f *fixture) {
	ctx := context.Background()
	key := storage.HistoryKey{URL: "https://git.example/" + f.project, Lookup: "p", ConfigHash: "h", AlgoRev: 1}
	row := func(commit string, vt map[string]int) storage.SourceHistory {
		return storage.SourceHistory{
			URL: key.URL, Commit: commit, Lookup: key.Lookup, ConfigHash: key.ConfigHash, AlgoRev: key.AlgoRev,
			MainRecipe: "p.spec", Version: "1.0", ReleaseStart: 2, VT: vt, Walked: 4,
		}
	}
	if h, err := f.s.NearestHistory(ctx, key, []string{"c3", "c2", "c1"}); err != nil || h != nil {
		t.Fatalf("empty memo: %+v, %v", h, err)
	}
	for _, r := range []storage.SourceHistory{row("c1", map[string]int{"1.0": 1}), row("c2", map[string]int{"1.0": 2, "0.9": 5})} {
		if err := f.s.PutHistory(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	h, err := f.s.NearestHistory(ctx, key, []string{"c3", "c2", "c1"})
	if err != nil || h == nil || h.Commit != "c2" || h.VT["0.9"] != 5 || h.Walked != 4 || h.ReleaseStart != 2 {
		t.Errorf("nearest: %+v, %v; want c2 with its table", h, err)
	}
	if err := f.s.PutHistory(ctx, row("c2", map[string]int{"1.0": 99})); err != nil {
		t.Fatalf("second writer: %v", err)
	}
	if h, _ = f.s.NearestHistory(ctx, key, []string{"c2"}); h.VT["1.0"] != 2 {
		t.Errorf("an existing row must be kept: %+v", h)
	}
	other := key
	other.ConfigHash = "other"
	if h, _ = f.s.NearestHistory(ctx, other, []string{"c2"}); h != nil {
		t.Errorf("a row under another config hash must not match: %+v", h)
	}
	other = key
	other.AlgoRev = 2
	if h, _ = f.s.NearestHistory(ctx, other, []string{"c2"}); h != nil {
		t.Errorf("a row under another algo rev must not match: %+v", h)
	}
}
