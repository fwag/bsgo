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

// NameConflict records that several units produced different binaries under
// one file name. Only one of them can be published.
type NameConflict struct {
	Filename string
	// Kept is the S3 key of the published binary, Dropped the keys of the
	// binaries that lost.
	Kept    string
	Dropped []string
}

// selectBinaries applies the publication policy to the candidates: source
// packages are dropped (no source sub-repository is requested), debug
// packages only with flags.debuginfo, and ExportFilter for the own
// architecture.
//
// Several units can build a binary with the same file name (for example the
// kernel-livepatch units of SUSE:SLFO). Like OBS, which lets a later binary
// replace an earlier one of the same name, this never fails the repository:
// the candidates are ordered by their artifact key (which starts with the
// unit name) and the last one wins. The order does not depend on the
// listing order of the index, so repeated publishes choose the same binary
// and do not make the published file flap. Identical copies are no conflict.
func selectBinaries(cands []Binary, pol Policy, arch string) (Selection, []NameConflict, error) {
	router, err := exportRouter(pol.ExportFilter, arch)
	if err != nil {
		return nil, nil, err
	}
	ordered := append([]Binary(nil), cands...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Source < ordered[j].Source })
	chosen := map[string]Binary{}
	dropped := map[string][]string{}
	for _, c := range ordered {
		sr := classifyBinary(c.BinInfo)
		if sr == SubRepoSource || (sr == SubRepoDebug && !pol.Debuginfo) {
			continue
		}
		if !isValidPathSegment(c.Filename) || !strings.HasSuffix(c.Filename, ".rpm") {
			return nil, nil, fmt.Errorf("invalid binary file name %q", c.Filename)
		}
		if !exportedToArch(router, c.Filename) {
			continue
		}
		if prev, dup := chosen[c.Filename]; dup && prev.SHA256 != c.SHA256 {
			dropped[c.Filename] = append(dropped[c.Filename], prev.Source)
		}
		chosen[c.Filename] = c
	}
	sel := Selection{}
	for _, c := range chosen {
		sr := classifyBinary(c.BinInfo)
		sel[sr] = append(sel[sr], c)
	}
	for _, list := range sel {
		sort.Slice(list, func(i, j int) bool { return list[i].Filename < list[j].Filename })
	}
	return sel, nameConflicts(chosen, dropped), nil
}

func nameConflicts(chosen map[string]Binary, dropped map[string][]string) []NameConflict {
	out := make([]NameConflict, 0, len(dropped))
	for name, keys := range dropped {
		out = append(out, NameConflict{Filename: name, Kept: chosen[name].Source, Dropped: keys})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	return out
}
