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
		p = &Plan{Prefix: prefix, Prpa: prpa, scope: prefix + "/" + prpa + "/"}
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
// superseded ones (only while the prpa has a pointer) and every version that is
// too recent. A version is too recent when it is younger than the grace period
// itself or when the version that superseded it (the next newer upload) is:
// readers that loaded the previous pointer may still be using it, however old
// its objects are.
func decideVersions(p *Plan, vs map[string]*indexVersion, hasPointer bool, now time.Time, opts Options) {
	list := sortedVersions(vs)
	keep := map[*indexVersion]bool{}
	for i, v := range list {
		if v.current || v.young(now, opts.Grace) {
			keep[v] = true
		}
		if i+1 < len(list) && list[i+1].young(now, opts.Grace) {
			keep[v] = true
		}
	}
	superseded := 0
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].current {
			continue
		}
		if hasPointer && superseded < opts.KeepVersions {
			keep[list[i]] = true
		}
		superseded++
	}
	for _, v := range list {
		for _, o := range v.objs {
			if keep[v] {
				p.keep(o)
			} else {
				p.drop(o)
			}
		}
	}
}

func (v *indexVersion) young(now time.Time, grace time.Duration) bool {
	for _, o := range v.objs {
		if young(o, now, grace) {
			return true
		}
	}
	return false
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

// recentlySupersededEntries returns the entries.json keys of the index
// versions whose successor (the next newer version of the prpa) is younger than
// grace. Those versions may still be read by jobs and publisher runs that
// loaded the previous pointer, so the binaries they list stay.
func recentlySupersededEntries(all []Object, now time.Time, grace time.Duration) []string {
	byPrpa := map[string]map[string]*indexVersion{}
	entries := map[string]string{}
	for _, o := range all {
		prpa, hash, ok := splitIndexKey(o.Key)
		if !ok {
			continue
		}
		v := versionOf(byPrpa, prpa, hash)
		v.objs = append(v.objs, o)
		if o.LastModified.After(v.newest) {
			v.newest = o.LastModified
		}
		if strings.HasSuffix(o.Key, ".entries.json") {
			entries[prpa+"/"+hash] = o.Key
		}
	}
	var out []string
	for prpa, vs := range byPrpa {
		list := sortedVersions(vs)
		for i := 0; i+1 < len(list); i++ {
			if k, ok := entries[prpa+"/"+list[i].hash]; ok && list[i+1].young(now, grace) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sortedVersions(vs map[string]*indexVersion) []*indexVersion {
	list := make([]*indexVersion, 0, len(vs))
	for _, v := range vs {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].newest.Equal(list[j].newest) {
			return list[i].newest.Before(list[j].newest)
		}
		return list[i].hash < list[j].hash
	})
	return list
}
