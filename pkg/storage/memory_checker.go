package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ValidateCounterKey checks that every field a counter row is keyed by is set.
func ValidateCounterKey(key CounterKey) error {
	if key.Project == "" || key.Repo == "" || key.Arch == "" || key.Package == "" ||
		key.SourceCommit == "" || key.Meta == "" || key.Versrel == "" || key.CI < 1 {
		return fmt.Errorf("build counters require project, repo, arch, package, source commit, meta, versrel and ci")
	}
	return nil
}

// counterRow is the identity of a build_counters row; the vrev and versrel
// of the build are values.
type counterRow struct {
	project, repo, arch, pkg, flavor, commit, meta string
}

type counterValue struct {
	ci, b int
	// versrel is empty for rows written before revisions existed.
	versrel string
}

func rowOf(key CounterKey) counterRow {
	return counterRow{key.Project, key.Repo, key.Arch, key.Package, key.Flavor, key.SourceCommit, key.Meta}
}

// AddLegacyCounters records a build made before revisions existed (a row
// without versrel), for tests of the legacy floor.
func (s *MemoryStore) AddLegacyCounters(key CounterKey, c BuildCounters) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters[rowOf(key)] = counterValue{ci: c.CI, b: c.B}
}

// NextBuildCounters implements Store.
func (s *MemoryStore) NextBuildCounters(_ context.Context, key CounterKey) (BuildCounters, error) {
	if err := ValidateCounterKey(key); err != nil {
		return BuildCounters{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A row written before revisions existed belongs to a job that was never
	// built (otherwise no new job is made for its key): it is renumbered.
	if v, ok := s.counters[rowOf(key)]; ok && v.versrel != "" {
		return BuildCounters{CI: v.ci, B: v.b}, nil
	}
	b, legacyCI, legacyB := 0, 0, 0
	for r, v := range s.counters {
		if r.project != key.Project || r.pkg != key.Package || r.flavor != key.Flavor {
			continue
		}
		sameRepo := r.repo == key.Repo && r.arch == key.Arch
		switch {
		case v.versrel == "":
			legacyCI = max(legacyCI, v.ci)
			if sameRepo {
				legacyB = max(legacyB, v.b)
			}
		case sameRepo && v.versrel == key.Versrel:
			b = max(b, v.b)
		}
	}
	b++
	if key.CI <= legacyCI {
		b = max(b, legacyB+1)
	}
	s.counters[rowOf(key)] = counterValue{ci: key.CI, b: b, versrel: key.Versrel}
	return BuildCounters{CI: key.CI, B: b}, nil
}

// RequestForcedRebuild implements Store.
func (s *MemoryStore) RequestForcedRebuild(_ context.Context, req ForcedRebuild) error {
	if req.Project == "" || req.Package == "" {
		return fmt.Errorf("forced rebuild requires project and package")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.forced {
		if f.Project == req.Project && f.Repo == req.Repo && f.Arch == req.Arch &&
			f.Package == req.Package && f.Flavor == req.Flavor {
			s.forced[i].Priority = req.Priority
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

// ListBuildRepoIndexes implements Store.
func (s *MemoryStore) ListBuildRepoIndexes(_ context.Context) ([]*BuildRepoIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*BuildRepoIndex, 0, len(s.indexes))
	for _, idx := range s.indexes {
		cp := *idx
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Arch < b.Arch
	})
	return out, nil
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
