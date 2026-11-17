package checker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openSUSE/bsgo/pkg/solv"
)

// genMetaAlgo returns the BuildFlags genmetaalgo value; 1 is the default like
// in OBS. See genMeta for what the algorithms do with dependency cycles.
func (ps *pass) genMetaAlgo() int {
	if v, ok := ps.sched.BuildFlagsH["genmetaalgo"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 1 {
			return n
		}
	}
	return 1
}

// metaResult is the meta of one build: its lines (see metatree.go) and their
// hash, which is what the build state stores and compares.
type metaResult struct {
	hash  string
	lines []string
}

func newMetaResult(lines []string) *metaResult {
	h := sha256.New()
	h.Write([]byte("bsgo-meta-v2\n"))
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return &metaResult{hash: hex.EncodeToString(h.Sum(nil)), lines: lines}
}

// meta computes the meta of a build of u (BSSched/BuildJob/Package.pm). The
// rebuild policy of the target narrows it: "local" looks at the source only,
// "direct" at the packages the recipe names and without nested lines.
//
// A dependency built in this prpa contributes the meta its producer was last
// built with, a tree, instead of the identity of its files; genMeta then cuts
// everything that leads back to u's own binaries. So a cycle does not
// retrigger itself, while a change deep below a cycle member still reaches
// the other members through the lines of that member.
func (ps *pass) meta(u *unit) *metaResult {
	first := u.commit + "  " + u.name
	policy := ps.target.Flags.EffectiveRebuild()
	if policy == "local" {
		return newMetaResult([]string{first})
	}
	deps := ps.metaDeps(u, policy)
	key := ps.metaCheck(u, policy, deps)
	if r := ps.c.cachedMeta(key); r != nil {
		return r
	}
	gen, err := genMeta(u.info.Subpacks, ps.depLines(u, deps), ps.genMetaAlgo())
	if err != nil {
		ps.fail(fmt.Errorf("meta of %s: %w", u.name, err))
	}
	gen = append([]string{first}, gen...)
	if policy == "direct" {
		gen = withoutNested(gen)
	}
	r := newMetaResult(gen)
	ps.c.storeMeta(key, r)
	return r
}

func withoutNested(lines []string) []string {
	out := lines[:0:0]
	for _, l := range lines {
		if !strings.Contains(l, "/") {
			out = append(out, l)
		}
	}
	return out
}

// depSource is one build dependency of a meta: the binary and, when it was
// built in this prpa, the meta its producer was last built with.
type depSource struct {
	pkg solv.ResolvedPackage
	// metaHash and tree are empty for binaries that count by their sha256.
	metaHash string
	tree     []string
}

// token identifies the contribution of the dependency for the meta cache.
func (d depSource) token() string {
	if d.tree != nil {
		return "L " + d.pkg.Name + " " + d.metaHash
	}
	return "P " + d.pkg.Name + " " + d.pkg.SHA256
}

// metaDeps returns the build dependencies that take part in the meta of u,
// sorted by name, without the binaries u produces itself.
func (ps *pass) metaDeps(u *unit, policy string) []depSource {
	var out []depSource
	for _, p := range u.edeps.Packages {
		if policy == "direct" && !u.deps[p.Name] {
			continue
		}
		prod := ps.prod[p.Name]
		if prod == u {
			continue
		}
		d := depSource{pkg: p}
		d.metaHash, d.tree = ps.producerMeta(prod, p.Name)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pkg.Name < out[j].pkg.Name })
	return out
}

// producerMeta returns the meta the producer of binary name was last
// successfully built with. It is empty when the binary does not come from a
// build in this prpa or its meta is unknown (a build from before metas were
// kept): the binary then counts by its sha256 like in OBS.
func (ps *pass) producerMeta(prod *unit, name string) (string, []string) {
	if prod == nil || !ps.isOwnBinary(prod, name) {
		return "", nil
	}
	st := ps.states[unitKey(prod.pkg, prod.flavor)]
	if st == nil || st.BuiltMeta == "" {
		return "", nil
	}
	return st.BuiltMeta, ps.c.tree(st.BuiltMeta)
}

// isOwnBinary reports whether name is a binary of prod in the own build
// repository (as opposed to a same-named binary further down the path).
func (ps *pass) isOwnBinary(prod *unit, name string) bool {
	for _, n := range ps.own[prod.name] {
		if n == name {
			return true
		}
	}
	return false
}

// metaCheck is the shortcut key of OBS (the "hdrmetamd5"): everything the
// meta depends on, hashed. Equal keys give equal metas, so the lines need not
// be generated again.
func (ps *pass) metaCheck(u *unit, policy string, deps []depSource) string {
	h := sha256.New()
	fmt.Fprintf(h, "v2\n%d\n%s\n%s\n%s\n%s\n", ps.genMetaAlgo(), policy, u.name, u.commit,
		strings.Join(u.info.Subpacks, " "))
	for _, d := range deps {
		fmt.Fprintln(h, d.token())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// depLines generates the dependency lines of u: one line per build
// dependency, followed by the lines of the meta of its local producer
// (BSSched/BuildJob/Package.pm).
func (ps *pass) depLines(u *unit, deps []depSource) []string {
	var lines []string
	for _, d := range deps {
		if d.tree == nil {
			lines = append(lines, shortID(d.pkg.SHA256)+"  "+d.pkg.Name)
			continue
		}
		lines = addMeta(lines, d.tree, d.pkg.Name, u.name)
	}
	return lines
}

// attemptID identifies one build attempt of a meta. A forced rebuild of an
// unchanged meta is a new attempt, distinguished by the time of the request.
func attemptID(meta string, forcedAt time.Time) string {
	if forcedAt.IsZero() {
		return meta
	}
	sum := sha256.Sum256([]byte(meta + "|force|" + strconv.FormatInt(forcedAt.UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

var jobNamespace = uuid.MustParse("6f1a3c2e-5b7d-4c0a-9e41-2d8f7a1b9c53")

// jobID derives the deterministic job ID of an attempt.
func (ps *pass) jobID(u *unit, attempt string) string {
	return uuid.NewSHA1(jobNamespace, []byte(ps.prpa.String()+"\x00"+u.name+"\x00"+attempt)).String()
}

// release substitutes the counters into the Release directive.
// release computes the release of a build like BSSched/BuildJob.pm: the
// first field of the Release: directive, unless a later "<buildtype>:"
// field overrides it (SLFO: "Release: <CI_CNT>.<B_CNT> spec:160000.<CI_CNT>.<B_CNT>").
func (ps *pass) release(buildType string, ci, b int) string {
	tmpl := ps.sched.Release
	if len(ps.sched.ReleaseList) > 1 {
		for _, f := range ps.sched.ReleaseList[1:] {
			if v, ok := strings.CutPrefix(f, buildType+":"); ok {
				tmpl = v
			}
		}
	}
	if tmpl == "" {
		tmpl = "<CI_CNT>.<B_CNT>"
	}
	if start, err := strconv.Atoi(strings.TrimSpace(ps.sched.CICntStart)); err == nil && start > 1 {
		ci += start - 1
	}
	return strings.NewReplacer("<CI_CNT>", strconv.Itoa(ci), "<B_CNT>", strconv.Itoa(b)).Replace(tmpl)
}
