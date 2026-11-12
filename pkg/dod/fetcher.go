package dod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// S3Store defines minimal object storage interactions needed by the DoD fetcher.
type S3Store interface {
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, data []byte) error
}

// DoDFetcher downloads packages on demand from upstream mirrors and caches them in S3 CAS.
type DoDFetcher struct {
	mu     sync.Mutex
	getter HTTPGetter
	s3     S3Store
}

// NewDoDFetcher creates a new Download-on-Demand fetcher.
func NewDoDFetcher(getter HTTPGetter, s3 S3Store) *DoDFetcher {
	if getter == nil {
		getter = NewDefaultHTTPGetter()
	}
	return &DoDFetcher{
		getter: getter,
		s3:     s3,
	}
}

// EnsurePackageCached verifies if the requested package is in S3 CAS, and downloads it if missing.
func (f *DoDFetcher) EnsurePackageCached(ctx context.Context, baseURL, relativeLocation, expectedSHA256 string) (string, error) {
	filename := filepath.Base(relativeLocation)
	digest := expectedSHA256
	if digest == "" {
		digest = "unverified"
	}

	s3Key := fmt.Sprintf("repos/external/%s/%s", digest, filename)

	// 1. Check if package is already cached in S3 CAS
	if f.s3 != nil {
		if _, err := f.s3.GetObject(ctx, s3Key); err == nil {
			return s3Key, nil
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Re-check under lock in case another goroutine fetched it
	if f.s3 != nil {
		if _, err := f.s3.GetObject(ctx, s3Key); err == nil {
			return s3Key, nil
		}
	}

	// 2. Download binary package from upstream URL
	pkgURL := strings.TrimSuffix(baseURL, "/") + "/" + strings.TrimPrefix(relativeLocation, "/")
	pkgData, err := f.getter.Get(ctx, pkgURL)
	if err != nil {
		return "", fmt.Errorf("failed downloading on-demand package from %s: %w", pkgURL, err)
	}

	// 3. Cryptographic checksum validation
	sum := sha256.Sum256(pkgData)
	actualSHA := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && !strings.EqualFold(actualSHA, expectedSHA256) {
		return "", fmt.Errorf("cryptographic checksum mismatch for %s: expected %s, got %s",
			filename, expectedSHA256, actualSHA)
	}

	// If expectedSHA256 was empty, update s3Key with actual computed SHA-256
	if expectedSHA256 == "" {
		s3Key = fmt.Sprintf("repos/external/%s/%s", actualSHA, filename)
	}

	// 4. Cache package in S3 CAS
	if f.s3 != nil {
		if err := f.s3.PutObject(ctx, s3Key, pkgData); err != nil {
			return "", fmt.Errorf("failed caching on-demand package in S3 (%s): %w", s3Key, err)
		}
	}

	return s3Key, nil
}
