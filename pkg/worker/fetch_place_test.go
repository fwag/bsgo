package worker

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// The same bdep twice in one job (liblua5_4-5 on the KVM worker) used to fail
// with "permission denied": the second hard link hit EEXIST and the copy
// fallback could not overwrite the read-only first one.
func TestFetchBdepsPlacingIsIdempotent(t *testing.T) {
	payload := []byte("lua-rpm")
	other := []byte("other-build-of-the-same-file")
	mk := func(content []byte) v1.Bdep {
		return v1.Bdep{Name: "liblua5_4-5", Version: "5.4.7", Release: "160000.1.3", Arch: "x86_64",
			SHA256: shaOf(content), Size: int64(len(content)), Source: v1.BdepSource{S3Key: "k" + shaOf(content)}}
	}
	tests := []struct {
		name     string
		bdeps    []v1.Bdep
		existing []byte // content already at the destination
		readOnly bool
	}{
		{"listed twice", []v1.Bdep{mk(payload), mk(payload)}, nil, false},
		{"listed three times", []v1.Bdep{mk(payload), mk(payload), mk(payload)}, nil, false},
		{"destination already correct and read-only", []v1.Bdep{mk(payload)}, payload, true},
		{"destination holds other content", []v1.Bdep{mk(payload)}, other, false},
		{"destination holds other content, read-only", []v1.Bdep{mk(payload)}, other, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			store.objects["k"+shaOf(payload)] = payload
			s := &ProcessSandbox{WorkDir: t.TempDir(), CacheDir: t.TempDir(), S3Storage: store}
			dir := filepath.Join(t.TempDir(), "bdeps")
			dest := filepath.Join(dir, tc.bdeps[0].Filename())
			if tc.existing != nil {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0o600)
				if tc.readOnly {
					mode = 0o400
				}
				if err := os.WriteFile(dest, tc.existing, mode); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.fetchBdeps(context.Background(), tc.bdeps, dir, io.Discard); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("destination content %q (%v), want %q", got, err, payload)
			}
		})
	}
}
