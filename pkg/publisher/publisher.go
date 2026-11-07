package publisher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func isValidPathSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "..") {
		return false
	}
	return true
}

type PublishRequest struct {
	RequestID    string   `json:"request_id"`
	Project      string   `json:"project"`
	Target       string   `json:"target"`
	Architecture string   `json:"architecture"`
	NewRPMPaths  []string `json:"new_rpm_paths"`
}

type PublishResult struct {
	Project         string   `json:"project"`
	Target          string   `json:"target"`
	Architecture    string   `json:"architecture"`
	RepomdXMLKey    string   `json:"repomd_xml_key"`
	UploadedXMLKeys []string `json:"uploaded_xml_keys"`
}

type S3Client interface {
	DownloadMetadata(ctx context.Context, prefix, localDir string) error
	UploadFile(ctx context.Context, localPath, s3Key string) error
}

type LockManager interface {
	AcquireLock(ctx context.Context, key string) (func(), error)
}

type LocalLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewLocalLockManager() *LocalLockManager {
	return &LocalLockManager{
		locks: make(map[string]*sync.Mutex),
	}
}

func (m *LocalLockManager) AcquireLock(_ context.Context, key string) (func(), error) {
	m.mu.Lock()
	l, exists := m.locks[key]
	if !exists {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	m.mu.Unlock()

	l.Lock()
	return func() { l.Unlock() }, nil
}

type Publisher struct {
	crExecutor CreateRepoExecutor
	s3Client   S3Client
	lockMgr    LockManager
	workDir    string
}

func NewPublisher(cr CreateRepoExecutor, s3 S3Client, lockMgr LockManager, workDir string) *Publisher {
	return &Publisher{
		crExecutor: cr,
		s3Client:   s3,
		lockMgr:    lockMgr,
		workDir:    workDir,
	}
}

func (p *Publisher) ProcessPublishRequest(ctx context.Context, req PublishRequest) (*PublishResult, error) {
	if !isValidPathSegment(req.Project) || !isValidPathSegment(req.Target) || !isValidPathSegment(req.Architecture) {
		return nil, fmt.Errorf("invalid publish request: path traversal detected in project, target, or architecture")
	}

	lockKey := fmt.Sprintf("%s:%s:%s", req.Project, req.Target, req.Architecture)
	unlock, err := p.lockMgr.AcquireLock(ctx, lockKey)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire repo lock: %w", err)
	}
	defer unlock()

	repoDir := filepath.Join(p.workDir, req.Project, req.Target, req.Architecture)
	oldMetaDir := filepath.Join(repoDir, "repodata")
	_ = os.MkdirAll(repoDir, 0750)

	s3Prefix := fmt.Sprintf("repos/%s/%s/%s/", req.Project, req.Target, req.Architecture)
	_ = p.s3Client.DownloadMetadata(ctx, s3Prefix+"repodata", oldMetaDir)

	hasOldMeta := checkFileExists(filepath.Join(oldMetaDir, "repomd.xml"))

	opts := CreateRepoOptions{
		Dir:            repoDir,
		Update:         hasOldMeta,
		RecyclePkgList: hasOldMeta,
	}
	if hasOldMeta {
		opts.OldMetadataDir = oldMetaDir
	}

	if err := p.crExecutor.Run(ctx, opts); err != nil {
		return nil, fmt.Errorf("metadata generation failed: %w", err)
	}

	return p.uploadMetadataAtomic(ctx, repoDir, s3Prefix, req)
}

func (p *Publisher) uploadMetadataAtomic(ctx context.Context, repoDir, s3Prefix string, req PublishRequest) (*PublishResult, error) {
	repodataDir := filepath.Join(repoDir, "repodata")
	entries, err := os.ReadDir(repodataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read generated repodata dir: %w", err)
	}

	var hashPrefixedFiles []string
	var repomdFile string

	for _, e := range entries {
		if e.Name() == "repomd.xml" {
			repomdFile = e.Name()
		} else {
			hashPrefixedFiles = append(hashPrefixedFiles, e.Name())
		}
	}

	var uploadedKeys []string
	for _, f := range hashPrefixedFiles {
		localPath := filepath.Join(repodataDir, f)
		s3Key := fmt.Sprintf("%srepodata/%s", s3Prefix, f)
		if err := p.s3Client.UploadFile(ctx, localPath, s3Key); err != nil {
			return nil, fmt.Errorf("failed to upload metadata file %s: %w", f, err)
		}
		uploadedKeys = append(uploadedKeys, s3Key)
	}

	repomdKey := s3Prefix + "repodata/repomd.xml"
	if repomdFile != "" {
		localPath := filepath.Join(repodataDir, repomdFile)
		if err := p.s3Client.UploadFile(ctx, localPath, repomdKey); err != nil {
			return nil, fmt.Errorf("failed to upload atomic repomd.xml: %w", err)
		}
	}

	return &PublishResult{
		Project:         req.Project,
		Target:          req.Target,
		Architecture:    req.Architecture,
		RepomdXMLKey:    repomdKey,
		UploadedXMLKeys: uploadedKeys,
	}, nil
}

func checkFileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
