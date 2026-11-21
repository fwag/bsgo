package gc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// Pointers is the pointer table; storage.Store satisfies it.
type Pointers interface {
	ListBuildRepoIndexes(ctx context.Context) ([]*storage.BuildRepoIndex, error)
}

// errNoPointers guards against a pointer table that is empty or was swapped
// for a fresh one: with no root set everything would look like garbage.
var errNoPointers = errors.New("no build_repo_index rows while objects exist; refusing to treat everything as garbage")

// indexProtected returns the solv and entries keys of every current pointer.
func indexProtected(ptrs Pointers) Protected {
	return func(ctx context.Context) (map[string]struct{}, error) {
		list, err := ptrs.ListBuildRepoIndexes(ctx)
		if err != nil {
			return nil, err
		}
		set := make(map[string]struct{}, 2*len(list))
		for _, p := range list {
			set[p.SolvKey] = struct{}{}
			set[p.EntriesKey] = struct{}{}
		}
		return set, nil
	}
}

// indexVersion is the pair of objects of one content hash.
type indexVersion struct {
	hash    string
	objs    []Object
	newest  time.Time
	current bool
}

// splitIndexKey splits buildrepo/<project>/<repo>/<arch>/<hash>.<ext>.
func splitIndexKey(key string) (prpa, hash string, ok bool) {
	parts := strings.SplitN(key, "/", 5)
	if len(parts) != 5 || parts[0] != PrefixBuildRepo {
		return "", "", false
	}
	for _, ext := range []string{".entries.json", ".solv"} {
		if h, found := strings.CutSuffix(parts[4], ext); found && h != "" {
			return strings.Join(parts[1:4], "/"), h, true
		}
	}
	return "", "", false
}

func listPrefix(root, project string) string {
	if project == "" {
		return root + "/"
	}
	return root + "/" + project + "/"
}

// PlanIndexGC decides, per prpa, which objects below buildrepo/ to delete. An
// index version (the .solv and .entries.json of one hash) is kept when a
// current pointer references it, when it is among the opts.KeepVersions most
// recent superseded versions of a prpa that still has a pointer, or when it is
// younger than opts.Grace. Everything else, including every version of a prpa
// without pointer, is deleted as a pair. Files that are not index objects are
// kept.
func PlanIndexGC(ctx context.Context, ptrs Pointers, objs Objects, now time.Time, opts Options) ([]Plan, error) {
	list, err := ptrs.ListBuildRepoIndexes(ctx)
	if err != nil {
		return nil, err
	}
	all, err := objs.List(ctx, listPrefix(PrefixBuildRepo, opts.Project))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 && len(all) > 0 {
		return nil, errNoPointers
	}
	cur := map[string]struct{}{}
	hasPointer := map[string]bool{}
	for _, p := range list {
		cur[p.SolvKey], cur[p.EntriesKey] = struct{}{}, struct{}{}
		hasPointer[p.Project+"/"+p.Repo+"/"+p.Arch] = true
	}
	plans := map[string]*Plan{}
	versions := map[string]map[string]*indexVersion{}
	for _, o := range all {
		prpa, hash, ok := splitIndexKey(o.Key)
		if !ok {
			// Not an index object: not ours to delete.
			if parts := strings.SplitN(o.Key, "/", 5); len(parts) == 5 {
				planOf(plans, PrefixBuildRepo, strings.Join(parts[1:4], "/")).keep(o)
			}
			continue
		}
		planOf(plans, PrefixBuildRepo, prpa)
		v := versionOf(versions, prpa, hash)
		v.objs = append(v.objs, o)
		if o.LastModified.After(v.newest) {
			v.newest = o.LastModified
		}
		if _, isCur := cur[o.Key]; isCur {
			v.current = true
		}
	}
	for prpa, vs := range versions {
		decideVersions(plans[prpa], vs, hasPointer[prpa], now, opts)
	}
	return sortedPlans(plans), nil
}

func planOf(plans map[string]*Plan, prefix, prpa string) *Plan {
	p, ok := plans[prpa]
	if !ok {
		p = &Plan{Prefix: prefix, Prpa: prpa}
		plans[prpa] = p
	}
	return p
}

func versionOf(versions map[string]map[string]*indexVersion, prpa, hash string) *indexVersion {
	vs, ok := versions[prpa]
	if !ok {
		vs = map[string]*indexVersion{}
		versions[prpa] = vs
	}
	v, ok := vs[hash]
	if !ok {
		v = &indexVersion{hash: hash}
		vs[hash] = v
	}
	return v
}

// decideVersions keeps the current version, the newest opts.KeepVersions
// superseded ones (only while the prpa has a pointer) and the young ones.
func decideVersions(p *Plan, vs map[string]*indexVersion, hasPointer bool, now time.Time, opts Options) {
	var old []*indexVersion
	for _, v := range vs {
		if v.current || now.Sub(v.newest) < opts.Grace {
			keepAll(p, v)
			continue
		}
		old = append(old, v)
	}
	sort.Slice(old, func(i, j int) bool {
		if !old[i].newest.Equal(old[j].newest) {
			return old[i].newest.After(old[j].newest)
		}
		return old[i].hash < old[j].hash
	})
	for i, v := range old {
		if hasPointer && i < opts.KeepVersions {
			keepAll(p, v)
			continue
		}
		for _, o := range v.objs {
			p.drop(o)
		}
	}
}

func keepAll(p *Plan, v *indexVersion) {
	for _, o := range v.objs {
		p.keep(o)
	}
}

func sortedPlans(plans map[string]*Plan) []Plan {
	out := make([]Plan, 0, len(plans))
	for _, p := range plans {
		p.finish()
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prpa < out[j].Prpa })
	return out
}
