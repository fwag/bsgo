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
	// build generates the lines when only the hash was cached.
	build func() []string
}

// text returns the lines of the meta, generating them when needed.
func (m *metaResult) text() []string {
	if m.lines == nil && m.build != nil {
		m.lines = m.build()
	}
	return m.lines
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
	gen := func() []string { return ps.genLines(u, policy, deps) }
	if h, ok := ps.c.cachedMeta(key); ok {
		return &metaResult{hash: h, build: gen}
	}
	r := newMetaResult(gen())
	ps.c.storeMeta(key, r.hash)
	return r
}

// genLines generates the lines of the meta of u.
func (ps *pass) genLines(u *unit, policy string, deps []depSource) []string {
	gen, err := genMeta(ps.subpacksOf(u), ps.depLines(u, deps), ps.genMetaAlgo())
	if err != nil {
		ps.fail(fmt.Errorf("meta of %s: %w", u.name, err))
	}
	gen = append([]string{u.commit + "  " + u.name}, gen...)
	if policy == "direct" {
		gen = withoutNested(gen)
	}
	return gen
}

// subpacksOf returns the binary names of the source package of u, like the
// subpacks of OBS (BSSched/Checker.pm preparehashes): the names the recipes of
// all its flavors declare plus every binary that was built from it. Recipe
// parsing alone misses generated subpackages (python singlespec, lua).
func (ps *pass) subpacksOf(u *unit) []string {
	if names, ok := ps.subpacks[u.pkg]; ok {
		return names
	}
	set := map[string]bool{}
	for _, o := range ps.units {
		if o.pkg != u.pkg {
			continue
		}
		if o.info != nil {
			for _, n := range o.info.Subpacks {
				set[n] = true
			}
		}
		for _, n := range ps.own[o.name] {
			set[n] = true
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	ps.subpacks[u.pkg] = names
	return names
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
	id  string // identity of the binary, see binaryID
	// commit is the source commit its local producer built it from, for a
	// producer whose meta tree is unknown.
	commit string
	// metaHash and tree are empty for binaries that count by their sha256.
	metaHash string
	tree     []string
}

// token identifies the contribution of the dependency for the meta cache.
func (d depSource) token() string {
	if d.tree != nil {
		return "L " + d.pkg.Name + " " + d.metaHash
	}
	if d.commit != "" {
		return "C " + d.pkg.Name + " " + d.commit
	}
	return "P " + d.pkg.Name + " " + d.id
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
		d := depSource{pkg: p, id: ps.binaryID(p)}
		d.metaHash, d.tree, d.commit = ps.producerMeta(prod, p.Name)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pkg.Name < out[j].pkg.Name })
	return out
}

// producerMeta returns what is known about the build of a binary that comes
// from a build in this prpa: the meta its producer was last successfully built
// with. When that meta is unknown (a build from before metas were kept) it
// returns the source commit of that build instead, which is the id the first
// line of the missing meta would carry, so the line of the dependency is the
// same whether or not the tree exists yet. It returns nothing for binaries
// from other repositories or without a recorded build: they count by their
// sha256 like in OBS.
func (ps *pass) producerMeta(prod *unit, name string) (hash string, tree []string, commit string) {
	if prod == nil || !ps.isOwnBinary(prod, name) {
		return "", nil, ""
	}
	st := ps.states[unitKey(prod.pkg, prod.flavor)]
	if st == nil || st.BuiltMeta == "" {
		return "", nil, ""
	}
	if tree = ps.trees[st.BuiltMeta]; tree != nil {
		return st.BuiltMeta, tree, ""
	}
	return "", nil, st.BuiltCommit
}

// binaryID is the identity of a binary: its sha256, or when the repository
// metadata has none a hash of what identifies the file otherwise, with the
// repository named by project and repository or URL, not by pool position.
func (ps *pass) binaryID(p solv.ResolvedPackage) string {
	if p.SHA256 != "" {
		return p.SHA256
	}
	repo := ""
	if p.Repo >= 0 && p.Repo < len(ps.sources) {
		src := ps.sources[p.Repo]
		repo = src.elem.Project + "/" + src.elem.Repo + "|" + src.url
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{repo, p.Name, p.EVR, p.Arch, p.Location}, "\x00")))
	return hex.EncodeToString(sum[:])
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
		strings.Join(ps.subpacksOf(u), " "))
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
		if d.tree == nil && d.commit != "" {
			// What addMeta makes of the first line of the producer's meta.
			lines = append(lines, d.commit+"  "+d.pkg.Name)
			continue
		}
		if d.tree == nil {
			lines = append(lines, shortID(d.id)+"  "+d.pkg.Name)
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
