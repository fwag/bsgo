package checker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func eventsEnv(t *testing.T) (*checker.Events, *coord.Memory) {
	t.Helper()
	store := storage.NewMemoryStore()
	add := func(name string, archs []string, deps ...manifest.DependencySource) {
		err := store.UpsertProject(context.Background(), &manifest.ProjectManifest{
			SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: name},
			Targets: map[string]manifest.BuildTarget{"standard": {Architectures: archs, Dependencies: deps}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("base", []string{"x86_64", "aarch64"})
	add("mid", []string{"x86_64"}, manifest.DependencySource{Project: "base", Target: "standard"})
	add("top", []string{"x86_64", "aarch64"}, manifest.DependencySource{Project: "mid", Target: "standard"},
		manifest.DependencySource{Project: "base", Target: "standard"})
	add("unrelated", []string{"x86_64"}, manifest.DependencySource{URL: "http://x/", Name: "base/standard"})
	c := &coord.Memory{}
	return &checker.Events{Projects: store, Marker: c}, c
}

func dirty(t *testing.T, c *coord.Memory) string {
	t.Helper()
	marks, err := c.Dirty(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range marks {
		out = append(out, m.Prpa)
	}
	return strings.Join(out, " ")
}

func TestEventsDirtyTheRightPrpas(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		do   func(e *checker.Events) error
		want string
	}{
		{"binaries in base x86_64 dirty every consumer of that arch",
			func(e *checker.Events) error {
				return e.BinariesIntegrated(ctx, checker.Prpa{Project: "base", Repo: "standard", Arch: "x86_64"})
			},
			"base/standard/x86_64 mid/standard/x86_64 top/standard/x86_64"},
		{"binaries in mid dirty top and itself only",
			func(e *checker.Events) error {
				return e.BinariesIntegrated(ctx, checker.Prpa{Project: "mid", Repo: "standard", Arch: "x86_64"})
			},
			"mid/standard/x86_64 top/standard/x86_64"},
		{"aarch64 binaries leave x86_64 alone",
			func(e *checker.Events) error {
				return e.BinariesIntegrated(ctx, checker.Prpa{Project: "base", Repo: "standard", Arch: "aarch64"})
			},
			"base/standard/aarch64 top/standard/aarch64"},
		{"job finished dirties exactly its prpa",
			func(e *checker.Events) error {
				return e.JobFinished(ctx, checker.Prpa{Project: "top", Repo: "standard", Arch: "x86_64"})
			},
			"top/standard/x86_64"},
		{"package change dirties all prpas of the project",
			func(e *checker.Events) error { return e.PackageChanged(ctx, "top") },
			"top/standard/aarch64 top/standard/x86_64"},
		{"config change of base reaches consumers",
			func(e *checker.Events) error { return e.ProjectConfigChanged(ctx, "mid") },
			"mid/standard/x86_64 top/standard/x86_64"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, c := eventsEnv(t)
			if err := tc.do(e); err != nil {
				t.Fatal(err)
			}
			if got := dirty(t, c); got != tc.want {
				t.Errorf("dirty %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEventsAllMarksEveryPrpa(t *testing.T) {
	e, c := eventsEnv(t)
	if err := e.All(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(dirty(t, c))); n != 6 {
		t.Errorf("marked %d prpas, want 6", n)
	}
}
