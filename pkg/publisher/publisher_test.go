package publisher_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
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

func TestPublisherPathTraversalRejection(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cr := publisher.NewRealCreateRepoExecutor()
	s3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()
	pub := publisher.NewPublisher(cr, s3, lockMgr, tmpDir)

	maliciousReqs := []publisher.PublishRequest{
		{Project: "../../etc", Target: "standard", Architecture: "x86_64"},
		{Project: "openSUSE:Factory", Target: "../target", Architecture: "x86_64"},
		{Project: "openSUSE:Factory", Target: "standard", Architecture: "../../x86_64"},
	}

	for _, req := range maliciousReqs {
		_, err := pub.ProcessPublishRequest(ctx, req)
		if err == nil {
			t.Errorf("expected error for malicious publish request %+v, got nil", req)
		}
	}
}

type MockEventPublisher struct {
	PublishedEvents []v1.PublicationEvent
}

func (m *MockEventPublisher) PublishEvent(_ context.Context, _ string, data []byte) error {
	var event v1.PublicationEvent
	if err := json.Unmarshal(data, &event); err == nil {
		m.PublishedEvents = append(m.PublishedEvents, event)
	}
	return nil
}

type MockHelmIndexExecutor struct {
	Invoked bool
	Dir     string
}

func (m *MockHelmIndexExecutor) Run(_ context.Context, opts publisher.HelmIndexOptions) error {
	m.Invoked = true
	m.Dir = opts.Dir
	// Create mock index.yaml
	indexPath := filepath.Join(opts.Dir, "index.yaml")
	return os.WriteFile(indexPath, []byte("apiVersion: v1\nentries: {}\n"), 0644)
}

type MockCreateRepoExecutor struct {
	Invoked bool
}

func (m *MockCreateRepoExecutor) Run(_ context.Context, opts publisher.CreateRepoOptions) error {
	m.Invoked = true
	repodataDir := filepath.Join(opts.Dir, "repodata")
	_ = os.MkdirAll(repodataDir, 0755)
	_ = os.WriteFile(filepath.Join(repodataDir, "primary.xml.gz"), []byte("mock-primary"), 0644)
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte("<repomd/>"), 0644)
	return nil
}

func TestPublisherRepoTypeNoneBypass(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	mockCR := &MockCreateRepoExecutor{}
	mockS3 := NewMockS3Client()
	mockEvents := &MockEventPublisher{}
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(mockCR, mockS3, lockMgr, tmpDir)
	pub.SetEventPublisher(mockEvents)

	req := publisher.PublishRequest{
		RequestID:    "req-none-01",
		Project:      "openSUSE:Images",
		Target:       "appliance",
		Architecture: "x86_64",
		RepoTypes:    []string{"none"},
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error for repo_types: ['none']: %v", err)
	}

	if mockCR.Invoked {
		t.Errorf("createrepo_c should NOT be invoked when repo_types is ['none']")
	}
	if len(mockS3.UploadedKeys) != 0 {
		t.Errorf("expected 0 uploads to S3 for 'none', got %d (%v)", len(mockS3.UploadedKeys), mockS3.UploadedKeys)
	}
	if res.RepomdXMLKey != "" {
		t.Errorf("expected empty RepomdXMLKey for 'none', got %q", res.RepomdXMLKey)
	}
	if len(mockEvents.PublishedEvents) != 1 {
		t.Fatalf("expected 1 publication event emitted, got %d", len(mockEvents.PublishedEvents))
	}
	if mockEvents.PublishedEvents[0].RepomdKey != "" {
		t.Errorf("expected empty RepomdKey in event, got %q", mockEvents.PublishedEvents[0].RepomdKey)
	}
}

func TestPublisherRepoTypeHelm(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	mockCR := &MockCreateRepoExecutor{}
	mockHelm := &MockHelmIndexExecutor{}
	mockS3 := NewMockS3Client()
	mockEvents := &MockEventPublisher{}
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(mockCR, mockS3, lockMgr, tmpDir)
	pub.SetHelmExecutor(mockHelm)
	pub.SetEventPublisher(mockEvents)

	req := publisher.PublishRequest{
		RequestID:    "req-helm-01",
		Project:      "cloud-charts",
		Target:       "helm-repo",
		Architecture: "x86_64",
		RepoTypes:    []string{"helm"},
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error for repo_types: ['helm']: %v", err)
	}

	if mockCR.Invoked {
		t.Errorf("createrepo_c should NOT be invoked when repo_types is strictly ['helm']")
	}
	if !mockHelm.Invoked {
		t.Errorf("expected helm executor to be invoked")
	}
	expectedKey := "repos/cloud-charts/helm-repo/x86_64/index.yaml"
	if res.IndexYAMLKey != expectedKey {
		t.Errorf("expected IndexYAMLKey %q, got %q", expectedKey, res.IndexYAMLKey)
	}
	if len(mockS3.UploadedKeys) != 1 || mockS3.UploadedKeys[0] != expectedKey {
		t.Errorf("expected S3 upload of %q, got %v", expectedKey, mockS3.UploadedKeys)
	}
	if len(mockEvents.PublishedEvents) != 1 || mockEvents.PublishedEvents[0].RepomdKey != expectedKey {
		t.Errorf("expected event RepomdKey %q, got %v", expectedKey, mockEvents.PublishedEvents)
	}
}

func TestPublisherMultiRepoTypes(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	mockCR := &MockCreateRepoExecutor{}
	mockHelm := &MockHelmIndexExecutor{}
	mockS3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(mockCR, mockS3, lockMgr, tmpDir)
	pub.SetHelmExecutor(mockHelm)

	req := publisher.PublishRequest{
		RequestID:    "req-multi-01",
		Project:      "mixed-repo",
		Target:       "standard",
		Architecture: "x86_64",
		RepoTypes:    []string{"rpm-md", "helm"},
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error for multi repo types: %v", err)
	}

	if !mockCR.Invoked {
		t.Errorf("expected createrepo_c to be invoked for rpm-md")
	}
	if !mockHelm.Invoked {
		t.Errorf("expected helm index to be invoked for helm")
	}
	if res.RepomdXMLKey == "" {
		t.Errorf("expected non-empty RepomdXMLKey")
	}
	if res.IndexYAMLKey == "" {
		t.Errorf("expected non-empty IndexYAMLKey")
	}
}
