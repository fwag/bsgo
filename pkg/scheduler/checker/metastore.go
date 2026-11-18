package checker

import (
	"fmt"
	"sort"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// Bounds of the checker wide caches. A pass never depends on them: it copies
// the trees it needs into pass.trees, so eviction cannot change a decision.
const (
	// metaGrace is how long an unreferenced meta is kept: a job that was
	// superseded may still finish and then refers to its meta.
	metaGrace = 48 * time.Hour

	maxTreeCache  = 4000
	maxCheckCache = 50000
)

// evict drops arbitrary entries until at most half of limit remain.
func evict[V any](m map[string]V, limit int) {
	if len(m) <= limit {
		return
	}
	for k := range m {
		if len(m) <= limit/2 {
			return
		}
		delete(m, k)
	}
}

// cachedMeta returns the hash of the meta generated for the inputs key (the
// OBS meta shortcut).
func (c *Checker) cachedMeta(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.metas[key]
	return h, ok
}

func (c *Checker) storeMeta(key, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	evict(c.metas, maxCheckCache)
	c.metas[key] = hash
}

// loadMetaTrees puts the metas of the last successful builds of the units
// that produced binaries of the own build repository, and of the members of
// cycles, into pass.trees: the metas of their consumers include them. Metas
// the checker cached are not read again.
func (ps *pass) loadMetaTrees() error {
	var want []string
	for _, u := range ps.units {
		st := ps.states[unitKey(u.pkg, u.flavor)]
		if st == nil || st.BuiltMeta == "" {
			continue
		}
		if len(ps.own[u.name]) == 0 && (u.group == nil || !u.group.cyclic) {
			continue
		}
		if _, ok := ps.trees[st.BuiltMeta]; ok {
			continue
		}
		if lines := ps.c.cachedTree(st.BuiltMeta); lines != nil {
			ps.trees[st.BuiltMeta] = lines
			continue
		}
		ps.trees[st.BuiltMeta] = nil
		want = append(want, st.BuiltMeta)
	}
	if len(want) == 0 {
		return nil
	}
	sort.Strings(want)
	metas, err := ps.c.d.Store.GetBuildMetas(ps.ctx, ps.prpa.Project, ps.prpa.Repo, ps.prpa.Arch, want)
	if err != nil {
		return fmt.Errorf("load build metas: %w", err)
	}
	for _, m := range metas {
		ps.trees[m.Hash] = m.Lines
	}
	ps.c.cacheTrees(metas)
	return nil
}

func (c *Checker) cachedTree(hash string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.trees[hash]
}

func (c *Checker) cacheTrees(metas []storage.BuildMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	evict(c.trees, maxTreeCache)
	for _, m := range metas {
		c.trees[m.Hash] = m.Lines
	}
}

// saveMetas persists the metas of the newly scheduled builds and drops the
// ones no state refers to any more. It runs before jobs are emitted and the
// states are written: neither ever points to a meta that is not stored.
func (ps *pass) saveMetas() error {
	var metas []storage.BuildMeta
	keep := map[string]bool{}
	for _, st := range ps.states {
		keep[st.Meta], keep[st.BuiltMeta] = true, true
	}
	for _, d := range ps.out.list {
		if d.job == nil || d.lines == nil {
			continue
		}
		keep[d.meta] = true
		metas = append(metas, storage.BuildMeta{Hash: d.meta, Lines: d.lines})
	}
	if len(metas) == 0 {
		return nil
	}
	p := ps.prpa
	if err := ps.c.d.Store.SaveBuildMetas(ps.ctx, p.Project, p.Repo, p.Arch, metas); err != nil {
		return fmt.Errorf("save build metas: %w", err)
	}
	delete(keep, "")
	hashes := make([]string, 0, len(keep))
	for h := range keep {
		hashes = append(hashes, h)
	}
	if err := ps.c.d.Store.PruneBuildMetas(ps.ctx, p.Project, p.Repo, p.Arch, hashes, ps.c.d.Now().Add(-metaGrace)); err != nil {
		return fmt.Errorf("prune build metas: %w", err)
	}
	return nil
}
