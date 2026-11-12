package publisher_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestPublisherTombstonePurge(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	mockCR := &MockCreateRepoExecutor{}
	mockS3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(mockCR, mockS3, lockMgr, tmpDir)

	// Create local repo directory with an existing RPM to be deleted
	stdDir := filepath.Join(tmpDir, "openSUSE:Factory", "standard", "x86_64")
	_ = os.MkdirAll(stdDir, 0755)
	deletedRPM := filepath.Join(stdDir, "obsolete-tool-1.0.x86_64.rpm")
	_ = os.WriteFile(deletedRPM, []byte("rpm-content"), 0644)

	// Add the file to S3 as well
	s3Key := "repos/openSUSE:Factory/standard/x86_64/obsolete-tool-1.0.x86_64.rpm"
	mockS3.FilesOnS3[s3Key] = []byte("rpm-content")

	req := v1.PublishRequest{
		RequestID:    "del-req-01",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
		DeletedRPMs:  []string{"obsolete-tool-1.0.x86_64.rpm"},
	}

	_, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("ProcessPublishRequest failed: %v", err)
	}

	// 1. Verify file was removed from local filesystem
	if _, err := os.Stat(deletedRPM); err == nil {
		t.Errorf("expected obsolete RPM to be purged from local staging directory")
	}

	// 2. Verify file was deleted from S3
	if _, ok := mockS3.FilesOnS3[s3Key]; ok {
		t.Errorf("expected obsolete RPM to be deleted from S3")
	}

	// 3. Verify createrepo was executed to rebuild repodata without the deleted package
	if len(mockCR.Dirs) == 0 {
		t.Errorf("expected createrepo_c to be run after tombstone purge")
	}
}
