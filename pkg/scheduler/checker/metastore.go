package checker

import (
	"fmt"
	"sort"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// maxMetaCache bounds the cached metas; the caches are rebuilt from the store
// on demand.
const maxMetaCache = 20000

// tree returns the meta stored under hash, or nil when it is not known.
func (c *Checker) tree(hash string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lines := c.trees[hash]; len(lines) > 0 {
		return lines
	}
	return nil
}

func (c *Checker) cachedMeta(key string) *metaResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metas[key]
}

func (c *Checker) storeMeta(key string, r *metaResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.metas) > maxMetaCache || len(c.trees) > maxMetaCache {
		clear(c.metas)
		clear(c.trees)
	}
	c.metas[key] = r
	c.trees[r.hash] = r.lines
}

// loadMetaTrees reads the metas of the last successful builds of the units
// that produced binaries of the own build repository, which the metas of their
// consumers include. Metas already cached are not read again.
func (ps *pass) loadMetaTrees() error {
	var want []string
	seen := map[string]bool{}
	for name := range ps.own {
		u := ps.byName[name]
		if u == nil {
			continue
		}
		st := ps.states[unitKey(u.pkg, u.flavor)]
		if st == nil || st.BuiltMeta == "" || seen[st.BuiltMeta] || ps.c.tree(st.BuiltMeta) != nil {
			continue
		}
		seen[st.BuiltMeta] = true
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
	ps.c.mu.Lock()
	defer ps.c.mu.Unlock()
	for _, m := range metas {
		ps.c.trees[m.Hash] = m.Lines
	}
	return nil
}

// saveMetas persists the metas of the newly scheduled builds and drops the
// ones no state refers to any more. It runs before the states are written: a
// state never points to a meta that is not stored.
func (ps *pass) saveMetas() error {
	var metas []storage.BuildMeta
	keep := map[string]bool{}
	for _, st := range ps.states {
		keep[st.Meta], keep[st.BuiltMeta] = true, true
	}
	for _, d := range ps.out.list {
		if d.job == nil || d.keep || d.lines == nil {
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
	if err := ps.c.d.Store.PruneBuildMetas(ps.ctx, p.Project, p.Repo, p.Arch, hashes); err != nil {
		return fmt.Errorf("prune build metas: %w", err)
	}
	return nil
}
