package solv

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ParseRPMFilename extracts package name, EVR, and architecture from standard RPM filenames
// (e.g. "libreadline8-8.3.6-3.1.x86_64.rpm" -> Name: "libreadline8", EVR: "8.3.6-3.1", Arch: "x86_64").
func ParseRPMFilename(filename string) PackageSpec {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".rpm")

	lastDot := strings.LastIndex(base, ".")
	if lastDot == -1 {
		return PackageSpec{Name: base, EVR: "0", Arch: "noarch", Provides: []string{base}}
	}
	arch := base[lastDot+1:]
	nameVersion := base[:lastDot]

	lastDash := strings.LastIndex(nameVersion, "-")
	if lastDash == -1 {
		return PackageSpec{Name: nameVersion, EVR: "0", Arch: arch, Provides: []string{nameVersion}}
	}
	release := nameVersion[lastDash+1:]
	nameAndVer := nameVersion[:lastDash]

	secondLastDash := strings.LastIndex(nameAndVer, "-")
	if secondLastDash == -1 {
		return PackageSpec{Name: nameAndVer, EVR: release, Arch: arch, Provides: []string{nameAndVer}}
	}
	name := nameAndVer[:secondLastDash]
	version := nameAndVer[secondLastDash+1:]
	evr := fmt.Sprintf("%s-%s", version, release)

	provides := []string{
		name,
		fmt.Sprintf("%s = %s", name, evr),
	}

	return PackageSpec{
		Name:     name,
		EVR:      evr,
		Arch:     arch,
		Provides: provides,
	}
}

// MergeAndDumpSolv merges new package specifications into an existing .solv binary blob (or creates a new one).
func MergeAndDumpSolv(existingBlob []byte, arch, repoName string, packages []PackageSpec) ([]byte, error) {
	if arch == "" {
		arch = "x86_64"
	}
	if repoName == "" {
		repoName = "repo"
	}

	pool, err := NewPool(arch)
	if err != nil {
		return nil, fmt.Errorf("failed creating pool for %s: %w", arch, err)
	}
	defer pool.Close()

	var repo *Repo
	if len(existingBlob) > 0 {
		repo, err = pool.LoadRepoFromMemory(repoName, existingBlob)
		if err != nil {
			repo = pool.CreateRepo(repoName)
		}
	} else {
		repo = pool.CreateRepo(repoName)
	}

	for _, pkg := range packages {
		if pkg.Name == "" {
			continue
		}
		if pkg.Arch == "" {
			pkg.Arch = arch
		}
		if err := repo.AddPackage(pkg); err != nil {
			return nil, fmt.Errorf("failed adding package %s: %w", pkg.Name, err)
		}
	}

	return repo.DumpMemory()
}
