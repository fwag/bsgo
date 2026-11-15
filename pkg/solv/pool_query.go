package solv

/*
#include "c_bridge.h"
*/
import "C"

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unsafe"
)

// solvID identifies a solvable inside a prepared pool.
type solvID = int32

// depID identifies a libsolv dependency (plain name or relational dep).
type depID = int32

// Dependency kinds understood by solvableDeps.
const (
	depRequires = iota
	depConflicts
	depObsoletes
	depRecommends
	depSupplements
	depProvides // explicit provides, without primary.xml file entries
)

// libsolv relational dependency flags (pool.h).
const (
	relAnd     = 16
	relOr      = 17
	relWith    = 18
	relCond    = 22
	relElse    = 26
	relWithout = 28
	relUnless  = 29
	relCondA   = 30
	relMaxCmp  = 7
)

type solvInfo struct {
	name, evr, arch string
	checksum        string
	location        string
	repo            int
}

type depKey struct {
	s    solvID
	kind int
}

// queryCache memoizes libsolv lookups. It is only valid for one prepared state
// of a pool and is reset whenever the pool is prepared again.
type queryCache struct {
	str2dep  map[string]depID
	provides map[depID][]solvID
	info     map[solvID]*solvInfo
	deps     map[depKey][]depID
	depStr   map[depID]string
}

func newQueryCache() *queryCache {
	return &queryCache{
		str2dep:  map[string]depID{},
		provides: map[depID][]solvID{},
		info:     map[solvID]*solvInfo{},
		deps:     map[depKey][]depID{},
		depStr:   map[depID]string{},
	}
}

func gunzipIfNeeded(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed decompressing rpmmd gzip: %w", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("failed reading decompressed rpmmd: %w", err)
	}
	return out, nil
}

// prepareLocked (re)builds the shadowing map and the whatprovides index when
// repositories changed. The caller must hold p.mu.
func (p *Pool) prepareLocked() error {
	if p.ptr == nil {
		return fmt.Errorf("solver pool is closed")
	}
	if !p.dirty {
		return nil
	}
	if C.bsgo_pool_prepare(p.ptr) != 0 {
		return fmt.Errorf("failed to prepare libsolv pool")
	}
	p.cache = newQueryCache()
	p.dirty = false
	return nil
}

var opRe = regexp.MustCompile(`^(\S+?)\s*([<=>]{1,2})\s*(\S.*?)\s*$`)

// normalizeDepString canonicalizes "name op evr" spacing and operators the way
// OBS does (">>" is ">", "<<" is "<", "==" is "="). Rich deps pass through.
func normalizeDepString(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") {
		return s
	}
	m := opRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	op := m[2]
	switch op {
	case "==":
		op = "="
	case "<<":
		op = "<"
	case ">>":
		op = ">"
	}
	return m[1] + " " + op + " " + m[3]
}

func (p *Pool) str2dep(s string) depID {
	if id, ok := p.cache.str2dep[s]; ok {
		return id
	}
	cs := C.CString(normalizeDepString(s))
	defer C.free(unsafe.Pointer(cs))
	id := depID(C.bsgo_str2dep(p.ptr, cs))
	p.cache.str2dep[s] = id
	return id
}

func copyIDs(ptr *C.Id, n C.int) []int32 {
	if n <= 0 || ptr == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(ptr))
	src := unsafe.Slice((*int32)(unsafe.Pointer(ptr)), int(n))
	out := make([]int32, len(src))
	copy(out, src)
	return out
}

// rawProviders returns the considered solvables providing dep.
func (p *Pool) rawProviders(dep depID) []solvID {
	if dep == 0 {
		return nil
	}
	if r, ok := p.cache.provides[dep]; ok {
		return r
	}
	var out *C.Id
	n := C.bsgo_whatprovides(p.ptr, C.Id(dep), &out)
	r := copyIDs(out, n)
	p.cache.provides[dep] = r
	return r
}

func (p *Pool) solvInfo(s solvID) *solvInfo {
	if i, ok := p.cache.info[s]; ok {
		return i
	}
	var ci C.BsgoSolvInfo
	i := &solvInfo{repo: -1}
	if C.bsgo_solvable_info(p.ptr, C.Id(s), &ci) == 0 {
		i.name = C.GoString(ci.name)
		i.evr = C.GoString(ci.evr)
		i.arch = C.GoString(ci.arch)
		i.repo = int(ci.repo)
		if ci.sha256 != 0 {
			i.checksum = C.GoString(ci.checksum)
		}
		if ci.location != nil {
			i.location = C.GoString(ci.location)
		}
	}
	p.cache.info[s] = i
	return i
}

func (p *Pool) name(s solvID) string { return p.solvInfo(s).name }

func (p *Pool) solvableDeps(s solvID, kind int) []depID {
	k := depKey{s, kind}
	if r, ok := p.cache.deps[k]; ok {
		return r
	}
	var out *C.Id
	n := C.bsgo_solvable_deps(p.ptr, C.Id(s), C.int(kind), &out)
	r := copyIDs(out, n)
	p.cache.deps[k] = r
	return r
}

func (p *Pool) depString(d depID) string {
	if s, ok := p.cache.depStr[d]; ok {
		return s
	}
	s := C.GoString(C.bsgo_dep2str(p.ptr, C.Id(d)))
	p.cache.depStr[d] = s
	return s
}

// decompose splits a relational dep into its parts.
func (p *Pool) decompose(d depID) (name, evr depID, flags int, ok bool) {
	var cn, ce C.Id
	var cf C.int
	if C.bsgo_dep_decompose(p.ptr, C.Id(d), &cn, &ce, &cf) == 0 {
		return 0, 0, 0, false
	}
	return depID(cn), depID(ce), int(cf), true
}

// isComplex reports whether d is a rich (boolean) dependency.
func (p *Pool) isComplex(d depID) bool {
	_, _, f, ok := p.decompose(d)
	if !ok {
		return false
	}
	switch f {
	case relAnd, relOr, relWith, relCond, relWithout, relUnless, relCondA:
		return true
	}
	return false
}

// baseName returns the package or capability name of a simple dependency, the
// equivalent of OBS (split(/[ <=>]/, $r, 2))[0].
func (p *Pool) baseName(d depID) string {
	for {
		n, _, f, ok := p.decompose(d)
		if !ok {
			return C.GoString(C.bsgo_id2str(p.ptr, C.Id(d)))
		}
		if f > relMaxCmp {
			return strings.SplitN(p.depString(d), " ", 2)[0]
		}
		d = n
	}
}
