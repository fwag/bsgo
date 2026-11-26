package storage

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Package revision states.
const (
	// RevisionPending is a revision whose vrev is not computed yet.
	RevisionPending = "pending"
	// RevisionResolved is a revision with its final vrev.
	RevisionResolved = "resolved"
	// RevisionSkipped is a superseded revision whose commit could not be
	// resolved; it takes no part in the floor of later revisions.
	RevisionSkipped = "skipped"
)

// Sources of a resolved vrev.
const (
	RevisionSourceGit   = "git"
	RevisionSourceAdmin = "admin"
)

// UnknownVersion is the version of a revision without a usable main recipe.
const UnknownVersion = "unknown"

const (
	// MaxRevisionAttempts is the number of failed resolutions after which a
	// superseded revision is skipped. The latest revision retries forever.
	MaxRevisionAttempts = 3
	maxRevisionBackoff  = 30 * time.Minute
	// WantedWindow is how long a revision counts as wanted after the checker
	// last asked for it; the checker repeats the request on every pass.
	WantedWindow = 30 * time.Minute
)

// RevisionBackoff is the wait before the next resolution attempt after the
// given number of failed attempts: 1m, 2m, 4m, ... at most 30m.
func RevisionBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		return maxRevisionBackoff
	}
	return min(time.Minute<<(attempts-1), maxRevisionBackoff)
}

// PackageRevision is one entry of the append-only revision log of a package,
// the equivalent of an OBS source revision: one is appended whenever the
// pinned (git url, commit) of the package changes. Its Vrev is the CI_CNT of
// the builds made from it.
type PackageRevision struct {
	Project string `json:"project"`
	Package string `json:"package"`
	Seq     int    `json:"seq"`
	GitURL  string `json:"git_url"`
	Commit  string `json:"commit"`
	State   string `json:"state"`
	// Version is the version of the main recipe, "unknown" when it has none.
	Version string `json:"version,omitempty"`
	// GitVrev is the counter derived from the git history alone.
	GitVrev int `json:"git_vrev,omitempty"`
	// Vrev is the final CI_CNT; zero until the revision is resolved.
	Vrev int `json:"vrev,omitempty"`
	// MinVrev is a lower bound for the vrev of the next resolution (set by an
	// operator recompute so the release never goes down).
	MinVrev   int    `json:"min_vrev,omitempty"`
	Source    string `json:"source,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// NextTryAt, ClaimedBy, ClaimedUntil and WantedAt are zero when unset.
	NextTryAt    time.Time `json:"next_try_at,omitzero"`
	ClaimedBy    string    `json:"claimed_by,omitempty"`
	ClaimedUntil time.Time `json:"claimed_until,omitzero"`
	WantedAt     time.Time `json:"wanted_at,omitzero"`
	CreatedAt    time.Time `json:"created_at,omitzero"`
	ResolvedAt   time.Time `json:"resolved_at,omitzero"`
}

// Resolved reports whether the revision has its final vrev.
func (r *PackageRevision) Resolved() bool { return r.State == RevisionResolved && r.Vrev > 0 }

// RevisionRef names the revision of a pinned commit.
type RevisionRef struct {
	Project string
	Package string
	Commit  string
}

// RevisionResult is what the history engine computed for a revision.
type RevisionResult struct {
	Version string
	GitVrev int
}

// RevisionCounts are the revision totals by state.
type RevisionCounts struct {
	Pending, Resolved, Skipped int
}

// ResolveVrev applies the OBS rule over the revisions before the one being
// resolved: vrev = max(git, floor+1, min) where floor is the highest vrev of an
// earlier resolved revision with the same version, and a change between a known
// and an unknown version is at least the previous vrev + 1. Skipped and pending
// revisions in earlier are ignored.
func ResolveVrev(earlier []PackageRevision, version string, gitVrev, minVrev int) int {
	floor := 0
	var prev *PackageRevision
	for i := range earlier {
		r := &earlier[i]
		if r.State != RevisionResolved {
			continue
		}
		if r.Version == version {
			floor = max(floor, r.Vrev)
		}
		if prev == nil || r.Seq > prev.Seq {
			prev = r
		}
	}
	vrev := max(gitVrev, floor+1, minVrev)
	if prev != nil && prev.Version != version && (version == UnknownVersion || prev.Version == UnknownVersion) {
		vrev = max(vrev, prev.Vrev+1)
	}
	return vrev
}

// SourceHistory is the memoized outcome of the version history fold of one
// commit (see package recipe/history).
type SourceHistory struct {
	URL, Commit, Lookup, ConfigHash string
	AlgoRev                         int
	MainRecipe, Version             string
	ReleaseStart                    int
	VT                              map[string]int
	Walked                          int
}

// HistoryKey selects the memo rows that are comparable with each other.
type HistoryKey struct {
	URL, Lookup, ConfigHash string
	AlgoRev                 int
}

func revisionKey(project, pkg string) string { return project + "\x00" + pkg }

// appendRevisionLocked appends a pending revision when the pin changed.
func (s *MemoryStore) appendRevisionLocked(project string, pkg string, url, commit string) {
	if url == "" || commit == "" {
		return
	}
	k := revisionKey(project, pkg)
	log := s.revisions[k]
	if n := len(log); n > 0 && log[n-1].GitURL == url && log[n-1].Commit == commit {
		return
	}
	s.revisions[k] = append(log, PackageRevision{
		Project: project, Package: pkg, Seq: len(log) + 1, GitURL: url, Commit: commit,
		State: RevisionPending, CreatedAt: s.now(),
	})
}

// RevisionFor implements Store.
func (s *MemoryStore) RevisionFor(_ context.Context, project, pkg, commit string) (*PackageRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	log := s.revisions[revisionKey(project, pkg)]
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Commit == commit {
			cp := log[i]
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// ListRevisions implements Store.
func (s *MemoryStore) ListRevisions(_ context.Context, project string, packages []string) ([]PackageRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	want := map[string]bool{}
	for _, p := range packages {
		want[p] = true
	}
	var out []PackageRevision
	for _, log := range s.revisions {
		for _, r := range log {
			if r.Project == project && (len(want) == 0 || want[r.Package]) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// WantRevisions implements Store.
func (s *MemoryStore) WantRevisions(_ context.Context, refs []RevisionRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, ref := range refs {
		log := s.revisions[revisionKey(ref.Project, ref.Package)]
		for i := len(log) - 1; i >= 0; i-- {
			if log[i].Commit == ref.Commit {
				if log[i].State == RevisionPending {
					log[i].WantedAt = now
				}
				break
			}
		}
	}
	return nil
}

// lowestPendingLocked returns the index of the lowest pending revision of a log.
func lowestPending(log []PackageRevision) int {
	for i := range log {
		if log[i].State == RevisionPending {
			return i
		}
	}
	return -1
}

// ClaimRevisions implements Store.
func (s *MemoryStore) ClaimRevisions(_ context.Context, owner string, limit int, lease time.Duration) ([]PackageRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	type cand struct {
		log    []PackageRevision
		idx    int
		wanted bool
	}
	var cands []cand
	for _, log := range s.revisions {
		i := lowestPending(log)
		if i < 0 {
			continue
		}
		r := &log[i]
		if r.NextTryAt.After(now) || r.ClaimedUntil.After(now) {
			continue
		}
		wanted := false
		for _, o := range log {
			if o.State == RevisionPending && !o.WantedAt.IsZero() && now.Sub(o.WantedAt) < WantedWindow {
				wanted = true
			}
		}
		cands = append(cands, cand{log, i, wanted})
	}
	sort.Slice(cands, func(a, b int) bool {
		ra, rb := &cands[a].log[cands[a].idx], &cands[b].log[cands[b].idx]
		if cands[a].wanted != cands[b].wanted {
			return cands[a].wanted
		}
		if !ra.CreatedAt.Equal(rb.CreatedAt) {
			return ra.CreatedAt.Before(rb.CreatedAt)
		}
		return revisionKey(ra.Project, ra.Package) < revisionKey(rb.Project, rb.Package)
	})
	var out []PackageRevision
	for _, c := range cands {
		if len(out) >= limit {
			break
		}
		r := &c.log[c.idx]
		r.ClaimedBy, r.ClaimedUntil = owner, now.Add(lease)
		out = append(out, *r)
	}
	return out, nil
}

// RenewRevisionClaim implements Store.
func (s *MemoryStore) RenewRevisionClaim(_ context.Context, rev PackageRevision, owner string, lease time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.pendingRevisionLocked(rev)
	if err != nil || row.ClaimedBy != owner {
		return ErrConflict
	}
	row.ClaimedUntil = s.now().Add(lease)
	return nil
}

// ReleaseRevisionClaims implements Store.
func (s *MemoryStore) ReleaseRevisionClaims(_ context.Context, owner string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, log := range s.revisions {
		for i := range log {
			if log[i].ClaimedBy == owner && log[i].State == RevisionPending {
				log[i].ClaimedBy, log[i].ClaimedUntil = "", time.Time{}
				n++
			}
		}
	}
	return n, nil
}

// pendingRevisionLocked returns the stored row of rev when it is still
// pending and the lowest pending revision of its package.
func (s *MemoryStore) pendingRevisionLocked(rev PackageRevision) (*PackageRevision, error) {
	log := s.revisions[revisionKey(rev.Project, rev.Package)]
	i := lowestPending(log)
	if i < 0 || log[i].Seq != rev.Seq || log[i].Commit != rev.Commit {
		return nil, ErrConflict
	}
	return &log[i], nil
}

// FinishRevision implements Store.
func (s *MemoryStore) FinishRevision(_ context.Context, rev PackageRevision, res RevisionResult) (*PackageRevision, error) {
	if res.Version == "" || res.GitVrev < 1 {
		return nil, fmt.Errorf("revision result requires a version and a git vrev")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.pendingRevisionLocked(rev)
	if err != nil {
		return nil, err
	}
	earlier := s.revisions[revisionKey(rev.Project, rev.Package)][:row.Seq-1]
	row.Vrev = ResolveVrev(earlier, res.Version, res.GitVrev, row.MinVrev)
	row.Version, row.GitVrev = res.Version, res.GitVrev
	row.State, row.Source, row.LastError = RevisionResolved, RevisionSourceGit, ""
	row.ClaimedBy, row.ClaimedUntil, row.ResolvedAt = "", time.Time{}, s.now()
	cp := *row
	return &cp, nil
}

// FailRevision implements Store.
func (s *MemoryStore) FailRevision(_ context.Context, rev PackageRevision, cause string, minRetry time.Duration) (*PackageRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.pendingRevisionLocked(rev)
	if err != nil {
		return nil, err
	}
	latest := row.Seq == len(s.revisions[revisionKey(rev.Project, rev.Package)])
	row.Attempts++
	row.LastError = cause
	row.ClaimedBy, row.ClaimedUntil = "", time.Time{}
	if !latest && row.Attempts >= MaxRevisionAttempts {
		row.State = RevisionSkipped
	} else {
		row.NextTryAt = s.now().Add(max(RevisionBackoff(row.Attempts), minRetry))
	}
	cp := *row
	return &cp, nil
}

// RevisionCounts implements Store.
func (s *MemoryStore) RevisionCounts(_ context.Context) (RevisionCounts, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var c RevisionCounts
	for _, log := range s.revisions {
		for _, r := range log {
			switch r.State {
			case RevisionPending:
				c.Pending++
			case RevisionResolved:
				c.Resolved++
			case RevisionSkipped:
				c.Skipped++
			}
		}
	}
	return c, nil
}

// ResetRevisions implements Store.
func (s *MemoryStore) ResetRevisions(_ context.Context, project string, packages []string, allowDecrease bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, p := range packages {
		want[p] = true
	}
	var done []string
	urls := map[string]bool{}
	for k, log := range s.revisions {
		n := len(log)
		if n == 0 || log[0].Project != project || (len(want) > 0 && !want[log[0].Package]) {
			continue
		}
		last := &s.revisions[k][n-1]
		urls[last.GitURL] = true
		if last.State == RevisionPending {
			continue
		}
		last.MinVrev = 0
		if !allowDecrease {
			last.MinVrev = last.Vrev
		}
		last.State, last.Vrev, last.Source, last.Attempts = RevisionPending, 0, "", 0
		last.LastError, last.NextTryAt, last.ResolvedAt = "", time.Time{}, time.Time{}
		done = append(done, last.Package)
	}
	for k := range s.history {
		if urls[s.history[k].URL] {
			delete(s.history, k)
		}
	}
	sort.Strings(done)
	return done, nil
}

// SetRevisionVrev implements Store.
func (s *MemoryStore) SetRevisionVrev(_ context.Context, project, pkg, commit, version string, vrev int) (*PackageRevision, error) {
	if vrev < 1 {
		return nil, fmt.Errorf("vrev must be at least 1")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.revisions[revisionKey(project, pkg)]
	if len(log) == 0 {
		return nil, ErrNotFound
	}
	row := &log[len(log)-1]
	if commit != "" && row.Commit != commit {
		return nil, ErrConflict
	}
	if version == "" {
		version = row.Version
	}
	if version == "" {
		return nil, fmt.Errorf("the revision has no version yet: pass one")
	}
	row.Version, row.Vrev, row.State, row.Source = version, vrev, RevisionResolved, RevisionSourceAdmin
	row.ClaimedBy, row.ClaimedUntil, row.ResolvedAt = "", time.Time{}, s.now()
	cp := *row
	return &cp, nil
}

func historyMapKey(h *SourceHistory) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", h.URL, h.Commit, h.Lookup, h.AlgoRev, h.ConfigHash)
}

// NearestHistory implements Store.
func (s *MemoryStore) NearestHistory(_ context.Context, key HistoryKey, commits []string) (*SourceHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range commits {
		probe := SourceHistory{URL: key.URL, Commit: c, Lookup: key.Lookup, AlgoRev: key.AlgoRev, ConfigHash: key.ConfigHash}
		if h, ok := s.history[historyMapKey(&probe)]; ok {
			return copyHistory(h), nil
		}
	}
	return nil, nil
}

func copyHistory(h *SourceHistory) *SourceHistory {
	cp := *h
	cp.VT = make(map[string]int, len(h.VT))
	for k, v := range h.VT {
		cp.VT[k] = v
	}
	return &cp
}

// PutHistory implements Store.
func (s *MemoryStore) PutHistory(_ context.Context, h SourceHistory) error {
	if h.URL == "" || h.Commit == "" || h.Lookup == "" {
		return fmt.Errorf("source history requires url, commit and lookup")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := historyMapKey(&h)
	if _, ok := s.history[k]; !ok {
		s.history[k] = copyHistory(&h)
	}
	return nil
}
