package solv

/*
#cgo LDFLAGS: -lsolv
#include "c_bridge.h"
*/
import "C"

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"unsafe"
)

type NoProviderError struct {
	Requirement string
}

func (e *NoProviderError) Error() string {
	return fmt.Sprintf("no provider found for requirement: %s", e.Requirement)
}

type AmbiguityError struct {
	Requirement string
}

func (e *AmbiguityError) Error() string {
	return fmt.Sprintf("multiple conflicting providers found for requirement: %s", e.Requirement)
}

type PackageSpec struct {
	Name     string
	EVR      string
	Arch     string
	Provides []string
	Requires []string
}

type ResolvedPackage struct {
	Name string
	EVR  string
	Arch string
}

type ExpansionRequest struct {
	BuildRequires []string
	Prefer        []string
	Architecture  string
}

type ExpansionResult struct {
	Packages []ResolvedPackage
}

type Pool struct {
	mu  sync.Mutex
	ptr *C.Pool
}

type Repo struct {
	mu   sync.Mutex
	ptr  *C.Repo
	pool *Pool
}

func NewPool(arch string) (*Pool, error) {
	cArch := C.CString(arch)
	defer C.free(unsafe.Pointer(cArch))

	p := C.bsgo_pool_create(cArch)
	if p == nil {
		return nil, fmt.Errorf("failed to allocate libsolv pool")
	}
	return &Pool{ptr: p}, nil
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ptr != nil {
		C.bsgo_pool_free(p.ptr)
		p.ptr = nil
	}
}

func (p *Pool) CreateRepo(name string) *Repo {
	p.mu.Lock()
	defer p.mu.Unlock()

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	r := C.bsgo_repo_create(p.ptr, cName)
	return &Repo{ptr: r, pool: p}
}

func (p *Pool) LoadRepoFromMemory(name string, data []byte) (*Repo, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot load empty solv data")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	r := C.bsgo_load_repo_from_mem(p.ptr, cName, unsafe.Pointer(&data[0]), C.size_t(len(data)))
	if r == nil {
		return nil, fmt.Errorf("failed to parse solv blob into repository")
	}
	return &Repo{ptr: r, pool: p}, nil
}

func (r *Repo) AddPackage(spec PackageSpec) error {
	r.pool.mu.Lock()
	defer r.pool.mu.Unlock()

	cName := C.CString(spec.Name)
	defer C.free(unsafe.Pointer(cName))
	cEVR := C.CString(spec.EVR)
	defer C.free(unsafe.Pointer(cEVR))

	var cArch *C.char
	if spec.Arch != "" {
		cArch = C.CString(spec.Arch)
		defer C.free(unsafe.Pointer(cArch))
	}

	cProvides, freeProvides := makeCStringArray(spec.Provides)
	defer freeProvides()
	cRequires, freeRequires := makeCStringArray(spec.Requires)
	defer freeRequires()

	nProvides := len(spec.Provides)
	if nProvides > math.MaxInt32 {
		return fmt.Errorf("provides count exceeds max int32")
	}
	nRequires := len(spec.Requires)
	if nRequires > math.MaxInt32 {
		return fmt.Errorf("requires count exceeds max int32")
	}

	// #nosec G115 -- slice length is bounded by math.MaxInt32
	cNumProvides := C.int(int32(nProvides))
	// #nosec G115 -- slice length is bounded by math.MaxInt32
	cNumRequires := C.int(int32(nRequires))

	ret := C.bsgo_add_package(
		r.ptr, cName, cEVR, cArch,
		cNumProvides, cProvides,
		cNumRequires, cRequires,
	)
	if ret != 0 {
		return fmt.Errorf("failed to add package %s to repo", spec.Name)
	}
	return nil
}

func (r *Repo) DumpMemory() ([]byte, error) {
	r.pool.mu.Lock()
	defer r.pool.mu.Unlock()

	var outBuf unsafe.Pointer
	var outLen C.size_t

	ret := C.bsgo_dump_repo_to_mem(r.ptr, &outBuf, &outLen)
	if ret != 0 || outBuf == nil {
		return nil, fmt.Errorf("failed to dump solv memory")
	}
	defer C.free(outBuf)

	if outLen > math.MaxInt32 {
		return nil, fmt.Errorf("dump size exceeds max int32")
	}

	// #nosec G115 -- outLen is bounded by math.MaxInt32
	cLen := C.int(int32(outLen))
	data := C.GoBytes(outBuf, cLen)
	return data, nil
}

func (p *Pool) Resolve(req ExpansionRequest) (*ExpansionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cReqs, freeReqs := makeCStringArray(req.BuildRequires)
	defer freeReqs()
	cPrefers, freePrefers := makeCStringArray(req.Prefer)
	defer freePrefers()

	nReqs := len(req.BuildRequires)
	if nReqs > math.MaxInt32 {
		return nil, fmt.Errorf("build requires count exceeds max int32")
	}
	nPrefers := len(req.Prefer)
	if nPrefers > math.MaxInt32 {
		return nil, fmt.Errorf("prefer count exceeds max int32")
	}

	// #nosec G115 -- slice lengths are bounded by math.MaxInt32
	cNumReqs := C.int(int32(nReqs))
	// #nosec G115 -- slice lengths are bounded by math.MaxInt32
	cNumPrefers := C.int(int32(nPrefers))

	outcome := C.bsgo_expand(
		p.ptr,
		cNumReqs, cReqs,
		cNumPrefers, cPrefers,
	)
	defer C.bsgo_free_outcome(&outcome)

	switch outcome.status {
	case C.BSGO_SOLV_OK:
		return parseResolvedPackages(&outcome), nil
	case C.BSGO_SOLV_ERR_NOPROVIDER:
		detail := C.GoString(outcome.err_detail)
		return nil, &NoProviderError{Requirement: detail}
	case C.BSGO_SOLV_ERR_CHOICE:
		detail := C.GoString(outcome.err_detail)
		return nil, &AmbiguityError{Requirement: detail}
	default:
		return nil, fmt.Errorf("libsolv expansion failed with status %d", outcome.status)
	}
}

func parseResolvedPackages(outcome *C.ExpansionOutcome) *ExpansionResult {
	count := int(outcome.package_count)
	res := &ExpansionResult{
		Packages: make([]ResolvedPackage, count),
	}

	cPkgs := unsafe.Slice(outcome.packages, count)
	for i := 0; i < count; i++ {
		res.Packages[i] = ResolvedPackage{
			Name: C.GoString(cPkgs[i].name),
			EVR:  C.GoString(cPkgs[i].evr),
			Arch: C.GoString(cPkgs[i].arch),
		}
	}
	return res
}

func makeCStringArray(strs []string) (**C.char, func()) {
	if len(strs) == 0 {
		return nil, func() {}
	}

	cStrs := make([]*C.char, len(strs))
	for i, s := range strs {
		cStrs[i] = C.CString(s)
	}

	freeFunc := func() {
		for _, cs := range cStrs {
			C.free(unsafe.Pointer(cs))
		}
		runtime.KeepAlive(cStrs)
	}

	return (**C.char)(unsafe.Pointer(&cStrs[0])), freeFunc
}
