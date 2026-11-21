package worker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestPublishHermeticResults(t *testing.T) {
	jobDir := t.TempDir()
	pkgDir := filepath.Join(jobDir, "packages")
	buildRoot := filepath.Join(jobDir, "build-root")
	for _, d := range []string{pkgDir, filepath.Join(buildRoot, ".build.packages/OTHER")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(pkgDir, "image.iso"):                            "iso",
		filepath.Join(jobDir, "build.log"):                            "log",
		filepath.Join(buildRoot, ".build.packages/OTHER/_statistics"): "<buildstatistics/>",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bi := fixtureBuildInfo()
	job := &v1.BuildJob{JobID: "j", Project: "P", Package: "gcc", Target: "T", Architecture: "A", BuildInfo: bi}
	arts := []v1.ChrootRPM{{Filename: "image.iso", SHA256: shaOf([]byte("iso")), SizeBytes: 3}}
	store := newMemStore()
	s := &ProcessSandbox{WorkDir: t.TempDir(), S3Storage: store}

	res, err := s.publishHermeticResults(context.Background(), buildRoot, jobDir, pkgDir, job, arts, bi.SourceCommit, io.Discard, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	prefix := "artifacts/P/T/A/gcc:gcc13/"
	for _, name := range []string{"image.iso", "_slsa_provenance.json", "build.log", "_statistics"} {
		if _, ok := store.objects[prefix+name]; !ok {
			t.Errorf("%s%s not uploaded; have %v", prefix, name, keys(store))
		}
	}
	if res.SourceCommit != bi.SourceCommit || res.Meta != "meta-digest" || res.Release != "1.2" {
		t.Errorf("result lacks provenance fields: %+v", res)
	}
	for _, a := range res.Artifacts {
		if a.S3Key != prefix+a.Filename {
			t.Errorf("artifact %s reports key %s", a.Filename, a.S3Key)
		}
	}
	if len(store.objects) != 4 {
		t.Errorf("unexpected objects: %v", keys(store))
	}
}

func TestPublishHermeticResultsFailures(t *testing.T) {
	job := &v1.BuildJob{JobID: "j", Project: "P", Package: "x", Target: "T", Architecture: "A", BuildInfo: fixtureBuildInfo()}
	arts := []v1.ChrootRPM{{Filename: "a.iso"}}
	tests := []struct {
		name  string
		store ObjectStore
		arts  []v1.ChrootRPM
	}{
		{"no store", nil, arts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			s := &ProcessSandbox{WorkDir: d, S3Storage: tc.store}
			if _, err := s.publishHermeticResults(context.Background(), d, d, d, job, tc.arts, "c", io.Discard, time.Now()); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func keys(m *memStore) []string {
	var out []string
	for k := range m.objects {
		out = append(out, k)
	}
	return out
}

// TestPublishHermeticResultsWithoutBinaries covers a build that exits 0 and
// produces no binary packages (a test-only flavor): it succeeds, uploads
// provenance and log, and reports no bininfo.json so nothing is integrated.
func TestPublishHermeticResultsWithoutBinaries(t *testing.T) {
	tests := []struct {
		name  string
		log   bool
		wants []string
	}{
		{"nothing but provenance", false, []string{"_slsa_provenance.json"}},
		{"provenance and log", true, []string{"_slsa_provenance.json", "build.log"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jobDir := t.TempDir()
			pkgDir := filepath.Join(jobDir, "packages")
			if err := os.MkdirAll(pkgDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if tc.log {
				if err := os.WriteFile(filepath.Join(jobDir, "build.log"), []byte("log"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bi := fixtureBuildInfo()
			job := &v1.BuildJob{JobID: "j", Project: "P", Package: "c-ares", Target: "T", Architecture: "A", BuildInfo: bi}
			store := newMemStore()
			s := &ProcessSandbox{WorkDir: t.TempDir(), S3Storage: store}

			res, err := s.publishHermeticResults(context.Background(), jobDir, jobDir, pkgDir, job, nil, bi.SourceCommit, io.Discard, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode != 0 || res.Meta != bi.Meta || res.SourceCommit != bi.SourceCommit {
				t.Errorf("result %+v is not a plain success", res)
			}
			prefix := "artifacts/P/T/A/c-ares:gcc13/"
			if len(store.objects) != len(tc.wants) || len(res.Artifacts) != len(tc.wants) {
				t.Errorf("objects %v, artifacts %v, want %v", keys(store), res.Artifacts, tc.wants)
			}
			for _, name := range tc.wants {
				if _, ok := store.objects[prefix+name]; !ok {
					t.Errorf("%s%s not uploaded; have %v", prefix, name, keys(store))
				}
			}
		})
	}
}
