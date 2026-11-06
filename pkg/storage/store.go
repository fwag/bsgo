package storage

import (
	"context"
	"errors"
	"sync"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource already exists")
)

type Store interface {
	CreateProject(ctx context.Context, prj *manifest.ProjectManifest) error
	GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error)
	ListProjects(ctx context.Context) ([]*manifest.ProjectManifest, error)

	UpsertPackage(ctx context.Context, projectName string, pkg *manifest.PackageManifest) error
	GetPackage(ctx context.Context, projectName, packageName string) (*manifest.PackageManifest, error)
	ListPackages(ctx context.Context, projectName string) ([]*manifest.PackageManifest, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	projects map[string]*manifest.ProjectManifest
	packages map[string]map[string]*manifest.PackageManifest
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects: make(map[string]*manifest.ProjectManifest),
		packages: make(map[string]map[string]*manifest.PackageManifest),
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
