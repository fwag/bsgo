package worker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestExecuteBuildInfoRefusedInPlainChroot(t *testing.T) {
	s := &ProcessSandbox{WorkDir: t.TempDir(), SandboxMode: "obs-build"}
	job := &v1.BuildJob{JobID: "j-chroot", Project: "p", Package: "x", BuildInfo: fixtureBuildInfo()}
	if _, err := s.Execute(context.Background(), job, io.Discard); err == nil || !strings.Contains(err.Error(), "network isolation") {
		t.Fatalf("err = %v, want network isolation refusal", err)
	}
}

// TestExecuteBuildInfoInvokesObsBuildHermetically runs the whole hermetic
// pipeline against a fake obs-build that records its arguments and rpmlist.
func TestExecuteBuildInfoInvokesObsBuildHermetically(t *testing.T) {
	bare, ids := bareRepoWithHistory(t)
	payload := []byte("bdep")
	store := newMemStore()
	store.objects["k"] = payload

	work := t.TempDir()
	record := filepath.Join(work, "record")
	fake := filepath.Join(work, "fake-build")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record +
		"\nwhile [ $# -gt 0 ]; do [ \"$1\" = --rpmlist ] && cat \"$2\" >> " + record + "; shift; done\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	bi := fixtureBuildInfo()
	bi.SourceCommit = ids[1]
	bi.Bdeps = []v1.Bdep{{
		Name: "glibc", Version: "1", Release: "1", Arch: "x86_64", SHA256: shaOf(payload), Size: 4,
		Source: v1.BdepSource{S3Key: "k"}, Preinstall: true,
	}}
	job := &v1.BuildJob{
		JobID: "j-hermetic", Project: "p", Package: "x", Target: "t", Architecture: "a",
		Source:    v1.SourceSpec{GitURL: bare, RecipePath: "pkg.spec"},
		BuildInfo: bi,
	}
	s := &ProcessSandbox{WorkDir: work, SandboxMode: "obs-build", AllowUnisolatedChroot: true, ObsBuildBin: fake, S3Storage: store}
	_, err := s.Execute(context.Background(), job, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no artifacts") {
		t.Fatalf("err = %v, want 'no artifacts' (the fake build produces none)", err)
	}
	data, readErr := os.ReadFile(record)
	if readErr != nil {
		t.Fatal(readErr)
	}
	got := string(data)
	wants := []string{"--rpmlist", "--dist", "--arch\nx86_64:i686:i586:noarch", "--release\n1.2", "--buildflavor=gcc13", "glibc ", "preinstall: glibc"}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("fake obs-build did not see %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--repo") {
		t.Errorf("--repo passed to obs-build:\n%s", got)
	}
}
