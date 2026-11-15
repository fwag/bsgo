package solv

/*
#include "c_bridge.h"
*/
import "C"

import (
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"github.com/openSUSE/bsgo/pkg/bininfo"
)

// Dependency kinds of C.bsgo_solvable_add_dep.
const (
	addProvides = iota
	addRequires
	addConflicts
	addObsoletes
	addRecommends
	addSuggests
	addSupplements
	addEnhances
)

// BuildSolv builds a libsolv repository blob from the bininfo of the current
// binary set. It carries the real provides, requires (with prerequisite
// markers), conflicts, obsoletes and the weak dependencies of each RPM, and
// stores the RPM file sha256 as SOLVABLE_CHECKSUM. Source RPMs are skipped.
// The result is deterministic for a given set of infos.
func BuildSolv(arch string, infos []bininfo.BinInfo) ([]byte, error) {
	if arch == "" {
		return nil, fmt.Errorf("architecture is required to build a solv index")
	}
	pool, err := NewPool(arch)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	repo := pool.CreateRepo("bininfo")

	sorted := append([]bininfo.BinInfo(nil), infos...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.EVR() != b.EVR() {
			return a.EVR() < b.EVR()
		}
		if a.Arch != b.Arch {
			return a.Arch < b.Arch
		}
		return a.Filename < b.Filename
	})
	for _, info := range sorted {
		if info.Arch == "src" || info.Arch == "nosrc" {
			continue
		}
		if err := repo.addBinInfo(info); err != nil {
			return nil, fmt.Errorf("indexing %s: %w", info.Filename, err)
		}
	}
	pool.mu.Lock()
	C.bsgo_repo_finish(repo.ptr)
	pool.mu.Unlock()
	return repo.DumpMemory()
}

func (r *Repo) addBinInfo(info bininfo.BinInfo) error {
	if info.Name == "" || info.Version == "" || info.Arch == "" {
		return fmt.Errorf("incomplete package identity %q", info.Name)
	}
	r.pool.mu.Lock()
	defer r.pool.mu.Unlock()
	r.pool.dirty = true

	cName := C.CString(info.Name)
	defer C.free(unsafe.Pointer(cName))
	cEVR := C.CString(info.EVR())
	defer C.free(unsafe.Pointer(cEVR))
	cArch := C.CString(info.Arch)
	defer C.free(unsafe.Pointer(cArch))
	sid := C.bsgo_repo_add_solvable(r.ptr, cName, cEVR, cArch)
	if sid == 0 {
		return fmt.Errorf("cannot add solvable")
	}

	self := bininfo.Dep{Name: info.Name, Op: "=", EVR: info.EVR()}
	if err := r.addDeps(sid, addProvides, append([]bininfo.Dep{self}, info.Provides...)); err != nil {
		return err
	}
	groups := []struct {
		kind int
		deps []bininfo.Dep
	}{
		{addRequires, info.Requires}, {addConflicts, info.Conflicts},
		{addObsoletes, info.Obsoletes}, {addRecommends, info.Recommends},
		{addSuggests, info.Suggests}, {addSupplements, info.Supplements},
		{addEnhances, info.Enhances},
	}
	for _, g := range groups {
		if err := r.addDeps(sid, g.kind, g.deps); err != nil {
			return err
		}
	}
	if info.SHA256 == "" {
		return nil
	}
	cSum := C.CString(info.SHA256)
	defer C.free(unsafe.Pointer(cSum))
	if C.bsgo_solvable_set_sha256(r.ptr, sid, cSum) != 0 {
		return fmt.Errorf("cannot set checksum")
	}
	return nil
}

func (r *Repo) addDeps(sid C.Id, kind int, deps []bininfo.Dep) error {
	for _, d := range deps {
		if kind == addRequires && strings.HasPrefix(d.Name, "rpmlib(") {
			continue
		}
		s := d.String()
		cs := C.CString(s)
		pre := C.int(0)
		if d.Pre {
			pre = 1
		}
		rc := C.bsgo_solvable_add_dep(r.ptr, sid, C.int(kind), cs, pre)
		C.free(unsafe.Pointer(cs))
		if rc != 0 {
			return fmt.Errorf("cannot parse dependency %q", s)
		}
	}
	return nil
}
