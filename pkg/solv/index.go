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
	units := make([]UnitBinary, len(infos))
	for i, b := range infos {
		units[i] = UnitBinary{BinInfo: b}
	}
	return BuildUnitSolv(arch, units)
}

// UnitBinary is a binary together with the package unit ("pkg" or
// "pkg:flavor") that produced it.
type UnitBinary struct {
	Unit string
	bininfo.BinInfo
}

// BuildUnitSolv is BuildSolv for binaries that carry their unit, so that
// UpdateSolv can later replace the binaries of one unit. It is the full
// rebuild (repair) path.
func BuildUnitSolv(arch string, bins []UnitBinary) ([]byte, error) {
	if arch == "" {
		return nil, fmt.Errorf("architecture is required to build a solv index")
	}
	pool, err := NewPool(arch)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	repo := pool.CreateRepo("bininfo")
	if err := repo.addSorted(bins); err != nil {
		return nil, err
	}
	return repo.finishAndDump()
}

// UpdateSolv replaces the binaries of unit in the solv blob current (nil or
// empty for an empty repository) with add and returns the new blob. Only the
// replaced unit is touched, so the cost does not depend on how many other
// units the repository holds. The unit of every solvable is stored in the
// string attribute "bsgo:unit" (a custom libsolv key, see c_bridge.c); the
// unit's solvables are removed with repo_free_solvable and the new ones are
// appended, so the result has the same provides and requires per package as
// BuildUnitSolv over the resulting binary set, but not necessarily the same
// solvable order.
func UpdateSolv(arch string, current []byte, unit string, add []bininfo.BinInfo) ([]byte, error) {
	if arch == "" {
		return nil, fmt.Errorf("architecture is required to update a solv index")
	}
	if unit == "" {
		return nil, fmt.Errorf("unit is required to update a solv index")
	}
	pool, err := NewPool(arch)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	repo, err := loadOrCreate(pool, current)
	if err != nil {
		return nil, err
	}
	cUnit := C.CString(unit)
	defer C.free(unsafe.Pointer(cUnit))
	pool.mu.Lock()
	C.bsgo_repo_remove_unit(repo.ptr, cUnit)
	pool.mu.Unlock()
	bins := make([]UnitBinary, len(add))
	for i, b := range add {
		bins[i] = UnitBinary{Unit: unit, BinInfo: b}
	}
	if err := repo.addSorted(bins); err != nil {
		return nil, err
	}
	return repo.finishAndDump()
}

func loadOrCreate(pool *Pool, current []byte) (*Repo, error) {
	if len(current) == 0 {
		return pool.CreateRepo("bininfo"), nil
	}
	return pool.LoadRepoFromMemory("bininfo", current)
}

func (r *Repo) finishAndDump() ([]byte, error) {
	r.pool.mu.Lock()
	C.bsgo_repo_finish(r.ptr)
	r.pool.mu.Unlock()
	return r.DumpMemory()
}

// addSorted adds the binary packages of bins in a canonical order.
func (r *Repo) addSorted(bins []UnitBinary) error {
	sorted := append([]UnitBinary(nil), bins...)
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
	for _, b := range sorted {
		if b.Arch == "src" || b.Arch == "nosrc" {
			continue
		}
		if err := r.addBinInfo(b.Unit, b.BinInfo); err != nil {
			return fmt.Errorf("indexing %s: %w", b.Filename, err)
		}
	}
	return nil
}

func (r *Repo) addBinInfo(unit string, info bininfo.BinInfo) error {
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

	if unit != "" {
		cUnit := C.CString(unit)
		rc := C.bsgo_solvable_set_unit(r.ptr, sid, cUnit)
		C.free(unsafe.Pointer(cUnit))
		if rc != 0 {
			return fmt.Errorf("cannot set unit")
		}
	}
	self := bininfo.Dep{Name: info.Name, Op: "=", EVR: info.EVR()}
	if err := r.addDeps(sid, addProvides, append([]bininfo.Dep{self}, info.Provides...)); err != nil {
		return err
	}
	for _, f := range info.Files {
		if !bininfo.IsPrimaryFile(f) {
			continue
		}
		cf := C.CString(f)
		rc := C.bsgo_solvable_add_file(r.ptr, sid, cf)
		C.free(unsafe.Pointer(cf))
		if rc != 0 {
			return fmt.Errorf("cannot add file %q", f)
		}
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
		cn := C.CString(d.Name)
		co := C.CString(d.Op)
		ce := C.CString(d.EVR)
		pre := C.int(0)
		if d.Pre {
			pre = 1
		}
		rc := C.bsgo_solvable_add_dep(r.ptr, sid, C.int(kind), cn, co, ce, pre)
		C.free(unsafe.Pointer(cn))
		C.free(unsafe.Pointer(co))
		C.free(unsafe.Pointer(ce))
		if rc != 0 {
			return fmt.Errorf("cannot parse dependency %q", d.String())
		}
	}
	return nil
}

// AllRequires returns every requirement of every package in the pool as
// sorted "package: dependency" lines, for comparing indexes.
func (p *Pool) AllRequires() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := C.bsgo_all_requires(p.ptr)
	if out == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(out))
	text := strings.TrimSuffix(C.GoString(out), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	sort.Strings(lines)
	return lines
}

// AllProvides returns every provide of every package in the pool, including
// file provides, as sorted "package: dependency" lines. It exists to compare
// indexes built from different sources.
func (p *Pool) AllProvides() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := C.bsgo_all_provides(p.ptr)
	if out == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(out))
	lines := strings.Split(strings.TrimSuffix(C.GoString(out), "\n"), "\n")
	sort.Strings(lines)
	return lines
}
