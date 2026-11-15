package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

func isValidPathSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "..") {
		return false
	}
	return true
}

type PublishRequest = v1.PublishRequest

type SubRepoResult struct {
	SubRepo         string   `json:"sub_repo"`
	S3Prefix        string   `json:"s3_prefix"`
	RepomdXMLKey    string   `json:"repomd_xml_key,omitempty"`
	UploadedXMLKeys []string `json:"uploaded_xml_keys"`
}

type PublishResult struct {
	Project         string                   `json:"project"`
	Target          string                   `json:"target"`
	Architecture    string                   `json:"architecture"`
	RepomdXMLKey    string                   `json:"repomd_xml_key,omitempty"`
	IndexYAMLKey    string                   `json:"index_yaml_key,omitempty"`
	UploadedXMLKeys []string                 `json:"uploaded_xml_keys"`
	SubRepositories map[string]SubRepoResult `json:"sub_repositories,omitempty"`
}

// S3ObjectInfo represents metadata for an S3 object.
type S3ObjectInfo = s3.S3ObjectInfo

type S3Client interface {
	DownloadMetadata(ctx context.Context, prefix, localDir string) error
	UploadFile(ctx context.Context, localPath, s3Key string) error
	DownloadFile(ctx context.Context, s3Key, localPath string) error
	ListObjects(ctx context.Context, prefix string) ([]S3ObjectInfo, error)
	DeleteObject(ctx context.Context, s3Key string) error
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
	mrExecutor   ModifyRepoExecutor
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
		mrExecutor: NewRealModifyRepoExecutor(),
		s3Client:   s3,
		lockMgr:    lockMgr,
		workDir:    workDir,
	}
}

func (p *Publisher) SetStore(s storage.Store) {
	p.store = s
}

func (p *Publisher) SetModifyRepoExecutor(mr ModifyRepoExecutor) {
	p.mrExecutor = mr
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

	// 2. RepoType: "rpm-md" generates RPM metadata via createrepo_c across partitioned sub-repositories
	if hasRepoType(repoTypes, "rpm-md") {
		rpmRes, err := p.processRPMSubRepos(ctx, req, repoDir)
		if err != nil {
			return nil, err
		}
		res.RepomdXMLKey = rpmRes.RepomdXMLKey
		res.UploadedXMLKeys = append(res.UploadedXMLKeys, rpmRes.UploadedXMLKeys...)
		res.SubRepositories = rpmRes.SubRepositories
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

func (p *Publisher) processRPMSubRepos(ctx context.Context, req PublishRequest, repoDir string) (*PublishResult, error) {
	classified := p.partitionRPMs(req, repoDir)
	activeSubRepos := p.determineActiveSubRepos(req, classified, repoDir)

	res := &PublishResult{
		Project:         req.Project,
		Target:          req.Target,
		Architecture:    req.Architecture,
		SubRepositories: make(map[string]SubRepoResult),
	}

	for _, sr := range activeSubRepos {
		srRes, err := p.processSingleSubRepo(ctx, req, sr, classified[sr])
		if err != nil {
			return nil, err
		}
		res.SubRepositories[string(sr)] = *srRes
		res.UploadedXMLKeys = append(res.UploadedXMLKeys, srRes.UploadedXMLKeys...)
		if sr == SubRepoStandard || res.RepomdXMLKey == "" {
			res.RepomdXMLKey = srRes.RepomdXMLKey
		}
	}

	if err := p.updateTargetSolvCache(ctx, req); err != nil {
		return nil, err
	}

	return res, nil
}

func (p *Publisher) partitionRPMs(req PublishRequest, repoDir string) map[SubRepoType][]string {
	classified := make(map[SubRepoType][]string)
	for _, path := range req.NewRPMPaths {
		sr := ClassifyRPM(path)
		classified[sr] = append(classified[sr], path)
	}
	for _, key := range req.ArtifactKeys {
		sr := ClassifyRPM(key)
		classified[sr] = append(classified[sr], key)
	}

	// Check files already in repoDir that belong to debug or src sub-repositories
	if matches, err := filepath.Glob(filepath.Join(repoDir, "*.rpm")); err == nil {
		for _, m := range matches {
			sr := ClassifyRPM(m)
			if sr != SubRepoStandard {
				classified[sr] = append(classified[sr], m)
			}
		}
	}
	return classified
}

func (p *Publisher) determineActiveSubRepos(req PublishRequest, classified map[SubRepoType][]string, repoDir string) []SubRepoType {
	if len(req.SubRepos) > 0 {
		return parseRequestedSubRepos(req.SubRepos)
	}

	active := []SubRepoType{SubRepoStandard}
	if len(classified[SubRepoDebug]) > 0 || checkSubRepoHasRPMs(p.workDir, req.Project, req.Target, req.Architecture, SubRepoDebug) {
		active = append(active, SubRepoDebug)
	}
	if len(classified[SubRepoSource]) > 0 || checkSubRepoHasRPMs(p.workDir, req.Project, req.Target, req.Architecture, SubRepoSource) {
		active = append(active, SubRepoSource)
	}
	return active
}

func parseRequestedSubRepos(subRepos []string) []SubRepoType {
	var res []SubRepoType
	for _, s := range subRepos {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "debug":
			res = append(res, SubRepoDebug)
		case "src", "source":
			res = append(res, SubRepoSource)
		case "standard", "runtime":
			res = append(res, SubRepoStandard)
		}
	}
	if len(res) == 0 {
		return []SubRepoType{SubRepoStandard}
	}
	return res
}

func checkSubRepoHasRPMs(baseWorkDir, project, target, arch string, sr SubRepoType) bool {
	dir := SubRepoWorkDir(baseWorkDir, project, target, arch, sr)
	matches, err := filepath.Glob(filepath.Join(dir, "*.rpm"))
	return err == nil && len(matches) > 0
}

func (p *Publisher) processSingleSubRepo(ctx context.Context, req PublishRequest, sr SubRepoType, rpms []string) (*SubRepoResult, error) {
	subRepoDir := SubRepoWorkDir(p.workDir, req.Project, req.Target, req.Architecture, sr)
	s3Prefix := SubRepoS3Prefix(req.Project, req.Target, req.Architecture, sr)
	_ = os.MkdirAll(subRepoDir, 0750)

	stageRPMFiles(subRepoDir, rpms)

	if p.s3Client != nil {
		for _, item := range rpms {
			filename := filepath.Base(item)
			dest := filepath.Join(subRepoDir, filename)
			if !checkFileExists(dest) {
				_ = p.s3Client.DownloadFile(ctx, item, dest)
			}
		}
	}

	if len(req.DeletedRPMs) > 0 {
		p.purgeDeletedRPMs(ctx, subRepoDir, s3Prefix, req.DeletedRPMs)
	}

	oldMetaDir := filepath.Join(subRepoDir, "repodata")
	_ = p.s3Client.DownloadMetadata(ctx, s3Prefix+"repodata", oldMetaDir)
	hasOldMeta := checkFileExists(filepath.Join(oldMetaDir, "repomd.xml"))

	opts := CreateRepoOptions{
		Dir:            subRepoDir,
		Update:         hasOldMeta,
		RecyclePkgList: false,
	}
	if hasOldMeta {
		opts.OldMetadataDir = oldMetaDir
	}

	if err := p.crExecutor.Run(ctx, opts); err != nil {
		return nil, fmt.Errorf("metadata generation failed for sub-repository %s: %w", sr, err)
	}

	if sr == SubRepoStandard {
		metaDir := filepath.Join(subRepoDir, "repodata")
		if err := EnrichMetadata(ctx, p.mrExecutor, metaDir, req, p.workDir); err != nil {
			return nil, fmt.Errorf("metadata enrichment failed: %w", err)
		}
	}

	if matches, err := filepath.Glob(filepath.Join(subRepoDir, "*.rpm")); err == nil {
		for _, m := range matches {
			rpmBase := filepath.Base(m)
			_ = p.s3Client.UploadFile(ctx, m, s3Prefix+rpmBase)
		}
	}

	upRes, err := p.uploadMetadataAtomic(ctx, subRepoDir, s3Prefix, req)
	if err != nil {
		return nil, err
	}

	return &SubRepoResult{
		SubRepo:         string(sr),
		S3Prefix:        s3Prefix,
		RepomdXMLKey:    upRes.RepomdXMLKey,
		UploadedXMLKeys: upRes.UploadedXMLKeys,
	}, nil
}

func stageRPMFiles(targetDir string, rpmPaths []string) {
	for _, p := range rpmPaths {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		dest := filepath.Join(targetDir, filepath.Base(p))
		if dest != p {
			copyFile(p, dest)
			if filepath.Dir(p) != targetDir {
				_ = os.Remove(p)
			}
		}
	}
}

func copyFile(src, dst string) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return
	}
	defer in.Close()

	out, err := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	defer out.Close()

	// #nosec G110 -- copying RPM file with bounded size in repository staging
	_, _ = io.Copy(out, in)
}

func (p *Publisher) purgeDeletedRPMs(ctx context.Context, subRepoDir, s3Prefix string, deletedRPMs []string) {
	for _, rpm := range deletedRPMs {
		base := filepath.Base(rpm)
		localPath := filepath.Join(subRepoDir, base)
		if checkFileExists(localPath) {
			_ = os.Remove(localPath)
		}
		if p.s3Client != nil {
			_ = p.s3Client.DeleteObject(ctx, s3Prefix+base)
		}
	}
}

// updateTargetSolvCache rebuilds the target's solv blob from the CURRENT set
// of per-package bininfo.json objects under the target's artifact prefix. Each
// package directory holds exactly one bininfo.json, so a new build of a
// package replaces the binary set of its previous build.
func (p *Publisher) updateTargetSolvCache(ctx context.Context, req PublishRequest) error {
	if p.store == nil {
		return nil
	}
	infos, err := p.collectBinInfos(ctx, req)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		return nil
	}

	repoName := fmt.Sprintf("%s:%s", req.Project, req.Target)
	newBlob, err := solv.BuildSolv(req.Architecture, infos)
	if err != nil {
		return fmt.Errorf("failed generating solv cache for %s: %w", repoName, err)
	}

	solvRecord := &storage.TargetSolv{
		ProjectName: req.Project,
		TargetName:  req.Target,
		Arch:        req.Architecture,
		SolvBlob:    newBlob,
		ContentHash: fmt.Sprintf("%x", sha256.Sum256(newBlob)),
	}
	return p.store.SaveTargetSolv(ctx, solvRecord)
}

// collectBinInfos reads every package's bininfo.json below
// artifacts/<project>/<target>/<arch>/ and returns the infos of the standard
// sub-repository that are not deleted by req.
func (p *Publisher) collectBinInfos(ctx context.Context, req PublishRequest) ([]bininfo.BinInfo, error) {
	prefix := fmt.Sprintf("artifacts/%s/%s/%s/", req.Project, req.Target, req.Architecture)
	objs, err := p.s3Client.ListObjects(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("listing bininfo under %s: %w", prefix, err)
	}
	deleted := make(map[string]bool)
	for _, del := range req.DeletedRPMs {
		deleted[filepath.Base(del)] = true
	}
	if err := os.MkdirAll(p.workDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating work dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(p.workDir, "bininfo-")
	if err != nil {
		return nil, fmt.Errorf("creating bininfo scratch dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var infos []bininfo.BinInfo
	for i, obj := range objs {
		rel := strings.TrimPrefix(obj.Key, prefix)
		if path.Base(rel) != bininfo.FileName || strings.Count(rel, "/") != 1 {
			continue
		}
		list, err := p.fetchBinInfo(ctx, obj.Key, filepath.Join(tmpDir, fmt.Sprintf("%d.json", i)))
		if err != nil {
			return nil, err
		}
		for _, info := range list {
			if !deleted[info.Filename] && ClassifyRPM(info.Filename) == SubRepoStandard {
				infos = append(infos, info)
			}
		}
	}
	return infos, nil
}

func (p *Publisher) fetchBinInfo(ctx context.Context, key, local string) ([]bininfo.BinInfo, error) {
	if err := p.s3Client.DownloadFile(ctx, key, local); err != nil {
		return nil, fmt.Errorf("downloading %s: %w", key, err)
	}
	// #nosec G304 -- local is a file created by DownloadFile in our scratch dir
	data, err := os.ReadFile(local)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	list, err := bininfo.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return list, nil
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
