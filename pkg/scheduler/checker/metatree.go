package checker

import (
	"fmt"
	"sort"
	"strings"
)

// The meta of a build is a tree flattened into lines, like the .meta files of
// OBS (BSSched/BuildJob/Package.pm, BSBuild.pm):
//
//	<id>  <path>
//
// The first line is "<source commit>  <unit>". Every other line names a build
// dependency: path is the binary name for a direct dependency, and
// "<binary>/<dependency of that binary>/..." for the dependencies a local
// producer recorded in its own meta when it was built. id identifies the
// binary (sha256 of the file) or, for a local producer, its source commit.
//
// Because a local dependency contributes the meta of its producer instead of
// the identity of its freshly built files, a rebuild with unchanged inputs
// changes nothing, and dependency cycles can be cut: genMeta removes every
// line that leads back through the package's own binaries.

// metaIDLen is the number of hex digits of a binary sha256 kept in a meta
// line; 128 bits keep the lines short and collisions out of reach.
const metaIDLen = 32

// shortID returns the meta line id of a sha256.
func shortID(sum string) string {
	if len(sum) > metaIDLen {
		return sum[:metaIDLen]
	}
	return sum
}

// addMeta appends the meta of the local dependency bin to dst and returns it
// (BSBuild::add_meta): the lines of the producer's meta get bin as extra path
// prefix, and the producer's first line, which names its package, is
// rewritten to name the binary. The meta of a package that is packid itself
// is dropped: our own build results never count.
func addMeta(dst, producer []string, bin, packid string) []string {
	start := len(dst)
	for _, l := range producer {
		dst = append(dst, strings.Replace(l, "  ", "  "+bin+"/", 1))
	}
	if len(dst) == start {
		return dst
	}
	if strings.HasSuffix(dst[start], "/"+packid) {
		return dst[:start]
	}
	if i := strings.Index(dst[start], "  "); i >= 0 {
		dst[start] = dst[start][:i+2] + bin
	}
	return dst
}

// metaDep is one parsed dependency line.
type metaDep struct {
	line  string
	slash int    // number of '/' in the line: the depth in the tree
	path  string // everything after the id
	key   string // id and last path element: the identity of the dependency
	// head is the first path element: the direct dependency the line stems from.
	head string
}

func parseMetaDep(line string) (metaDep, error) {
	i := strings.Index(line, "  ")
	if i <= 0 || strings.ContainsRune(line[:i], ' ') {
		return metaDep{}, fmt.Errorf("bad dependency line: %q", line)
	}
	path := line[i+2:]
	head := path
	if j := strings.IndexByte(path, '/'); j >= 0 {
		head = path[:j]
	}
	last := path[strings.LastIndexByte(path, '/')+1:]
	return metaDep{
		line: line, slash: strings.Count(line, "/"), path: path,
		key: line[:i+2] + last, head: head,
	}, nil
}

// anyElem reports whether any path element, starting at element skip, is in
// set.
func (d metaDep) anyElem(set map[string]bool, skip int) bool {
	rest := d.path
	for n := 0; ; n++ {
		elem, tail, more := strings.Cut(rest, "/")
		if n >= skip && set[elem] {
			return true
		}
		if !more {
			return false
		}
		rest = tail
	}
}

// through reports whether any path element after the first is in set, which
// is the "/name/" pattern match of BSBuild::gen_meta on "line/".
func (d metaDep) through(set map[string]bool) bool { return d.anyElem(set, 1) }

// contains reports whether any path element is in set.
func (d metaDep) contains(set map[string]bool) bool { return d.anyElem(set, 0) }

// genMeta ports BSBuild::gen_meta. deps are the dependency lines of a
// package whose own binaries are subpacks. The result is sorted by depth and
// path, holds every (id, binary) once at its shallowest position and has the
// cycles through subpacks removed:
//   - lines that lead through a subpack are the dependencies of a dependency
//     that was built against our own binaries: they are dropped;
//   - algo 0 additionally drops every line that goes through one of the
//     direct dependencies that depend on us;
//   - algo 1 drops the copies of a dependency that sit deeper in the tree than
//     a copy reached through such a dependency.
func genMeta(subpacks, deps []string, algo int) ([]string, error) {
	own := make(map[string]bool, len(subpacks))
	for _, s := range subpacks {
		own[s] = true
	}
	parsed, cycle, err := parseDeps(deps, own)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(parsed, func(i, j int) bool {
		a, b := parsed[i], parsed[j]
		if a.slash != b.slash {
			return a.slash < b.slash
		}
		if a.path != b.path {
			return a.path < b.path
		}
		return a.line < b.line
	})
	// Self-cycles are not cycles.
	for s := range own {
		delete(cycle, s)
	}
	cycSeen := map[string]int{}
	if len(cycle) > 0 {
		parsed, cycSeen = cutCycles(parsed, cycle, algo)
	}
	return pruneDeps(parsed, own, cycSeen), nil
}

// parseDeps parses the lines and collects the direct dependencies that have a
// subpack somewhere below them.
func parseDeps(deps []string, own map[string]bool) ([]metaDep, map[string]bool, error) {
	parsed := make([]metaDep, 0, len(deps))
	cycle := map[string]bool{}
	for _, l := range deps {
		d, err := parseMetaDep(l)
		if err != nil {
			return nil, nil, err
		}
		if len(own) > 0 && d.contains(own) {
			cycle[d.head] = true
		}
		parsed = append(parsed, d)
	}
	return parsed, cycle, nil
}

// cutCycles applies the genmetaalgo specific part of gen_meta to the sorted
// lines. It returns the remaining lines and, for algo 1, the depth at which a
// dependency was seen through a cycle member.
func cutCycles(sorted []metaDep, cycle map[string]bool, algo int) ([]metaDep, map[string]int) {
	if algo == 0 {
		kept := sorted[:0:0]
		for _, d := range sorted {
			if !d.through(cycle) {
				kept = append(kept, d)
			}
		}
		return kept, nil
	}
	seen := map[string]int{}
	for _, d := range sorted {
		if !d.through(cycle) {
			continue
		}
		if _, ok := seen[d.key]; !ok {
			seen[d.key] = d.slash
		}
	}
	return sorted, seen
}

// pruneDeps keeps the first line of every dependency identity, drops the lines
// that lead through an own binary and applies the algo 1 cycle depth cut.
func pruneDeps(sorted []metaDep, own map[string]bool, cycSeen map[string]int) []string {
	seen := map[string]bool{}
	var kept []metaDep
	for _, d := range sorted {
		if seen[d.key] || (len(own) > 0 && d.contains(own)) {
			continue
		}
		seen[d.key] = true
		kept = append(kept, d)
	}
	out := make([]string, 0, len(kept))
	for _, d := range kept {
		if depth, ok := cycSeen[d.key]; ok && d.slash >= depth {
			continue
		}
		out = append(out, d.line)
	}
	return out
}

// diffDepth ports BSBuild::diffdepth: the depth at which two metas differ.
// 0 means equal, 1 a different first line (the source), and n+2 a difference
// in a line with n slashes.
func diffDepth(m1, m2 []string) int {
	short, long := m1, m2
	if len(m1) > len(m2) {
		short, long = m2, m1
	}
	for i, l := range short {
		if l == long[i] {
			continue
		}
		if i == 0 {
			return 1
		}
		return min(strings.Count(l, "/"), strings.Count(long[i], "/")) + 2
	}
	if len(short) == len(long) {
		return 0
	}
	if len(short) == 0 {
		return 1
	}
	return strings.Count(long[len(short)], "/") + 2
}
