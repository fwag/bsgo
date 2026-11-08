package s3_test

import (
	"context"
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
