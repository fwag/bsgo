package dod_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openSUSE/bsgo/pkg/dod"
)

type memoryS3Store struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemoryS3Store() *memoryS3Store {
	return &memoryS3Store{objects: make(map[string][]byte)}
}

func (m *memoryS3Store) GetObject(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.objects[key]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("object not found: %s", key)
}

func (m *memoryS3Store) PutObject(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func TestExternalRepoIngestor(t *testing.T) {
	ctx := context.Background()

	repomdXML := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary">
    <location href="repodata/test-primary.xml.gz"/>
  </data>
</repomd>`

	primaryContent := []byte("fake-compressed-primary-data")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(repomdXML))
		case "/repodata/test-primary.xml.gz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(primaryContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	ingestor := dod.NewExternalRepoIngestor(nil)

	data, err := ingestor.FetchPrimary(ctx, ts.URL)
	if err != nil {
		t.Fatalf("FetchPrimary failed: %v", err)
	}

	if string(data) != string(primaryContent) {
		t.Errorf("expected %s, got %s", primaryContent, data)
	}

	// Verify caching on second call
	data2, err := ingestor.FetchPrimary(ctx, ts.URL)
	if err != nil {
		t.Fatalf("cached FetchPrimary failed: %v", err)
	}
	if string(data2) != string(primaryContent) {
		t.Errorf("expected cached %s, got %s", primaryContent, data2)
	}
}

func TestDoDFetcherAndS3Caching(t *testing.T) {
	ctx := context.Background()

	rpmPayload := []byte("authentic-binary-rpm-package-payload-bytes")
	sum := sha256.Sum256(rpmPayload)
	expectedHash := hex.EncodeToString(sum[:])

	httpHits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/packages/x86_64/my-tool-1.0.rpm" {
			httpHits++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rpmPayload)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	s3Store := newMemoryS3Store()
	fetcher := dod.NewDoDFetcher(nil, s3Store)

	// 1. Initial fetch -> downloads and caches in S3 CAS
	s3Key, err := fetcher.EnsurePackageCached(ctx, ts.URL+"/packages/x86_64", "my-tool-1.0.rpm", expectedHash)
	if err != nil {
		t.Fatalf("EnsurePackageCached failed: %v", err)
	}

	expectedKey := fmt.Sprintf("repos/external/%s/my-tool-1.0.rpm", expectedHash)
	if s3Key != expectedKey {
		t.Errorf("expected s3Key %s, got %s", expectedKey, s3Key)
	}
	if httpHits != 1 {
		t.Errorf("expected 1 HTTP hit, got %d", httpHits)
	}

	// 2. Second fetch -> serves directly from S3 CAS without hitting HTTP server
	s3Key2, err := fetcher.EnsurePackageCached(ctx, ts.URL+"/packages/x86_64", "my-tool-1.0.rpm", expectedHash)
	if err != nil {
		t.Fatalf("EnsurePackageCached second call failed: %v", err)
	}
	if s3Key2 != expectedKey {
		t.Errorf("expected s3Key %s, got %s", expectedKey, s3Key2)
	}
	if httpHits != 1 {
		t.Errorf("expected no additional HTTP hits, got %d", httpHits)
	}

	// 3. Mismatched checksum rejection
	_, err = fetcher.EnsurePackageCached(ctx, ts.URL+"/packages/x86_64", "my-tool-1.0.rpm", "corruptedhash000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Errorf("expected error on mismatched checksum")
	}
}
