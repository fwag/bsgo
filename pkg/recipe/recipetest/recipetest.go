// Package recipetest holds helpers shared by the recipe golden tests and the
// Perl oracle test: loading the layered project configuration and enumerating
// the units (package or package:flavor) of a spec corpus laid out as
// <dir>/<package>/{*.spec,_multibuild}.
package recipetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/recipe"
)

// Result is the normalized, comparable view of a parsed recipe. It contains
// the fields the scheduler consumes and that the Perl oracle reports.
type Result struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Release     string   `json:"release"`
	Epoch       string   `json:"epoch"`
	Deps        []string `json:"deps"`
	Subpacks    []string `json:"subpacks"`
	ExclArch    []string `json:"exclarch"`
	BadArch     []string `json:"badarch"`
	BuildTarget string   `json:"buildtarget"`
	Constraints []string `json:"constraints"`
	Error       string   `json:"error"`
}

// FromInfo normalizes an Info; absent lists become empty ones.
func FromInfo(i *recipe.Info) Result {
	return Result{
		Name: i.Name, Version: i.Version, Release: i.Release, Epoch: i.Epoch,
		Deps: nz(i.Deps), Subpacks: nz(i.Subpacks), ExclArch: nz(i.ExclArch),
		BadArch: nz(i.BadArch), BuildTarget: i.BuildTarget,
		Constraints: nz(i.Constraints), Error: i.Error,
	}
}

func nz(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// LoadConfig layers prjconf text like getconfig does for a project that is
// its own only path element (header defines, _is_this_project 1) and parses
// the worker variant, which keeps the Macros: blocks, as OBS hands it to
// Build::parse. It returns the config and the layered text.
func LoadConfig(prjconfPath, project, repo, arch string) (*buildconfig.Config, string, error) {
	raw, err := os.ReadFile(filepath.Clean(prjconfPath))
	if err != nil {
		return nil, "", err
	}
	_, worker := buildconfig.LayerTexts(project, []buildconfig.Source{{Project: project, Repo: repo, Text: string(raw)}})
	cfg, err := buildconfig.Parse(worker, arch)
	if err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", prjconfPath, err)
	}
	return cfg, worker, nil
}

// Case is one corpus unit.
type Case struct {
	recipe.Unit
	// Path is the recipe file on disk.
	Path string
}

// Cases enumerates the units of the corpus in dir, sorted by name. Units
// without a matching recipe are returned with an empty Path and RecipeFile.
func Cases(dir, repo string) ([]Case, error) {
	pkgs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, p := range pkgs {
		if !p.IsDir() {
			continue
		}
		cs, err := packageCases(filepath.Join(dir, p.Name()), p.Name(), repo)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func packageCases(pdir, pkg, repo string) ([]Case, error) {
	ents, err := os.ReadDir(pdir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range ents {
		files = append(files, e.Name())
	}
	mb, err := readMultibuild(pdir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pkg, err)
	}
	var out []Case
	for _, u := range recipe.Expand(pkg, files, mb, repo) {
		c := Case{Unit: u}
		if u.RecipeFile != "" {
			c.Path = filepath.Join(pdir, u.RecipeFile)
		}
		out = append(out, c)
	}
	return out, nil
}

// readMultibuild returns the parsed _multibuild of a package directory, or nil
// when there is none.
func readMultibuild(pdir string) (*recipe.Multibuild, error) {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(pdir, recipe.MultibuildFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return recipe.ParseMultibuild(data)
}
