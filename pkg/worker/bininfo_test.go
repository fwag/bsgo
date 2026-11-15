package worker

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func buildRPM(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("rpmbuild"); err != nil {
		t.Skip("rpmbuild not available")
	}
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, "SPECS"), 0o750); err != nil {
		t.Fatal(err)
	}
	spec := "Name: wk\nVersion: 1\nRelease: 1\nSummary: s\nLicense: MIT\nBuildArch: noarch\n%description\nx\n%files\n"
	specPath := filepath.Join(top, "SPECS", "wk.spec")
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("rpmbuild", "--define", "_topdir "+top, "-bb", specPath).CombinedOutput(); err != nil {
		t.Skipf("rpmbuild failed: %v: %s", err, out)
	}
	built := filepath.Join(top, "RPMS", "noarch", "wk-1-1.noarch.rpm")
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "wk-1-1.noarch.rpm")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestUploadBinInfo(t *testing.T) {
	job := &v1.BuildJob{Project: "p", Package: "pkg"}
	tests := []struct {
		name      string
		files     map[string]string
		realRPM   bool
		wantErr   bool
		wantFile  bool
		artifacts []string
	}{
		{name: "real rpm", realRPM: true, wantFile: true, artifacts: []string{"wk-1-1.noarch.rpm"}},
		{name: "corrupt rpm fails the job", files: map[string]string{"bad.rpm": "junk"}, wantErr: true, artifacts: []string{"bad.rpm"}},
		{name: "no rpm artifacts", files: map[string]string{"image.iso": "x"}, artifacts: []string{"image.iso"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pkgDir := t.TempDir()
			if tc.realRPM {
				buildRPM(t, pkgDir)
			}
			var arts []v1.ChrootRPM
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(pkgDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, a := range tc.artifacts {
				arts = append(arts, v1.ChrootRPM{Filename: a})
			}
			s := &ProcessSandbox{}
			err := s.uploadBinInfo(context.Background(), job, pkgDir, "standard", "x86_64", arts, io.Discard)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			data, readErr := os.ReadFile(filepath.Join(pkgDir, bininfo.FileName))
			if tc.wantFile != (readErr == nil) {
				t.Fatalf("bininfo.json present = %v, want %v", readErr == nil, tc.wantFile)
			}
			if tc.wantFile {
				infos, err := bininfo.Unmarshal(data)
				if err != nil || len(infos) != 1 || infos[0].Name != "wk" {
					t.Errorf("unexpected bininfo content: %v %+v", err, infos)
				}
			}
		})
	}
}
