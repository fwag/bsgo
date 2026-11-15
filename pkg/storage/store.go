package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource already exists")
)

type ProjectSigningKey struct {
	ProjectName         string `json:"project_name"`
	KeyFlavor           string `json:"key_flavor"`
	KeyType             string `json:"key_type"`
	PublicKeyArmored    string `json:"public_key_armored"`
	EncryptedPrivateKey string `json:"encrypted_private_key"`
	Fingerprint         string `json:"fingerprint"`
}

type TargetSolv struct {
	ProjectName string    `json:"project_name"`
	TargetName  string    `json:"target_name"`
	Arch        string    `json:"arch"`
	SolvBlob    []byte    `json:"solv_blob"`
	ContentHash string    `json:"content_hash"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DoDRepo points at the solv blob ingested from a remote rpm-md repository.
// RepomdChecksum is the sha256 of the repomd.xml the blob was built from;
// SolvKey is the S3 object holding the blob.
type DoDRepo struct {
	URL            string    `json:"url"`
	Name           string    `json:"name"`
	RepomdChecksum string    `json:"repomd_checksum"`
	SolvKey        string    `json:"solv_key"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Build states persisted in package_build_state (OBS names).
const (
	StateScheduled    = "scheduled"
	StateBuilding     = "building"
	StateSucceeded    = "succeeded"
	StateFailed       = "failed"
	StateUnresolvable = "unresolvable"
	StateBlocked      = "blocked"
	StateBroken       = "broken"
	StateExcluded     = "excluded"
	StateDisabled     = "disabled"
)

// ValidBuildStates lists every state accepted by the package_build_state table.
var ValidBuildStates = []string{
	StateScheduled, StateBuilding, StateSucceeded, StateFailed, StateUnresolvable,
	StateBlocked, StateBroken, StateExcluded, StateDisabled,
}

// IsValidBuildState reports whether state is a known build state.
func IsValidBuildState(state string) bool {
	for _, s := range ValidBuildStates {
		if s == state {
			return true
		}
	}
	return false
}

// PackageBuildState is the build state of one package/flavor in one (project, repo, arch).
type PackageBuildState struct {
	Project      string    `json:"project"`
	Repo         string    `json:"repo"`
	Arch         string    `json:"arch"`
	Package      string    `json:"package"`
	Flavor       string    `json:"flavor,omitempty"`
	State        string    `json:"state"`
	Details      string    `json:"details,omitempty"`
	SourceCommit string    `json:"source_commit,omitempty"`
	Meta         string    `json:"meta,omitempty"`
	Bcnt         int       `json:"bcnt,omitempty"`
	JobID        string    `json:"job_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// BuildStateFilter narrows ListBuildStates; empty fields match everything.
type BuildStateFilter struct {
	Repo  string
	Arch  string
	State string
}

func (f BuildStateFilter) Matches(st *PackageBuildState) bool {
	return (f.Repo == "" || f.Repo == st.Repo) &&
		(f.Arch == "" || f.Arch == st.Arch) &&
		(f.State == "" || f.State == st.State)
}

type Store interface {
	CreateProject(ctx context.Context, prj *manifest.ProjectManifest) error
	UpsertProject(ctx context.Context, prj *manifest.ProjectManifest) error
	GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error)
	ListProjects(ctx context.Context) ([]*manifest.ProjectManifest, error)

	UpsertPackage(ctx context.Context, projectName string, pkg *manifest.PackageManifest) error
	GetPackage(ctx context.Context, projectName, packageName string) (*manifest.PackageManifest, error)
	ListPackages(ctx context.Context, projectName string) ([]*manifest.PackageManifest, error)

	SaveProjectKey(ctx context.Context, key *ProjectSigningKey) error
	GetProjectKey(ctx context.Context, projectName, flavor, keyType string) (*ProjectSigningKey, error)
	ListProjectKeys(ctx context.Context, projectName string) ([]*ProjectSigningKey, error)

	SaveTargetSolv(ctx context.Context, solv *TargetSolv) error
	GetTargetSolv(ctx context.Context, projectName, targetName, arch string) (*TargetSolv, error)
	ListTargetSolvs(ctx context.Context, projectName string) ([]*TargetSolv, error)

	SaveDoDRepo(ctx context.Context, repo *DoDRepo) error
	GetDoDRepo(ctx context.Context, url string) (*DoDRepo, error)

	UpsertBuildState(ctx context.Context, st *PackageBuildState) error
	ListBuildStates(ctx context.Context, project string, filter BuildStateFilter) ([]*PackageBuildState, error)
	CountBuildStates(ctx context.Context, project string, filter BuildStateFilter) (map[string]int, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	projects map[string]*manifest.ProjectManifest
	packages map[string]map[string]*manifest.PackageManifest
	keys     map[string]map[string]*ProjectSigningKey // key: project -> flavor:type -> key
	solvs    map[string]*TargetSolv                   // key: project:target:arch -> solv
	states   map[string]*PackageBuildState            // key: project/repo/arch/package/flavor
	dod      map[string]*DoDRepo                      // key: url
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects: make(map[string]*manifest.ProjectManifest),
		packages: make(map[string]map[string]*manifest.PackageManifest),
		keys:     make(map[string]map[string]*ProjectSigningKey),
		solvs:    make(map[string]*TargetSolv),
		states:   make(map[string]*PackageBuildState),
		dod:      make(map[string]*DoDRepo),
	}
}

func (s *MemoryStore) CreateProject(_ context.Context, prj *manifest.ProjectManifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := prj.Project.Name
	if _, exists := s.projects[name]; exists {
		return ErrConflict
	}
	s.projects[name] = prj
	return nil
}

func (s *MemoryStore) UpsertProject(_ context.Context, prj *manifest.ProjectManifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.projects[prj.Project.Name] = prj
	return nil
}

func (s *MemoryStore) GetProject(_ context.Context, name string) (*manifest.ProjectManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prj, exists := s.projects[name]
	if !exists {
		return nil, ErrNotFound
	}
	return prj, nil
}

func (s *MemoryStore) ListProjects(_ context.Context) ([]*manifest.ProjectManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*manifest.ProjectManifest, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, p)
	}
	return list, nil
}

func (s *MemoryStore) UpsertPackage(_ context.Context, projectName string, pkg *manifest.PackageManifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.packages[projectName]; !exists {
		s.packages[projectName] = make(map[string]*manifest.PackageManifest)
	}
	s.packages[projectName][pkg.Package.Name] = pkg
	return nil
}

func (s *MemoryStore) GetPackage(_ context.Context, projectName, packageName string) (*manifest.PackageManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pkgs, prjExists := s.packages[projectName]
	if !prjExists {
		return nil, ErrNotFound
	}
	pkg, exists := pkgs[packageName]
	if !exists {
		return nil, ErrNotFound
	}
	return pkg, nil
}

func (s *MemoryStore) ListPackages(_ context.Context, projectName string) ([]*manifest.PackageManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pkgs, prjExists := s.packages[projectName]
	if !prjExists {
		return []*manifest.PackageManifest{}, nil
	}

	list := make([]*manifest.PackageManifest, 0, len(pkgs))
	for _, p := range pkgs {
		list = append(list, p)
	}
	return list, nil
}

func (s *MemoryStore) SaveProjectKey(_ context.Context, key *ProjectSigningKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.projects[key.ProjectName]; !exists {
		return ErrNotFound
	}

	if s.keys[key.ProjectName] == nil {
		s.keys[key.ProjectName] = make(map[string]*ProjectSigningKey)
	}

	keyID := fmt.Sprintf("%s:%s", key.KeyFlavor, key.KeyType)
	s.keys[key.ProjectName][keyID] = key
	return nil
}

func (s *MemoryStore) GetProjectKey(_ context.Context, projectName, flavor, keyType string) (*ProjectSigningKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prjKeys, exists := s.keys[projectName]
	if !exists {
		return nil, ErrNotFound
	}

	keyID := fmt.Sprintf("%s:%s", flavor, keyType)
	key, found := prjKeys[keyID]
	if !found {
		return nil, ErrNotFound
	}
	return key, nil
}

func (s *MemoryStore) ListProjectKeys(_ context.Context, projectName string) ([]*ProjectSigningKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prjKeys, exists := s.keys[projectName]
	if !exists {
		return []*ProjectSigningKey{}, nil
	}

	var list []*ProjectSigningKey
	for _, k := range prjKeys {
		list = append(list, k)
	}
	return list, nil
}

func (s *MemoryStore) SaveTargetSolv(_ context.Context, solv *TargetSolv) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.projects[solv.ProjectName]; !exists {
		return ErrNotFound
	}

	key := fmt.Sprintf("%s:%s:%s", solv.ProjectName, solv.TargetName, solv.Arch)
	s.solvs[key] = solv
	return nil
}

func (s *MemoryStore) GetTargetSolv(_ context.Context, projectName, targetName, arch string) (*TargetSolv, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s:%s:%s", projectName, targetName, arch)
	solv, found := s.solvs[key]
	if !found {
		return nil, ErrNotFound
	}
	return solv, nil
}

func (s *MemoryStore) ListTargetSolvs(_ context.Context, projectName string) ([]*TargetSolv, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := projectName + ":"
	var list []*TargetSolv
	for k, v := range s.solvs {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			list = append(list, v)
		}
	}
	return list, nil
}

func buildStateKey(st *PackageBuildState) string {
	return strings.Join([]string{st.Project, st.Repo, st.Arch, st.Package, st.Flavor}, "\x00")
}

func (s *MemoryStore) UpsertBuildState(_ context.Context, st *PackageBuildState) error {
	if st.Project == "" || st.Repo == "" || st.Arch == "" || st.Package == "" {
		return fmt.Errorf("build state requires project, repo, arch and package")
	}
	if !IsValidBuildState(st.State) {
		return fmt.Errorf("invalid build state %q", st.State)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := *st
	cp.UpdatedAt = time.Now()
	if prev, ok := s.states[buildStateKey(&cp)]; ok {
		preserveBuildIdentity(&cp, prev)
	}
	s.states[buildStateKey(&cp)] = &cp
	return nil
}

// preserveBuildIdentity keeps source_commit, meta and bcnt from prev when next does not
// carry them, mirroring the PostgreSQL upsert: status-only updates must not erase
// the identity of the last build.
func preserveBuildIdentity(next, prev *PackageBuildState) {
	if next.SourceCommit == "" {
		next.SourceCommit = prev.SourceCommit
	}
	if next.Meta == "" {
		next.Meta = prev.Meta
	}
	if next.Bcnt < prev.Bcnt {
		next.Bcnt = prev.Bcnt
	}
}

func (s *MemoryStore) ListBuildStates(_ context.Context, project string, filter BuildStateFilter) ([]*PackageBuildState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := []*PackageBuildState{}
	for _, st := range s.states {
		if st.Project == project && filter.Matches(st) {
			cp := *st
			list = append(list, &cp)
		}
	}
	sort.Slice(list, func(i, j int) bool { return buildStateKey(list[i]) < buildStateKey(list[j]) })
	return list, nil
}

func (s *MemoryStore) CountBuildStates(ctx context.Context, project string, filter BuildStateFilter) (map[string]int, error) {
	list, err := s.ListBuildStates(ctx, project, filter)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, st := range list {
		counts[st.State]++
	}
	return counts, nil
}

func (s *MemoryStore) SaveDoDRepo(_ context.Context, repo *DoDRepo) error {
	if repo.URL == "" || repo.RepomdChecksum == "" || repo.SolvKey == "" {
		return fmt.Errorf("dod repo requires url, repomd checksum and solv key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *repo
	cp.UpdatedAt = time.Now()
	s.dod[repo.URL] = &cp
	return nil
}

func (s *MemoryStore) GetDoDRepo(_ context.Context, url string) (*DoDRepo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	repo, ok := s.dod[url]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *repo
	return &cp, nil
}
