package publisher

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Policy is what the project configuration says about publishing one prpa.
type Policy struct {
	// Publish is the target's flags.publish.
	Publish bool
	// Debuginfo is the target's flags.debuginfo: publish -debuginfo and
	// -debugsource packages into the debug sub-repository.
	Debuginfo bool
	// RPMMD is false when the target's repo_types do not contain rpm-md
	// (for example "none" for images): no repodata is generated.
	RPMMD bool
	// ExportFilter is the layered ExportFilter directive: regular expression
	// to the architectures a matching binary is exported to.
	ExportFilter map[string][]string
}

// PolicySource resolves the policy of a prpa.
type PolicySource interface {
	Policy(ctx context.Context, p buildrepo.Prpa) (Policy, error)
}

// ProjectGetter is the part of storage.Store the policy source needs.
type ProjectGetter interface {
	GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error)
}

// ManifestPolicy derives the policy from the project manifests: the target
// flags, repo_types of the target configuration and the layered project
// configuration for ExportFilter.
type ManifestPolicy struct {
	Projects ProjectGetter
}

// ErrUnknownTarget means the prpa has no matching project, target or
// architecture; there is nothing to publish.
var ErrUnknownTarget = errors.New("unknown project, repository or architecture")

// Policy implements PolicySource.
func (m ManifestPolicy) Policy(ctx context.Context, p buildrepo.Prpa) (Policy, error) {
	load := func(name string) (*manifest.ProjectManifest, error) {
		prj, err := m.Projects.GetProject(ctx, name)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return prj, err
	}
	prj, err := load(p.Project)
	if err != nil {
		return Policy{}, fmt.Errorf("load project %s: %w", p.Project, err)
	}
	if prj == nil {
		return Policy{}, fmt.Errorf("%w: %s", ErrUnknownTarget, p)
	}
	tgt, ok := prj.Targets[p.Repo]
	if !ok || !containsString(tgt.Architectures, p.Arch) {
		return Policy{}, fmt.Errorf("%w: %s", ErrUnknownTarget, p)
	}
	pol := Policy{Publish: tgt.Flags.Publish, Debuginfo: tgt.Flags.Debuginfo, RPMMD: tgt.Config.HasRepoType("rpm-md")}
	if !pol.Publish || !pol.RPMMD {
		return pol, nil
	}
	path, err := buildconfig.PathFromTarget(prj, p.Repo)
	if err != nil {
		return Policy{}, err
	}
	schedText, _, err := buildconfig.Layer(prj.Project.Name, p.Repo, path, load)
	if err != nil {
		return Policy{}, err
	}
	cfg, err := buildconfig.Parse(schedText, p.Arch)
	if err != nil {
		return Policy{}, fmt.Errorf("project configuration of %s: %w", p, err)
	}
	pol.ExportFilter = cfg.ExportFilter
	return pol, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Binary is one RPM selected for publication.
type Binary struct {
	bininfo.BinInfo
	// Source is the S3 key of the RPM below artifacts/.
	Source string
}

// classifyBinary sorts a binary into its sub-repository by header data, not
// by file name: source packages by architecture, debug packages by the
// -debuginfo and -debugsource name suffixes.
func classifyBinary(b bininfo.BinInfo) SubRepoType {
	switch {
	case b.Arch == "src" || b.Arch == "nosrc":
		return SubRepoSource
	case strings.HasSuffix(b.Name, "-debuginfo") || strings.HasSuffix(b.Name, "-debugsource"):
		return SubRepoDebug
	default:
		return SubRepoStandard
	}
}

// exportRouter compiles ExportFilter for the own architecture with the OBS
// semantics (first matching rule in reverse text order decides). A binary
// stays in the own repository if the own architecture is listed in its rule
// or no rule matches. Binaries exported to other architectures are integrated
// into those build repositories by the indexer. An empty filter keeps all
// (nil router).
func exportRouter(filter map[string][]string, arch string) (*buildconfig.ExportRouter, error) {
	if len(filter) == 0 {
		return nil, nil
	}
	return buildconfig.NewExportRouter(filter, arch)
}

func exportedToArch(r *buildconfig.ExportRouter, filename string) bool {
	if r == nil {
		return true
	}
	own, _ := r.Route(filename)
	return own
}

// Selection is the binary set to publish, per sub-repository, sorted by
// file name.
type Selection map[SubRepoType][]Binary

// selectBinaries applies the publication policy to the candidates: source
// packages are dropped (no source sub-repository is requested), debug
// packages only with flags.debuginfo, and ExportFilter for the own
// architecture. Two binaries with the same file name must be identical.
func selectBinaries(cands []Binary, pol Policy, arch string) (Selection, error) {
	sel := Selection{}
	seen := map[string]string{}
	router, err := exportRouter(pol.ExportFilter, arch)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		sr := classifyBinary(c.BinInfo)
		if sr == SubRepoSource || (sr == SubRepoDebug && !pol.Debuginfo) {
			continue
		}
		if !isValidPathSegment(c.Filename) || !strings.HasSuffix(c.Filename, ".rpm") {
			return nil, fmt.Errorf("invalid binary file name %q", c.Filename)
		}
		if !exportedToArch(router, c.Filename) {
			continue
		}
		if sha, dup := seen[c.Filename]; dup {
			if sha != c.SHA256 {
				return nil, fmt.Errorf("two different binaries are named %s", c.Filename)
			}
			continue
		}
		seen[c.Filename] = c.SHA256
		sel[sr] = append(sel[sr], c)
	}
	for _, list := range sel {
		sort.Slice(list, func(i, j int) bool { return list[i].Filename < list[j].Filename })
	}
	return sel, nil
}
