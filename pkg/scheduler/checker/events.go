package checker

import (
	"context"
	"fmt"
	"sort"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

// Marker records that a prpa must be checked again. coord.Coordinator
// satisfies it.
type Marker interface {
	Mark(ctx context.Context, prpa string) error
}

// ProjectLister lists the projects whose prpas can be affected by an event.
// storage.Store satisfies it.
type ProjectLister interface {
	ListProjects(ctx context.Context) ([]*manifest.ProjectManifest, error)
}

// Events translates state changes into dirty prpas: package manifest changes,
// project configuration changes, new binaries in a build repository (which
// dirty every prpa that has it in its path) and finished jobs.
type Events struct {
	Projects ProjectLister
	Marker   Marker
}

// Prpas returns every prpa defined by the projects, sorted.
func (e *Events) Prpas(ctx context.Context) ([]Prpa, error) {
	prjs, err := e.Projects.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var out []Prpa
	for _, prj := range prjs {
		for repo, t := range prj.Targets {
			for _, arch := range t.Architectures {
				out = append(out, Prpa{Project: prj.Project.Name, Repo: repo, Arch: arch})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func (e *Events) mark(ctx context.Context, prpas []Prpa) error {
	for _, p := range prpas {
		if err := e.Marker.Mark(ctx, p.String()); err != nil {
			return err
		}
	}
	return nil
}

// All marks every prpa, for example at startup.
func (e *Events) All(ctx context.Context) error {
	all, err := e.Prpas(ctx)
	if err != nil {
		return err
	}
	return e.mark(ctx, all)
}

// PackageChanged marks every prpa of the project: a changed manifest can
// change any repository or architecture.
func (e *Events) PackageChanged(ctx context.Context, project string) error {
	return e.mark(ctx, e.inProject(ctx, project))
}

// ProjectConfigChanged marks the prpas of the project and of every project
// that has it in a build path, since they layer its configuration.
func (e *Events) ProjectConfigChanged(ctx context.Context, project string) error {
	own := e.inProject(ctx, project)
	var all []Prpa
	for _, p := range own {
		deps, err := e.Dependents(ctx, p)
		if err != nil {
			return err
		}
		all = append(all, deps...)
	}
	return e.mark(ctx, all)
}

// BinariesIntegrated marks the prpa itself and every prpa that has it in its
// path.
func (e *Events) BinariesIntegrated(ctx context.Context, p Prpa) error {
	deps, err := e.Dependents(ctx, p)
	if err != nil {
		return err
	}
	return e.mark(ctx, deps)
}

// JobFinished marks the prpa of a finished job.
func (e *Events) JobFinished(ctx context.Context, p Prpa) error {
	return e.mark(ctx, []Prpa{p})
}

func (e *Events) inProject(ctx context.Context, project string) []Prpa {
	all, err := e.Prpas(ctx)
	if err != nil {
		return nil
	}
	var out []Prpa
	for _, p := range all {
		if p.Project == project {
			out = append(out, p)
		}
	}
	return out
}

// Dependents returns p and every prpa of the same architecture that lists
// p's repository in its build path, sorted.
func (e *Events) Dependents(ctx context.Context, p Prpa) ([]Prpa, error) {
	prjs, err := e.Projects.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := []Prpa{p}
	for _, prj := range prjs {
		for repo, t := range prj.Targets {
			if !hasArch(t.Architectures, p.Arch) || (prj.Project.Name == p.Project && repo == p.Repo) {
				continue
			}
			path, err := buildconfig.PathFromTarget(prj, repo)
			if err != nil {
				continue // a malformed target is reported by its own pass
			}
			if pathHas(path, p) {
				out = append(out, Prpa{Project: prj.Project.Name, Repo: repo, Arch: p.Arch})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func pathHas(path []buildconfig.PathElem, p Prpa) bool {
	for _, el := range path {
		if el.URL == "" && el.Project == p.Project && el.Repo == p.Repo {
			return true
		}
	}
	return false
}
