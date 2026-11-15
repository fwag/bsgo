package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ValidateCounterKey checks that every field a counter row is keyed by is set.
func ValidateCounterKey(key CounterKey) error {
	if key.Project == "" || key.Repo == "" || key.Arch == "" || key.Package == "" ||
		key.SourceCommit == "" || key.Meta == "" {
		return fmt.Errorf("build counters require project, repo, arch, package, source commit and meta")
	}
	return nil
}

// NextBuildCounters implements Store.
func (s *MemoryStore) NextBuildCounters(_ context.Context, key CounterKey) (BuildCounters, error) {
	if err := ValidateCounterKey(key); err != nil {
		return BuildCounters{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.counters[key]; ok {
		return c, nil
	}
	ci, maxCI, b := 0, 0, 0
	for k, c := range s.counters {
		if k.Project != key.Project || k.Package != key.Package || k.Flavor != key.Flavor {
			continue
		}
		maxCI = max(maxCI, c.CI)
		if k.SourceCommit != key.SourceCommit {
			continue
		}
		ci = c.CI
		if k.Repo == key.Repo && k.Arch == key.Arch {
			b = max(b, c.B)
		}
	}
	if ci == 0 {
		ci = maxCI + 1
	}
	c := BuildCounters{CI: ci, B: b + 1}
	s.counters[key] = c
	return c, nil
}

// RequestForcedRebuild implements Store.
func (s *MemoryStore) RequestForcedRebuild(_ context.Context, req ForcedRebuild) error {
	if req.Project == "" || req.Package == "" {
		return fmt.Errorf("forced rebuild requires project and package")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.forced {
		if f.Project == req.Project && f.Repo == req.Repo && f.Arch == req.Arch &&
			f.Package == req.Package && f.Flavor == req.Flavor {
			return nil
		}
	}
	req.RequestedAt = time.Now()
	s.forced = append(s.forced, req)
	return nil
}

// ListForcedRebuilds implements Store.
func (s *MemoryStore) ListForcedRebuilds(_ context.Context, project string) ([]ForcedRebuild, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ForcedRebuild
	for _, f := range s.forced {
		if f.Project == project {
			out = append(out, f)
		}
	}
	return out, nil
}

// ClearForcedRebuilds implements Store.
func (s *MemoryStore) ClearForcedRebuilds(_ context.Context, project, repo, arch string, units []UnitRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []ForcedRebuild
	for _, f := range s.forced {
		if !coversAny(f, project, repo, arch, units) {
			kept = append(kept, f)
		}
	}
	s.forced = kept
	return nil
}

func coversAny(f ForcedRebuild, project, repo, arch string, units []UnitRef) bool {
	if f.Project != project {
		return false
	}
	for _, u := range units {
		if f.Applies(repo, arch, u) {
			return true
		}
	}
	return false
}

func buildRepoIndexKey(project, repo, arch string) string {
	return strings.Join([]string{project, repo, arch}, "\x00")
}

// ValidateBuildRepoIndex checks that a pointer row is complete.
func ValidateBuildRepoIndex(idx *BuildRepoIndex) error {
	if idx.Project == "" || idx.Repo == "" || idx.Arch == "" || idx.ContentHash == "" ||
		idx.SolvKey == "" || idx.EntriesKey == "" {
		return fmt.Errorf("build repo index requires project, repo, arch, content hash and keys")
	}
	return nil
}

// SaveBuildRepoIndex implements Store.
func (s *MemoryStore) SaveBuildRepoIndex(_ context.Context, idx *BuildRepoIndex) error {
	if err := ValidateBuildRepoIndex(idx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *idx
	cp.UpdatedAt = time.Now()
	s.indexes[buildRepoIndexKey(idx.Project, idx.Repo, idx.Arch)] = &cp
	return nil
}

// GetBuildRepoIndex implements Store.
func (s *MemoryStore) GetBuildRepoIndex(_ context.Context, project, repo, arch string) (*BuildRepoIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.indexes[buildRepoIndexKey(project, repo, arch)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *idx
	return &cp, nil
}

// GetBuildState implements Store.
func (s *MemoryStore) GetBuildState(_ context.Context, project, repo, arch, pkg, flavor string) (*PackageBuildState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[buildStateKey(&PackageBuildState{Project: project, Repo: repo, Arch: arch, Package: pkg, Flavor: flavor})]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *st
	return &cp, nil
}
