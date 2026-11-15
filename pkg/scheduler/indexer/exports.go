package indexer

import (
	"context"
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// ProjectGetter is the part of storage.Store ManifestExports needs.
type ProjectGetter interface {
	GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error)
}

// ManifestExports derives the export policy from the project manifests: the
// layered project configuration parsed for the building architecture, and the
// architectures of the target.
type ManifestExports struct {
	Projects ProjectGetter
}

// Exports implements ExportSource. A prpa whose project or target is gone
// exports nothing.
func (m ManifestExports) Exports(ctx context.Context, p buildrepo.Prpa) (ExportPolicy, error) {
	load := func(name string) (*manifest.ProjectManifest, error) {
		prj, err := m.Projects.GetProject(ctx, name)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return prj, err
	}
	prj, err := load(p.Project)
	if err != nil {
		return ExportPolicy{}, fmt.Errorf("load project %s: %w", p.Project, err)
	}
	if prj == nil {
		return ExportPolicy{}, nil
	}
	tgt, ok := prj.Targets[p.Repo]
	if !ok {
		return ExportPolicy{}, nil
	}
	path, err := buildconfig.PathFromTarget(prj, p.Repo)
	if err != nil {
		return ExportPolicy{}, err
	}
	schedText, _, err := buildconfig.Layer(prj.Project.Name, p.Repo, path, load)
	if err != nil {
		return ExportPolicy{}, err
	}
	cfg, err := buildconfig.Parse(schedText, p.Arch)
	if err != nil {
		return ExportPolicy{}, fmt.Errorf("project configuration of %s: %w", p, err)
	}
	router, err := buildconfig.NewExportRouter(cfg.ExportFilter, p.Arch)
	if err != nil {
		return ExportPolicy{}, err
	}
	return ExportPolicy{Router: router, Archs: tgt.Architectures}, nil
}
