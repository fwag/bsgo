package publisher_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

type MockS3Client struct {
	UploadedKeys []string
	FilesOnS3    map[string][]byte
}

func NewMockS3Client() *MockS3Client {
	return &MockS3Client{
		FilesOnS3: make(map[string][]byte),
	}
}

func (s *MockS3Client) DownloadMetadata(_ context.Context, prefix, localDir string) error {
	_ = os.MkdirAll(localDir, 0755)
	for k, v := range s.FilesOnS3 {
		if strings.HasPrefix(k, prefix) {
			rel := strings.TrimPrefix(k, prefix+"/")
			dst := filepath.Join(localDir, rel)
			_ = os.WriteFile(dst, v, 0644)
		}
	}
	return nil
}

func (s *MockS3Client) UploadFile(_ context.Context, localPath, s3Key string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	s.UploadedKeys = append(s.UploadedKeys, s3Key)
	s.FilesOnS3[s3Key] = data
	return nil
}

func TestRealCreateRepoExecution(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	repoDir := filepath.Join(tmpDir, "myrepo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	cr := publisher.NewRealCreateRepoExecutor()
	opts := publisher.CreateRepoOptions{
		Dir: repoDir,
	}

	// 1. Initial run on empty repo
	if err := cr.Run(ctx, opts); err != nil {
		t.Fatalf("createrepo_c failed on empty repo: %v", err)
	}

	repomdPath := filepath.Join(repoDir, "repodata", "repomd.xml")
	if _, err := os.Stat(repomdPath); os.IsNotExist(err) {
		t.Fatalf("expected repomd.xml to be created at %s", repomdPath)
	}

	// 2. Incremental update with --update and --recycle-pkglist
	opts.Update = true
	opts.RecyclePkgList = true
	opts.OldMetadataDir = filepath.Join(repoDir, "repodata")
	if err := cr.Run(ctx, opts); err != nil {
		t.Fatalf("createrepo_c incremental update failed: %v", err)
	}
}

func TestPublisherAtomicUploadWorkflow(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cr := publisher.NewRealCreateRepoExecutor()
	s3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(cr, s3, lockMgr, tmpDir)

	req := publisher.PublishRequest{
		RequestID:    "pub-req-01",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error processing publish request: %v", err)
	}

	if res.RepomdXMLKey != "repos/openSUSE:Factory/standard/x86_64/repodata/repomd.xml" {
		t.Errorf("unexpected repomd key: %s", res.RepomdXMLKey)
	}

	// Verify Commit-By-Pointer: repomd.xml MUST be the very last file uploaded!
	if len(s3.UploadedKeys) == 0 {
		t.Fatalf("expected uploaded files to S3")
	}

	lastKey := s3.UploadedKeys[len(s3.UploadedKeys)-1]
	if !strings.HasSuffix(lastKey, "repomd.xml") {
		t.Errorf("atomic publication order violation: expected repomd.xml to be uploaded last, got: %s", lastKey)
	}
}
