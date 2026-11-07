package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
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
	Package      string   `json:"package,omitempty"`
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

type EventPublisher interface {
	PublishEvent(ctx context.Context, subject string, data []byte) error
}

type SignOptions struct {
	Project    string
	Package    string
	SignFlavor string
	SignType   string
	HashAlgo   string
	KeyFile    string
}

type MetadataSigner interface {
	SignRepositoryMetadata(ctx context.Context, repodataDir string, opts SignOptions) (string, string, error)
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
	eventPub   EventPublisher
	signer     MetadataSigner
}

func NewPublisher(cr CreateRepoExecutor, s3 S3Client, lockMgr LockManager, workDir string) *Publisher {
	return &Publisher{
		crExecutor: cr,
		s3Client:   s3,
		lockMgr:    lockMgr,
		workDir:    workDir,
	}
}

func (p *Publisher) SetEventPublisher(ep EventPublisher) {
	p.eventPub = ep
}

func (p *Publisher) SetMetadataSigner(ms MetadataSigner) {
	p.signer = ms
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

	res, err := p.uploadMetadataAtomic(ctx, repoDir, s3Prefix, req)
	if err != nil {
		return nil, err
	}

	if p.eventPub != nil {
		event := v1.NewPublicationEvent(req.Project, req.Target, req.Architecture, req.Package, req.NewRPMPaths, res.RepomdXMLKey)
		if data, err := json.Marshal(event); err == nil {
			_ = p.eventPub.PublishEvent(ctx, natsqueue.SubjectEventsPublished, data)
		}
	}

	return res, nil
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

		if p.signer != nil {
			sigPath, keyPath, err := p.signer.SignRepositoryMetadata(ctx, repodataDir, SignOptions{Project: req.Project})
			if err == nil {
				_ = p.s3Client.UploadFile(ctx, sigPath, s3Prefix+"repodata/repomd.xml.asc")
				_ = p.s3Client.UploadFile(ctx, keyPath, s3Prefix+"repodata/repomd.xml.key")
				uploadedKeys = append(uploadedKeys, s3Prefix+"repodata/repomd.xml.asc", s3Prefix+"repodata/repomd.xml.key")
			}
		}

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

// ValidateProvenance verifies that an in-toto SLSA provenance statement is valid
// and that its declared artifact SHA-256 digests match the built RPMs.
func ValidateProvenance(provenanceData []byte, expectedRPMs map[string]string) (*v1.InTotoStatement, error) {
	var stmt v1.InTotoStatement
	if err := json.Unmarshal(provenanceData, &stmt); err != nil {
		return nil, fmt.Errorf("invalid provenance json: %w", err)
	}

	if stmt.Type != v1.InTotoStatementType {
		return nil, fmt.Errorf("invalid in-toto statement type: %q, expected %q", stmt.Type, v1.InTotoStatementType)
	}
	if stmt.PredicateType != v1.SLSAProvenanceType {
		return nil, fmt.Errorf("invalid slsa predicate type: %q, expected %q", stmt.PredicateType, v1.SLSAProvenanceType)
	}

	for _, sub := range stmt.Subject {
		expectedDigest, exists := expectedRPMs[sub.Name]
		if exists && expectedDigest != "" {
			actualDigest := sub.Digest["sha256"]
			if !strings.EqualFold(actualDigest, expectedDigest) {
				return nil, fmt.Errorf("provenance digest mismatch for %s: expected %s, got %s", sub.Name, expectedDigest, actualDigest)
			}
		}
	}

	return &stmt, nil
}

func checkFileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
