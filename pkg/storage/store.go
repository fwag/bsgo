package storage

import (
	"context"
	"errors"
	"fmt"
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

type Store interface {
	CreateProject(ctx context.Context, prj *manifest.ProjectManifest) error
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
}

type MemoryStore struct {
	mu       sync.RWMutex
	projects map[string]*manifest.ProjectManifest
	packages map[string]map[string]*manifest.PackageManifest
	keys     map[string]map[string]*ProjectSigningKey // key: project -> flavor:type -> key
	solvs    map[string]*TargetSolv                   // key: project:target:arch -> solv
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects: make(map[string]*manifest.ProjectManifest),
		packages: make(map[string]map[string]*manifest.PackageManifest),
		keys:     make(map[string]map[string]*ProjectSigningKey),
		solvs:    make(map[string]*TargetSolv),
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
