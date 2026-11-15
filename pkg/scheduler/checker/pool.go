package checker

import (
	"fmt"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

// poolSource describes one repository of the path pool, in pool order.
type poolSource struct {
	elem  buildconfig.PathElem
	index *buildrepo.Index // set for build repositories
	url   string           // set for DoD (URL) elements
}

// buildPool builds the dependency pool: the prpa's own build repository
// first, then the path elements in order. A name present in an earlier
// repository hides the same name in later ones, so binaries built in this
// prpa shadow a Bootstrap Dependency of the same name.
func (ps *pass) buildPool() error {
	var blobs []solv.RepoBlob
	for _, el := range ps.path {
		blob, src, ok, err := ps.pathRepo(el)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		blobs = append(blobs, blob)
		ps.sources = append(ps.sources, src)
	}
	pool, err := ps.c.d.Pools.Get(ps.prpa.Arch, blobs)
	if err != nil {
		return fmt.Errorf("build pool of %s: %w", ps.prpa, err)
	}
	ps.pool = pool
	return nil
}

// pathRepo loads one path element; ok is false when it has no content yet.
func (ps *pass) pathRepo(el buildconfig.PathElem) (blob solv.RepoBlob, src poolSource, ok bool, err error) {
	if el.URL != "" {
		blob, err = ps.c.d.DoD.Get(ps.ctx, el.URL)
		if err != nil {
			return blob, src, false, fmt.Errorf("path element %s/%s (%s): %w", el.Project, el.Repo, el.URL, err)
		}
		blob.Name = el.Project + "/" + el.Repo
		return blob, poolSource{elem: el, url: el.URL}, len(blob.Data) > 0, nil
	}
	p := Prpa{Project: el.Project, Repo: el.Repo, Arch: ps.prpa.Arch}
	idx, err := ps.c.d.Indexes.Index(ps.ctx, p)
	if err != nil {
		return blob, src, false, err
	}
	if len(idx.Solv) == 0 {
		return blob, src, false, nil
	}
	blob = solv.RepoBlob{Name: p.String(), Hash: idx.Hash, Data: idx.Solv}
	return blob, poolSource{elem: el, index: idx}, true, nil
}
