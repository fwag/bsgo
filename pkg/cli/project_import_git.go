package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/obsimport"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const importOBSGitLong = `Import an OBS project kept in a git superproject (for example SUSE:SLFO:1.2 in
products/SLFO on a Gitea forge) into a bsgo project directory:

  <out>/project.yaml                project manifest, converted exactly like
                                    "bsgo project convert-obs" (round-trip gate included)
  <out>/packages/xx/<pkg>.yaml      one hash-sharded manifest per submodule

Only git plumbing is used on --repo (rev-parse, ls-tree, show); the work tree
is never read or modified, so submodule directories may be empty.

Inputs read from --ref: the prjconf at --prjconf-path (default _config), .gitmodules, the
mode-160000 tree entries, and _meta when present. Without _meta in the ref, pass --meta.

Package manifests carry the git URL (relative .gitmodules URLs are resolved against
--gitea-base, or else the origin remote of --repo, with git semantics), the .gitmodules
branch (or --default-branch), and the full pinned commit id of the tree entry. They have no
recipe (auto-detected), no resources (nothing in the source defines them), and build
enabled. Packages listed in .gitmodules but absent from the tree, or the reverse, are
skipped with a diagnostic on stderr and listed in the --json report.

Bootstrap Dependency: --bootstrap-url with --bootstrap-name and --bootstrap-targets appends
a url dependency after the project's own path entries on each listed target. Remove it later
with "bsgo project dependency remove". No default target is assumed.

Apply the result to a control plane with "bsgo project apply <out>".
With --json, stdout carries one JSON document and nothing else.`

const importOBSGitExample = `  # Import SLFO 1.2 (the _meta is not in the repo, so pass it)
  bsgo project import-obs-git --repo ~/packages/SLFO --ref origin/slfo-1.2 \
    --meta SUSE_SLFO_1.2.meta.xml --out ./SLFO-bsgo

  # With a Bootstrap Dependency on SLFO 1.1 for the standard target
  bsgo project import-obs-git --repo ~/packages/SLFO --ref origin/slfo-1.2 --meta _meta --out ./out \
    --bootstrap-url http://builder:8088/ --bootstrap-name SUSE:SLFO:1.1/standard --bootstrap-targets standard

  # Check what would be generated, machine-readable
  bsgo project import-obs-git --repo ~/packages/SLFO --ref origin/slfo-1.2 --meta _meta --out ./out --dry-run --json`

type importGitFlags struct {
	repo, ref, out, meta, prjconfPath, giteaBase string
	defaultBranch                                string
	bootstrapURL, bootstrapName                  string
	bootstrapTargets                             []string
	verifyArches                                 []string
	dryRun, force, noVerify                      bool
}

type importGitOutput struct {
	Out      string                     `json:"out"`
	DryRun   bool                       `json:"dry_run"`
	Project  string                     `json:"project"`
	Verified bool                       `json:"verified"`
	Report   obsimport.Report           `json:"report"`
	Packages *obsimport.GitImportReport `json:"packages"`
}

func newProjectImportOBSGitCmd(opts *OutputOptions) *cobra.Command {
	var f importGitFlags
	cmd := &cobra.Command{
		Use:     "import-obs-git --repo <dir> --ref <ref> --out <dir>",
		Short:   "Import an OBS project from a git superproject into project.yaml and package manifests",
		Long:    importOBSGitLong,
		Example: importOBSGitExample,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runImportOBSGit(cmd, &f, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.repo, "repo", "", "git directory of the superproject (required)")
	fl.StringVar(&f.ref, "ref", "", "branch or commit to read, for example origin/slfo-1.2 (required)")
	fl.StringVar(&f.out, "out", "", "output directory (required)")
	fl.StringVar(&f.meta, "meta", "", "OBS _meta XML file; default: _meta from --ref")
	fl.StringVar(&f.prjconfPath, "prjconf-path", "_config", "path of the project configuration inside --ref")
	fl.StringVar(&f.giteaBase, "gitea-base", "", "forge root for relative submodule URLs, for example https://src.opensuse.org (default: origin remote of --repo)")
	fl.StringVar(&f.defaultBranch, "default-branch", "", "branch for submodules without a branch in .gitmodules (default: leave unset)")
	fl.StringVar(&f.bootstrapURL, "bootstrap-url", "", "URL of the Bootstrap Dependency repository")
	fl.StringVar(&f.bootstrapName, "bootstrap-name", "", "stable name of the Bootstrap Dependency, for example SUSE:SLFO:1.1/standard")
	fl.StringSliceVar(&f.bootstrapTargets, "bootstrap-targets", nil, "targets receiving the Bootstrap Dependency (required with --bootstrap-url)")
	fl.StringSliceVar(&f.verifyArches, "verify-arch", nil, "extra architectures for the round-trip gate")
	fl.BoolVar(&f.dryRun, "dry-run", false, "convert and verify, but write nothing")
	fl.BoolVar(&f.force, "force", false, "overwrite an existing project.yaml in --out")
	fl.BoolVar(&f.noVerify, "no-verify", false, "skip the round-trip gate (not recommended)")
	for _, name := range []string{"repo", "ref", "out"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func (f *importGitFlags) validate() error {
	if f.bootstrapURL == "" {
		if f.bootstrapName != "" || len(f.bootstrapTargets) > 0 {
			return errors.New("--bootstrap-name and --bootstrap-targets need --bootstrap-url")
		}
		return nil
	}
	if f.bootstrapName == "" || len(f.bootstrapTargets) == 0 {
		return errors.New("--bootstrap-url needs --bootstrap-name and --bootstrap-targets (there is no default target)")
	}
	return nil
}

func runImportOBSGit(cmd *cobra.Command, f *importGitFlags, opts *OutputOptions) error {
	if err := f.validate(); err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	src := &obsimport.GitSource{Dir: f.repo, Ref: f.ref}
	commit, err := src.Resolve(ctx)
	if err != nil {
		return err
	}
	prj, res, verified, err := importProject(ctx, src, f)
	if err != nil {
		return err
	}
	pkgs, rep, err := importPackages(ctx, src, f)
	if err != nil {
		return err
	}
	rep.Ref, rep.ResolvedCommit = f.ref, commit
	for _, s := range rep.Skipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s: %s\n", s.Package, s.Reason)
	}
	for _, p := range rep.MissingInTree {
		fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s: listed in .gitmodules but not in the tree\n", p)
	}
	for _, p := range rep.MissingInModules {
		fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s: in the tree but not in .gitmodules\n", p)
	}
	if !f.dryRun {
		if err := writeImportTree(f, prj, pkgs); err != nil {
			return err
		}
	}
	return reportImportGit(cmd, f, opts, &importGitOutput{
		Out: f.out, DryRun: f.dryRun, Project: prj.Project.Name, Verified: verified, Report: res.Report, Packages: rep,
	})
}

// importProject converts _meta and the prjconf read from the ref, runs the
// round-trip gate, then applies the Bootstrap Dependency.
func importProject(ctx context.Context, src *obsimport.GitSource, f *importGitFlags) (*manifest.ProjectManifest, *obsimport.Result, bool, error) {
	metaXML, err := readImportMeta(ctx, src, f)
	if err != nil {
		return nil, nil, false, err
	}
	conf, found, err := src.File(ctx, f.prjconfPath)
	if err != nil {
		return nil, nil, false, err
	}
	if !found {
		return nil, nil, false, fmt.Errorf("%s not found in %s", f.prjconfPath, f.ref)
	}
	meta, err := obsimport.ParseMeta(metaXML)
	if err != nil {
		return nil, nil, false, err
	}
	res, err := obsimport.Convert(meta, string(conf))
	if err != nil {
		return nil, nil, false, err
	}
	verified := false
	if !f.noVerify {
		if err := verifyConversion(res.Manifest, string(conf), meta, f.verifyArches); err != nil {
			return nil, nil, false, err
		}
		verified = true
	}
	if f.bootstrapURL != "" {
		if err := obsimport.AddBootstrapDependency(res.Manifest, f.bootstrapURL, f.bootstrapName, f.bootstrapTargets); err != nil {
			return nil, nil, false, err
		}
	}
	return res.Manifest, res, verified, nil
}

func readImportMeta(ctx context.Context, src *obsimport.GitSource, f *importGitFlags) ([]byte, error) {
	if f.meta != "" {
		data, err := os.ReadFile(filepath.Clean(f.meta))
		if err != nil {
			return nil, fmt.Errorf("read --meta: %w", err)
		}
		return data, nil
	}
	data, found, err := src.File(ctx, "_meta")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("no _meta in %s; pass --meta", f.ref)
	}
	return data, nil
}

func importPackages(ctx context.Context, src *obsimport.GitSource, f *importGitFlags) ([]*manifest.PackageManifest, *obsimport.GitImportReport, error) {
	raw, found, err := src.File(ctx, ".gitmodules")
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("no .gitmodules in %s", f.ref)
	}
	links, err := src.Gitlinks(ctx)
	if err != nil {
		return nil, nil, err
	}
	mods := obsimport.ParseGitmodules(string(raw))
	res := obsimport.URLResolver{GiteaBase: f.giteaBase}
	if f.giteaBase == "" {
		res.Superproject = src.RemoteURL(ctx)
	}
	if res.NeedsBase(mods) {
		return nil, nil, errors.New(".gitmodules has relative URLs but --repo has no http(s) origin remote; pass --gitea-base")
	}
	pkgs, rep := obsimport.BuildPackages(mods, links, res, f.defaultBranch)
	return pkgs, rep, nil
}

func writeImportTree(f *importGitFlags, prj *manifest.ProjectManifest, pkgs []*manifest.PackageManifest) error {
	out := filepath.Clean(f.out)
	projectFile := filepath.Join(out, "project.yaml")
	if _, err := os.Stat(projectFile); err == nil && !f.force {
		return fmt.Errorf("%s exists; use --force to overwrite", projectFile)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeYAML(projectFile, prj); err != nil {
		return err
	}
	for _, p := range pkgs {
		if err := writeYAML(filepath.Join(out, manifest.ShardPath(p.Package.Name)), p); err != nil {
			return err
		}
	}
	return nil
}

func writeYAML(file string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", file, err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

func reportImportGit(cmd *cobra.Command, f *importGitFlags, opts *OutputOptions, doc *importGitOutput) error {
	if opts.JSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	p := doc.Packages
	fmt.Fprintf(cmd.ErrOrStderr(), "imported %s from %s (%s): %d packages (%d in .gitmodules, %d in tree), %d skipped, verified=%t, dry-run=%t, out=%s\n",
		doc.Project, f.ref, p.ResolvedCommit, p.Packages, p.InGitmodules, p.InTree,
		len(p.Skipped)+len(p.MissingInTree)+len(p.MissingInModules), doc.Verified, doc.DryRun, f.out)
	return nil
}
