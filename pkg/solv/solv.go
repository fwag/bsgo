package solv

/*
#cgo LDFLAGS: -lsolv
#include "c_bridge.h"
*/
import "C"

import (
	"fmt"
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

	ret := C.bsgo_add_package(
		r.ptr, cName, cEVR, cArch,
		C.int(len(spec.Provides)), cProvides,
		C.int(len(spec.Requires)), cRequires,
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

	data := C.GoBytes(outBuf, C.int(outLen))
	return data, nil
}

func (p *Pool) Resolve(req ExpansionRequest) (*ExpansionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cReqs, freeReqs := makeCStringArray(req.BuildRequires)
	defer freeReqs()
	cPrefers, freePrefers := makeCStringArray(req.Prefer)
	defer freePrefers()

	outcome := C.bsgo_expand(
		p.ptr,
		C.int(len(req.BuildRequires)), cReqs,
		C.int(len(req.Prefer)), cPrefers,
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
