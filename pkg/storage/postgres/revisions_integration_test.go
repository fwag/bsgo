package postgres_test

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
	"github.com/openSUSE/bsgo/pkg/storage/storagetest"
)

func TestPostgresRevisions(t *testing.T) {
	store, prefix := newCheckerStore(t)
	storagetest.Revisions(t, store, prefix, func(prefix string) {
		_, err := store.Pool().Exec(context.Background(),
			`DELETE FROM package_revisions WHERE project LIKE $1 || ':%'`, prefix)
		if err != nil {
			t.Error(err)
		}
	})
}

func TestPostgresRevisionSeedMigration(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	_, err := store.Pool().Exec(ctx, `INSERT INTO projects (name, title, description, manifest_yaml) VALUES ($1, '', '', '')`, prj)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][]string{{"a", "u", "c1"}, {"b", "", ""}} {
		_, err = store.Pool().Exec(ctx, `
			INSERT INTO project_packages (project_name, package_name, shard, git_url, branch, commit_sha, enabled, manifest_yaml)
			VALUES ($1, $2, 'x', $3, '', $4, true, '')`, prj, p[0], p[1], p[2])
		if err != nil {
			t.Fatal(err)
		}
	}
	seed, err := migrationSeedSQL()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Second)
	if _, err := store.Pool().Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	// The seed also covers the packages other tests left in the database.
	t.Cleanup(func() {
		_, err := store.Pool().Exec(ctx, `DELETE FROM package_revisions WHERE created_at >= $1 AND project <> $2`, started, prj)
		if err != nil {
			t.Error(err)
		}
	})
	if _, err := store.Pool().Exec(ctx, `DELETE FROM package_revisions WHERE created_at >= $1 AND project <> $2`, started, prj); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListRevisions(ctx, prj, nil)
	if err != nil || len(list) != 1 || list[0].Package != "a" || list[0].Seq != 1 || list[0].State != storage.RevisionPending {
		t.Fatalf("seeded revisions %+v, %v: want one pending revision for the pinned package", list, err)
	}
}

func TestPostgresConcurrentReplicas(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	key := storage.HistoryKey{URL: "https://git.example/" + prj, Lookup: "p", ConfigHash: "h", AlgoRev: 1}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.PutHistory(ctx, storage.SourceHistory{
				URL: key.URL, Commit: "c", Lookup: "p", ConfigHash: "h", AlgoRev: 1, Version: "1.0",
				VT: map[string]int{"1.0": i}, Walked: 1,
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent PutHistory: %v", err)
		}
	}
	if h, err := store.NearestHistory(ctx, key, []string{"c"}); err != nil || h == nil {
		t.Errorf("row missing: %v", err)
	}
	if err := store.UpsertProject(ctx, &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: prj}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := store.Pool().Exec(ctx, `DELETE FROM package_revisions WHERE project = $1`, prj)
		if err != nil {
			t.Error(err)
		}
	})
	concurrentClaims(t, store, prj)
}

// concurrentClaims has several replicas claim and finish the same revisions:
// every revision is claimed once.
func concurrentClaims(t *testing.T, store storage.Store, prj string) {
	t.Helper()
	ctx := context.Background()
	for i := range 12 {
		pinPackage(t, store, prj, fmt.Sprintf("p%02d", i))
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				got, err := store.ClaimRevisions(ctx, "replica", 3, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				for _, r := range got {
					if r.Project == prj {
						seen[r.Package]++
					}
				}
				done := len(seen) == 12
				mu.Unlock()
				if done {
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != 12 {
		t.Fatalf("claimed %d of 12 packages", len(seen))
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("%s claimed %d times by concurrent replicas", p, n)
		}
	}
}

func pinPackage(t *testing.T, store storage.Store, prj, name string) {
	t.Helper()
	pm := &manifest.PackageManifest{
		SchemaVersion: "v1", Package: manifest.PackageInfo{Name: name},
		Source: manifest.PackageSource{Git: "https://git.example/" + name, Commit: "c1"}, Build: manifest.PackageBuild{Enabled: true},
	}
	if err := store.UpsertPackage(context.Background(), prj, pm); err != nil {
		t.Fatal(err)
	}
}

// migrationSeedSQL returns the seeding INSERT of migration 00016.
func migrationSeedSQL() (string, error) {
	data, err := fs.ReadFile(postgres.EmbedMigrations, "migrations/00016_release_counters.sql")
	if err != nil {
		return "", err
	}
	text := string(data)
	start := strings.Index(text, "INSERT INTO package_revisions")
	end := strings.Index(text, "-- +goose Down")
	if start < 0 || end < start {
		return "", fmt.Errorf("seed statement not found in the migration")
	}
	return text[start:end], nil
}
