package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func isValidPathSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "..") {
		return false
	}
	return true
}

type PublishRequest = v1.PublishRequest

type PublishResult struct {
	Project         string   `json:"project"`
	Target          string   `json:"target"`
	Architecture    string   `json:"architecture"`
	RepomdXMLKey    string   `json:"repomd_xml_key,omitempty"`
	IndexYAMLKey    string   `json:"index_yaml_key,omitempty"`
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
	crExecutor   CreateRepoExecutor
	helmExecutor HelmIndexExecutor
	s3Client     S3Client
	lockMgr      LockManager
	workDir      string
	eventPub     EventPublisher
	signer       MetadataSigner
	store        storage.Store
}

func NewPublisher(cr CreateRepoExecutor, s3 S3Client, lockMgr LockManager, workDir string) *Publisher {
	return &Publisher{
		crExecutor: cr,
		s3Client:   s3,
		lockMgr:    lockMgr,
		workDir:    workDir,
	}
}

func (p *Publisher) SetStore(s storage.Store) {
	p.store = s
}

func (p *Publisher) SetEventPublisher(ep EventPublisher) {
	p.eventPub = ep
}

func (p *Publisher) SetMetadataSigner(ms MetadataSigner) {
	p.signer = ms
}

func (p *Publisher) SetHelmExecutor(he HelmIndexExecutor) {
	p.helmExecutor = he
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

	repoTypes := req.RepoTypes
	if len(repoTypes) == 0 {
		repoTypes = []string{"rpm-md"}
	}

	res := &PublishResult{
		Project:      req.Project,
		Target:       req.Target,
		Architecture: req.Architecture,
	}

	// 1. RepoType: "none" suppresses repodata generation completely (ISOs, appliance images, raw binaries)
	if hasRepoType(repoTypes, "none") {
		p.emitPublicationEvent(ctx, req, "")
		return res, nil
	}

	repoDir := filepath.Join(p.workDir, req.Project, req.Target, req.Architecture)
	_ = os.MkdirAll(repoDir, 0750)
	s3Prefix := fmt.Sprintf("repos/%s/%s/%s/", req.Project, req.Target, req.Architecture)

	// 2. RepoType: "rpm-md" generates RPM metadata via createrepo_c
	if hasRepoType(repoTypes, "rpm-md") {
		rpmRes, err := p.processRPMRepo(ctx, req, repoDir, s3Prefix)
		if err != nil {
			return nil, err
		}
		res.RepomdXMLKey = rpmRes.RepomdXMLKey
		res.UploadedXMLKeys = append(res.UploadedXMLKeys, rpmRes.UploadedXMLKeys...)
	}

	// 3. RepoType: "helm" generates Helm chart repository index.yaml
	if hasRepoType(repoTypes, "helm") {
		helmKey, err := p.processHelmRepo(ctx, repoDir, s3Prefix)
		if err != nil {
			return nil, err
		}
		res.IndexYAMLKey = helmKey
		res.UploadedXMLKeys = append(res.UploadedXMLKeys, helmKey)
	}

	eventKey := res.RepomdXMLKey
	if eventKey == "" {
		eventKey = res.IndexYAMLKey
	}
	p.emitPublicationEvent(ctx, req, eventKey)

	return res, nil
}

func (p *Publisher) processRPMRepo(ctx context.Context, req PublishRequest, repoDir, s3Prefix string) (*PublishResult, error) {
	oldMetaDir := filepath.Join(repoDir, "repodata")
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

	_ = p.updateTargetSolvCache(ctx, req, repoDir)
	return res, nil
}

func (p *Publisher) updateTargetSolvCache(ctx context.Context, req PublishRequest, repoDir string) error {
	if p.store == nil {
		return nil
	}

	rpmMap := make(map[string]bool)
	for _, path := range req.NewRPMPaths {
		rpmMap[filepath.Base(path)] = true
	}
	if matches, err := filepath.Glob(filepath.Join(repoDir, "*.rpm")); err == nil {
		for _, m := range matches {
			rpmMap[filepath.Base(m)] = true
		}
	}

	var specs []solv.PackageSpec
	for rpm := range rpmMap {
		specs = append(specs, solv.ParseRPMFilename(rpm))
	}
	if len(specs) == 0 {
		return nil
	}

	var existingBlob []byte
	existing, err := p.store.GetTargetSolv(ctx, req.Project, req.Target, req.Architecture)
	if err == nil && existing != nil {
		existingBlob = existing.SolvBlob
	}

	repoName := fmt.Sprintf("%s:%s", req.Project, req.Target)
	newBlob, err := solv.MergeAndDumpSolv(existingBlob, req.Architecture, repoName, specs)
	if err != nil {
		return fmt.Errorf("failed generating solv cache for %s: %w", repoName, err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(newBlob))
	solvRecord := &storage.TargetSolv{
		ProjectName: req.Project,
		TargetName:  req.Target,
		Arch:        req.Architecture,
		SolvBlob:    newBlob,
		ContentHash: hash,
	}

	return p.store.SaveTargetSolv(ctx, solvRecord)
}

func (p *Publisher) processHelmRepo(ctx context.Context, repoDir, s3Prefix string) (string, error) {
	executor := p.helmExecutor
	if executor == nil {
		executor = NewRealHelmIndexExecutor()
	}

	opts := HelmIndexOptions{
		Dir: repoDir,
	}
	if err := executor.Run(ctx, opts); err != nil {
		return "", fmt.Errorf("helm indexing failed: %w", err)
	}

	indexLocal := filepath.Join(repoDir, "index.yaml")
	indexKey := s3Prefix + "index.yaml"
	if checkFileExists(indexLocal) {
		if err := p.s3Client.UploadFile(ctx, indexLocal, indexKey); err != nil {
			return "", fmt.Errorf("failed to upload helm index.yaml: %w", err)
		}
	}
	return indexKey, nil
}

func (p *Publisher) emitPublicationEvent(ctx context.Context, req PublishRequest, primaryKey string) {
	if p.eventPub == nil {
		return
	}
	event := v1.NewPublicationEvent(req.Project, req.Target, req.Architecture, req.Package, req.NewRPMPaths, primaryKey)
	if data, err := json.Marshal(event); err == nil {
		_ = p.eventPub.PublishEvent(ctx, natsqueue.SubjectEventsPublished, data)
	}
}

func hasRepoType(types []string, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	for _, t := range types {
		if strings.ToLower(strings.TrimSpace(t)) == expected {
			return true
		}
	}
	return false
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
