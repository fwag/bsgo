package storage

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Repository states of one (project, repo, arch), after the OBS repository
// states (osc results). The checker writes broken, building, blocked,
// finished and unpublished; the publisher writes publishing and published.
const (
	// RepoBroken: the project configuration of the repository is unusable.
	RepoBroken = "broken"
	// RepoBuilding: jobs are in flight (scheduled, dispatching, building,
	// signing or being integrated).
	RepoBuilding = "building"
	// RepoBlocked: no job is in flight but units wait for dependencies that
	// are not ready (or for unavailable sources).
	RepoBlocked = "blocked"
	// RepoFinished: all work is done, the build repository is not published.
	RepoFinished = "finished"
	// RepoPublishing: the publisher is writing the repository.
	RepoPublishing = "publishing"
	// RepoPublished: the repository published is the current build repository.
	RepoPublished = "published"
	// RepoUnpublished: the target does not publish.
	RepoUnpublished = "unpublished"
)

// PrpaState is the repository state of one (project, repo, arch).
type PrpaState struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Arch    string `json:"arch"`
	State   string `json:"state"`
	Details string `json:"details,omitempty"`
	// IndexHash is the content hash of the build repository index the state
	// refers to; published is current only while it equals the index hash.
	IndexHash string    `json:"index_hash,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ValidRepoStates lists every state accepted by the prpa_state table.
var ValidRepoStates = []string{
	RepoBroken, RepoBuilding, RepoBlocked, RepoFinished, RepoPublishing, RepoPublished, RepoUnpublished,
}

// ValidatePrpaState checks that a row is complete and its state known.
func ValidatePrpaState(st *PrpaState) error {
	if st.Project == "" || st.Repo == "" || st.Arch == "" {
		return fmt.Errorf("prpa state requires project, repo and arch")
	}
	for _, s := range ValidRepoStates {
		if s == st.State {
			return nil
		}
	}
	return fmt.Errorf("unknown repository state %q", st.State)
}

// RepoStateInput is what the checker knows at the end of a pass.
type RepoStateInput struct {
	// ConfigError is set when the repository configuration could not be used.
	ConfigError string
	// Counts is the number of units per build state.
	Counts map[string]int
	// PublishEnabled is the publish flag of the target.
	PublishEnabled bool
	// IndexHash is the hash of the current build repository index.
	IndexHash string
	// Current is the stored repository state, nil when there is none.
	Current *PrpaState
}

// DeriveRepoState decides the repository state after a checker pass, with the
// precedence of BSSched::Checker set_repo_state:
//
//  1. a configuration error makes the repository broken;
//  2. jobs in flight (scheduled, dispatching, building, signing, finished)
//     make it building;
//  3. otherwise units that are blocked make it blocked;
//  4. otherwise it is idle: unpublished when the target does not publish,
//     else finished, except that publishing, and published of the current
//     index, are kept (write is false), so that a pass does not undo the
//     publisher.
//
// write reports whether the stored row must change.
func DeriveRepoState(in RepoStateInput) (state, details string, write bool) {
	state, details = activeOrIdleState(in)
	cur := in.Current
	if cur == nil {
		return state, details, true
	}
	if state == RepoFinished && keepsPublisherState(cur, in.IndexHash) {
		return cur.State, cur.Details, false
	}
	return state, details, cur.State != state || cur.Details != details || cur.IndexHash != in.IndexHash
}

func keepsPublisherState(cur *PrpaState, indexHash string) bool {
	switch cur.State {
	case RepoPublishing:
		return true
	case RepoPublished:
		return cur.IndexHash == indexHash
	}
	return false
}

func activeOrIdleState(in RepoStateInput) (state, details string) {
	if in.ConfigError != "" {
		return RepoBroken, in.ConfigError
	}
	flight := 0
	for _, s := range []string{StateScheduled, StateDispatching, StateBuilding, StateSigning, StateFinished} {
		flight += in.Counts[s]
	}
	switch {
	case flight > 0:
		return RepoBuilding, fmt.Sprintf("%d jobs in flight", flight)
	case in.Counts[StateBlocked] > 0:
		return RepoBlocked, fmt.Sprintf("%d units blocked", in.Counts[StateBlocked])
	case !in.PublishEnabled:
		return RepoUnpublished, ""
	}
	return RepoFinished, ""
}

func prpaStateKey(project, repo, arch string) string { return project + "/" + repo + "/" + arch }

// SetPrpaState implements Store.
func (s *MemoryStore) SetPrpaState(_ context.Context, st *PrpaState) error {
	if err := ValidatePrpaState(st); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *st
	cp.UpdatedAt = time.Now()
	s.prpaStates[prpaStateKey(st.Project, st.Repo, st.Arch)] = &cp
	return nil
}

// GetPrpaState implements Store.
func (s *MemoryStore) GetPrpaState(_ context.Context, project, repo, arch string) (*PrpaState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.prpaStates[prpaStateKey(project, repo, arch)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *st
	return &cp, nil
}

// ListPrpaStates implements Store; rows are ordered by repo and arch.
func (s *MemoryStore) ListPrpaStates(_ context.Context, project string) ([]*PrpaState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*PrpaState
	for _, st := range s.prpaStates {
		if st.Project == project {
			cp := *st
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Arch < out[j].Arch
	})
	return out, nil
}
