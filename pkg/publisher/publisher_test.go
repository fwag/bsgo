package publisher_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	cleanPrefix := strings.TrimSuffix(prefix, "/") + "/"
	for k, v := range s.FilesOnS3 {
		if strings.HasPrefix(k, cleanPrefix) {
			rel := strings.TrimPrefix(k, cleanPrefix)
			dst := filepath.Join(localDir, rel)
			_ = os.MkdirAll(filepath.Dir(dst), 0755)
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

func (s *MockS3Client) ListObjects(_ context.Context, prefix string) ([]publisher.S3ObjectInfo, error) {
	var res []publisher.S3ObjectInfo
	for k := range s.FilesOnS3 {
		if strings.HasPrefix(k, prefix) {
			res = append(res, publisher.S3ObjectInfo{
				Key:          k,
				LastModified: time.Now().Add(-48 * time.Hour),
			})
		}
	}
	return res, nil
}

func (s *MockS3Client) DeleteObject(_ context.Context, s3Key string) error {
	delete(s.FilesOnS3, s3Key)
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
	Dirs    []string
}

func (m *MockCreateRepoExecutor) Run(_ context.Context, opts publisher.CreateRepoOptions) error {
	m.Invoked = true
	m.Dirs = append(m.Dirs, opts.Dir)
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

func TestPublisherSubRepoPartitioning(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cr := &MockCreateRepoExecutor{}
	s3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(cr, s3, lockMgr, tmpDir)

	// Create dummy RPM files for standard, debug, and source sub-repositories
	artDir := filepath.Join(tmpDir, "artifacts")
	if err := os.MkdirAll(artDir, 0755); err != nil {
		t.Fatalf("failed to create artifacts dir: %v", err)
	}

	stdRPM := filepath.Join(artDir, "ripgrep-14.1.0-1.x86_64.rpm")
	dbgInfoRPM := filepath.Join(artDir, "ripgrep-debuginfo-14.1.0-1.x86_64.rpm")
	dbgSrcRPM := filepath.Join(artDir, "ripgrep-debugsource-14.1.0-1.x86_64.rpm")
	srcRPM := filepath.Join(artDir, "ripgrep-14.1.0-1.src.rpm")

	for _, f := range []string{stdRPM, dbgInfoRPM, dbgSrcRPM, srcRPM} {
		if err := os.WriteFile(f, []byte("fake rpm payload for "+filepath.Base(f)), 0644); err != nil {
			t.Fatalf("failed to create dummy rpm: %v", err)
		}
	}

	req := publisher.PublishRequest{
		RequestID:    "pub-subrepo-01",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
		NewRPMPaths:  []string{stdRPM, dbgInfoRPM, dbgSrcRPM, srcRPM},
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error processing partitioned publish request: %v", err)
	}

	if len(cr.Dirs) != 3 {
		t.Errorf("expected createrepo_c to be invoked 3 times (for standard, debug, src), got %d (%v)", len(cr.Dirs), cr.Dirs)
	}

	// 1. Verify standard runtime sub-repository
	stdRes, ok := res.SubRepositories["standard"]
	if !ok {
		t.Fatalf("expected 'standard' sub-repository in result")
	}
	expectedStdPrefix := "repos/openSUSE:Factory/standard/x86_64/"
	if stdRes.S3Prefix != expectedStdPrefix {
		t.Errorf("expected standard S3 prefix %q, got %q", expectedStdPrefix, stdRes.S3Prefix)
	}
	if stdRes.RepomdXMLKey != expectedStdPrefix+"repodata/repomd.xml" {
		t.Errorf("unexpected standard repomd key: %s", stdRes.RepomdXMLKey)
	}

	stdWorkDir := filepath.Join(tmpDir, "openSUSE:Factory", "standard", "x86_64")
	if _, err := os.Stat(filepath.Join(stdWorkDir, "ripgrep-14.1.0-1.x86_64.rpm")); err != nil {
		t.Errorf("expected standard RPM in standard work dir")
	}
	if _, err := os.Stat(filepath.Join(stdWorkDir, "ripgrep-debuginfo-14.1.0-1.x86_64.rpm")); err == nil {
		t.Errorf("debuginfo RPM should NOT be present in standard work dir")
	}
	if _, err := os.Stat(filepath.Join(stdWorkDir, "ripgrep-14.1.0-1.src.rpm")); err == nil {
		t.Errorf("source RPM should NOT be present in standard work dir")
	}

	// 2. Verify debug sub-repository
	dbgRes, ok := res.SubRepositories["debug"]
	if !ok {
		t.Fatalf("expected 'debug' sub-repository in result")
	}
	expectedDbgPrefix := "repos/openSUSE:Factory/standard/debug/"
	if dbgRes.S3Prefix != expectedDbgPrefix {
		t.Errorf("expected debug S3 prefix %q, got %q", expectedDbgPrefix, dbgRes.S3Prefix)
	}
	if dbgRes.RepomdXMLKey != expectedDbgPrefix+"repodata/repomd.xml" {
		t.Errorf("unexpected debug repomd key: %s", dbgRes.RepomdXMLKey)
	}

	dbgWorkDir := filepath.Join(tmpDir, "openSUSE:Factory", "standard", "debug")
	if _, err := os.Stat(filepath.Join(dbgWorkDir, "ripgrep-debuginfo-14.1.0-1.x86_64.rpm")); err != nil {
		t.Errorf("expected debuginfo RPM in debug work dir")
	}
	if _, err := os.Stat(filepath.Join(dbgWorkDir, "ripgrep-debugsource-14.1.0-1.x86_64.rpm")); err != nil {
		t.Errorf("expected debugsource RPM in debug work dir")
	}
	if _, err := os.Stat(filepath.Join(dbgWorkDir, "ripgrep-14.1.0-1.x86_64.rpm")); err == nil {
		t.Errorf("standard RPM should NOT be present in debug work dir")
	}

	// 3. Verify source sub-repository
	srcRes, ok := res.SubRepositories["src"]
	if !ok {
		t.Fatalf("expected 'src' sub-repository in result")
	}
	expectedSrcPrefix := "repos/openSUSE:Factory/standard/src/"
	if srcRes.S3Prefix != expectedSrcPrefix {
		t.Errorf("expected source S3 prefix %q, got %q", expectedSrcPrefix, srcRes.S3Prefix)
	}
	if srcRes.RepomdXMLKey != expectedSrcPrefix+"repodata/repomd.xml" {
		t.Errorf("unexpected source repomd key: %s", srcRes.RepomdXMLKey)
	}

	srcWorkDir := filepath.Join(tmpDir, "openSUSE:Factory", "standard", "src")
	if _, err := os.Stat(filepath.Join(srcWorkDir, "ripgrep-14.1.0-1.src.rpm")); err != nil {
		t.Errorf("expected source RPM in src work dir")
	}
	if _, err := os.Stat(filepath.Join(srcWorkDir, "ripgrep-14.1.0-1.x86_64.rpm")); err == nil {
		t.Errorf("standard RPM should NOT be present in src work dir")
	}
}

func TestPublisherExplicitSubRepoFilter(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cr := &MockCreateRepoExecutor{}
	s3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()
	pub := publisher.NewPublisher(cr, s3, lockMgr, tmpDir)

	artDir := filepath.Join(tmpDir, "artifacts")
	_ = os.MkdirAll(artDir, 0755)
	dbgRPM := filepath.Join(artDir, "foo-debuginfo-1.0.rpm")
	_ = os.WriteFile(dbgRPM, []byte("dbg"), 0644)

	req := publisher.PublishRequest{
		RequestID:    "pub-explicit-01",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
		SubRepos:     []string{"debug"},
		NewRPMPaths:  []string{dbgRPM},
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.Dirs) != 1 {
		t.Errorf("expected createrepo_c to be invoked exactly once for debug sub-repository, got %d", len(cr.Dirs))
	}

	if _, ok := res.SubRepositories["debug"]; !ok {
		t.Errorf("expected debug sub-repository in results")
	}
	if _, ok := res.SubRepositories["standard"]; ok {
		t.Errorf("standard sub-repository should NOT be present when only debug was requested")
	}
	if _, ok := res.SubRepositories["src"]; ok {
		t.Errorf("src sub-repository should NOT be present when only debug was requested")
	}
}
