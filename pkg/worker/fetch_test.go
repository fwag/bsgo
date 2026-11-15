package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// memStore is an in-memory ObjectStore.
type memStore struct {
	objects map[string][]byte
	gets    atomic.Int32
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (m *memStore) GetObject(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	m.gets.Add(1)
	data, ok := m.objects[key]
	if !ok {
		return nil, 0, "", fmt.Errorf("no such key %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), "", nil
}

func (m *memStore) UploadFile(_ context.Context, local, key string) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	m.objects[key] = data
	return nil
}

func (m *memStore) DownloadPrefix(context.Context, string, string) (int, error) { return 0, nil }
func (m *memStore) Bucket() string                                              { return "mem" }

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFetchBdeps(t *testing.T) {
	s3Payload := []byte("rpm-from-s3")
	dodPayload := []byte("rpm-from-dod")
	var httpHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		if r.URL.Path != "/repo/Packages/b/bash.rpm" {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(dodPayload)
	}))
	defer srv.Close()

	store := newMemStore()
	store.objects["repos/p/t/a/glibc.rpm"] = s3Payload

	bdep := func(name string, payload []byte, src v1.BdepSource) v1.Bdep {
		return v1.Bdep{Name: name, Version: "1", Release: "1", Arch: "x86_64", SHA256: shaOf(payload), Size: int64(len(payload)), Source: src}
	}
	glibc := bdep("glibc", s3Payload, v1.BdepSource{S3Key: "repos/p/t/a/glibc.rpm"})
	bash := bdep("bash", dodPayload, v1.BdepSource{URL: srv.URL + "/repo/Packages/b/bash.rpm"})

	tests := []struct {
		name    string
		bdeps   []v1.Bdep
		wantErr string
	}{
		{"s3 and dod", []v1.Bdep{glibc, bash}, ""},
		{"sha mismatch fails", []v1.Bdep{func() v1.Bdep { b := glibc; b.SHA256 = shaOf([]byte("other")); return b }()}, "sha256 mismatch"},
		{"size mismatch fails", []v1.Bdep{func() v1.Bdep { b := glibc; b.Size = 3; return b }()}, "size mismatch"},
		{"placeholder digest rejected", []v1.Bdep{func() v1.Bdep { b := glibc; b.SHA256 = "mock-sha256-digest"; return b }()}, "no valid sha256"},
		{"empty digest rejected", []v1.Bdep{func() v1.Bdep { b := glibc; b.SHA256 = ""; return b }()}, "no valid sha256"},
		{"missing s3 object", []v1.Bdep{func() v1.Bdep { b := glibc; b.Source.S3Key = "nope"; return b }()}, "no such key"},
		{"http 404", []v1.Bdep{func() v1.Bdep { b := bash; b.Source.URL = srv.URL + "/missing"; return b }()}, "404"},
		{"both sources", []v1.Bdep{func() v1.Bdep { b := glibc; b.Source.URL = srv.URL; return b }()}, "exactly one"},
		{"no source", []v1.Bdep{func() v1.Bdep { b := glibc; b.Source = v1.BdepSource{}; return b }()}, "exactly one"},
		{"path traversal name", []v1.Bdep{func() v1.Bdep { b := glibc; b.Name = "../evil"; return b }()}, "unsafe"},
		{"file url", []v1.Bdep{func() v1.Bdep { b := bash; b.Source.URL = "file:///etc/passwd"; return b }()}, "unsupported source url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &ProcessSandbox{WorkDir: t.TempDir(), S3Storage: store}
			dir := filepath.Join(t.TempDir(), "bdeps")
			err := s.fetchBdeps(context.Background(), tc.bdeps, dir, io.Discard)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range tc.bdeps {
				got, readErr := os.ReadFile(filepath.Join(dir, b.Filename()))
				if readErr != nil || shaOf(got) != b.SHA256 {
					t.Errorf("bdep %s not placed correctly: %v", b.Name, readErr)
				}
			}
		})
	}
}

func TestFetchBdepsCacheReuseAndHealing(t *testing.T) {
	payload := []byte("cached-rpm")
	store := newMemStore()
	store.objects["k"] = payload
	b := v1.Bdep{Name: "x", Version: "1", Release: "1", Arch: "noarch", SHA256: shaOf(payload), Size: int64(len(payload)), Source: v1.BdepSource{S3Key: "k"}}
	s := &ProcessSandbox{WorkDir: t.TempDir(), CacheDir: t.TempDir(), S3Storage: store}
	ctx := context.Background()

	if err := s.fetchBdeps(ctx, []v1.Bdep{b}, filepath.Join(t.TempDir(), "a"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := s.fetchBdeps(ctx, []v1.Bdep{b}, filepath.Join(t.TempDir(), "b"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := store.gets.Load(); got != 1 {
		t.Errorf("second job should hit the cache, S3 gets = %d", got)
	}

	// Corrupt the cache entry: it must be detected and re-downloaded.
	cached := s.bdepCachePath(b.SHA256)
	if err := os.Chmod(cached, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "c")
	if err := s.fetchBdeps(ctx, []v1.Bdep{b}, dir, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, b.Filename()))
	if !bytes.Equal(got, payload) || store.gets.Load() != 2 {
		t.Errorf("corrupt cache entry not healed (gets=%d)", store.gets.Load())
	}
}

func TestFetchBdepsFailedDownloadLeavesNoCacheEntry(t *testing.T) {
	store := newMemStore()
	store.objects["k"] = []byte("actual")
	b := v1.Bdep{Name: "x", Version: "1", Release: "1", Arch: "noarch", SHA256: shaOf([]byte("expected")), Source: v1.BdepSource{S3Key: "k"}}
	s := &ProcessSandbox{WorkDir: t.TempDir(), S3Storage: store}
	if err := s.fetchBdeps(context.Background(), []v1.Bdep{b}, filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
		t.Fatal("expected mismatch error")
	}
	if checkFileExists(s.bdepCachePath(b.SHA256)) {
		t.Error("mismatching download must not enter the cache")
	}
}
