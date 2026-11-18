package storage_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestBuildMetaEncoding(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{"nothing", nil},
		{"one line", []string{"s1  pkg"}},
		{"tree", []string{"s1  pkg", "i1  dep", "i2  dep/sub"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := storage.EncodeBuildMeta(tc.lines)
			if err != nil {
				t.Fatal(err)
			}
			got, err := storage.DecodeBuildMeta(data)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.lines) {
				t.Errorf("round trip gave %q, want %q", got, tc.lines)
			}
		})
	}
	if _, err := storage.DecodeBuildMeta([]byte("not gzip")); err == nil {
		t.Error("garbage decoded")
	}
}

func TestMemoryBuildMetas(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	save := func(arch string, hashes ...string) {
		t.Helper()
		var ms []storage.BuildMeta
		for _, h := range hashes {
			ms = append(ms, storage.BuildMeta{Hash: h, Lines: []string{h + "  pkg"}})
		}
		if err := s.SaveBuildMetas(ctx, "p", "r", arch, ms); err != nil {
			t.Fatal(err)
		}
	}
	hashes := func(arch string, ask ...string) []string {
		t.Helper()
		got, err := s.GetBuildMetas(ctx, "p", "r", arch, ask)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range got {
			out = append(out, m.Hash)
		}
		return out
	}
	save("x86_64", "a", "b", "c")
	save("i586", "a")
	if got := hashes("x86_64", "a", "c", "unknown"); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("got %v, want a and c", got)
	}
	// Within the grace period nothing is pruned.
	if err := s.PruneBuildMetas(ctx, "p", "r", "x86_64", []string{"b"}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := hashes("x86_64", "a", "b", "c"); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("pruned within the grace period: %v", got)
	}
	if err := s.PruneBuildMetas(ctx, "p", "r", "x86_64", []string{"b"}, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := hashes("x86_64", "a", "b", "c"); !slices.Equal(got, []string{"b"}) {
		t.Errorf("after prune got %v, want b", got)
	}
	if got := hashes("i586", "a"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("other prpa lost its meta: %v", got)
	}
}
