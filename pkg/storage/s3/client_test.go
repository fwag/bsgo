package s3_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

func TestNewS3StorageConfig(t *testing.T) {
	ctx := context.Background()

	// Verify client constructor validates parameters and initializes properly
	storage, err := s3.NewS3Storage(ctx, "http://localhost:3900", "garage", "test-bucket", "key-id", "secret-key")
	if err != nil {
		t.Fatalf("failed to create S3 storage: %v", err)
	}
	if storage == nil {
		t.Fatalf("expected non-nil storage instance")
	}
}

func TestGarageS3BaseSystemRepo(t *testing.T) {
	ctx := context.Background()
	storage, err := s3.NewS3Storage(ctx, "http://localhost:3900", "garage", "bsgo-artifacts", "", "")
	if err != nil {
		t.Skip("garage s3 not available")
	}
	repoDir := "/home/ta/bsgo-worker-kvm/repos/Base:System/standard/x86_64"
	prefix := "repos/Base:System/standard/x86_64/"
	_ = filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(repoDir, path)
		return storage.UploadFile(ctx, path, prefix+rel)
	})

	tmp := t.TempDir()
	count, err := storage.DownloadPrefix(ctx, prefix, tmp)
	if err != nil {
		t.Fatalf("download prefix err: %v", err)
	}
	t.Logf("Downloaded %d files from S3 repos/Base:System/standard/x86_64/", count)
	if count < 4 {
		t.Fatalf("expected at least 4 files in S3 repo, got %d", count)
	}
}
