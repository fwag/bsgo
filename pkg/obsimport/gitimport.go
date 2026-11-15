package obsimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

// gitlinkMode is the tree entry mode of a submodule commit.
const gitlinkMode = "160000"

var commitRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Submodule is one entry of a .gitmodules file.
type Submodule struct {
	Name   string
	Path   string
	URL    string
	Branch string
}

// Gitlink is one mode-160000 entry of a git tree.
type Gitlink struct {
	Path   string
	Commit string
}

// Skipped records a package that was not converted.
type Skipped struct {
	Package string `json:"package"`
	Reason  string `json:"reason"`
}

// GitImportReport summarises a package import for --json output.
type GitImportReport struct {
	Ref              string    `json:"ref"`
	ResolvedCommit   string    `json:"resolved_commit"`
	Packages         int       `json:"packages"`
	InGitmodules     int       `json:"in_gitmodules"`
	InTree           int       `json:"in_tree"`
	MissingInTree    []string  `json:"missing_in_tree"`
	MissingInModules []string  `json:"missing_in_gitmodules"`
	Skipped          []Skipped `json:"skipped"`
}

// GitSource reads a superproject (such as SLFO) with git plumbing only; it
// never touches the work tree.
type GitSource struct {
	Dir string
	Ref string
}

func (g *GitSource) git(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-C", g.Dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) // #nosec G204 -- fixed binary, arguments never reach a shell
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Resolve returns the commit id the ref points at.
func (g *GitSource) Resolve(ctx context.Context) (string, error) {
	if strings.HasPrefix(g.Ref, "-") {
		return "", fmt.Errorf("invalid ref %q", g.Ref)
	}
	out, err := g.git(ctx, "rev-parse", "--verify", "--quiet", g.Ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve ref %q in %s: %w", g.Ref, g.Dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// File returns the content of a top-level file at the ref, or found=false
// when the ref has no such file.
func (g *GitSource) File(ctx context.Context, name string) (data []byte, found bool, err error) {
	listing, err := g.git(ctx, "ls-tree", "--name-only", g.Ref, "--", name)
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(listing)) == 0 {
		return nil, false, nil
	}
	out, err := g.git(ctx, "show", g.Ref+":"+name)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// RemoteURL returns remote.origin.url, or "" when none is configured.
func (g *GitSource) RemoteURL(ctx context.Context) string {
	out, err := g.git(ctx, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Gitlinks lists the mode-160000 entries at the top level of the ref.
func (g *GitSource) Gitlinks(ctx context.Context) ([]Gitlink, error) {
	out, err := g.git(ctx, "ls-tree", "-z", g.Ref)
	if err != nil {
		return nil, err
	}
	return parseLsTree(out)
}

func parseLsTree(out []byte) ([]Gitlink, error) {
	var links []Gitlink
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(rec), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree record %q", rec)
		}
		if fields[0] != gitlinkMode {
			continue
		}
		if !commitRe.MatchString(fields[2]) {
			return nil, fmt.Errorf("entry %s: %q is not a full commit id", name, fields[2])
		}
		links = append(links, Gitlink{Path: name, Commit: fields[2]})
	}
	return links, nil
}

// ParseGitmodules parses the .gitmodules ini format.
func ParseGitmodules(raw string) []Submodule {
	var mods []Submodule
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[submodule"):
			name := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(line, "]"), "[submodule"), ` "`)
			mods = append(mods, Submodule{Name: name})
		case len(mods) > 0:
			if k, v, ok := strings.Cut(line, "="); ok {
				mods[len(mods)-1].set(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
	}
	return mods
}

func (s *Submodule) set(key, val string) {
	switch key {
	case "path":
		s.Path = val
	case "url":
		s.URL = val
	case "branch":
		s.Branch = val
	}
}

// URLResolver turns .gitmodules URLs into absolute clone URLs.
type URLResolver struct {
	// GiteaBase is the forge root, for example https://src.opensuse.org.
	// Relative URLs have their leading ../ segments dropped and are joined to it.
	GiteaBase string
	// Superproject is the URL of the superproject, used (git semantics) when
	// GiteaBase is empty.
	Superproject string
}

// Resolve returns the absolute URL for a .gitmodules url value.
func (r URLResolver) Resolve(raw string) (string, error) {
	switch {
	case raw == "":
		return "", errors.New("empty url")
	case strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../"):
		return r.resolveRelative(raw)
	default:
		return withGitSuffix(raw), nil
	}
}

// NeedsBase reports whether any submodule URL is relative while the resolver
// has neither GiteaBase nor an http(s) superproject URL.
func (r URLResolver) NeedsBase(mods []Submodule) bool {
	if r.GiteaBase != "" || strings.Contains(r.Superproject, "://") {
		return false
	}
	for _, m := range mods {
		if strings.HasPrefix(m.URL, "./") || strings.HasPrefix(m.URL, "../") {
			return true
		}
	}
	return false
}

func (r URLResolver) resolveRelative(raw string) (string, error) {
	rest, ups := raw, 0
	for {
		if strings.HasPrefix(rest, "../") {
			ups++
			rest = rest[3:]
		} else if strings.HasPrefix(rest, "./") {
			rest = rest[2:]
		} else {
			break
		}
	}
	if r.GiteaBase != "" {
		return withGitSuffix(strings.TrimRight(r.GiteaBase, "/") + "/" + rest), nil
	}
	base := strings.TrimRight(r.Superproject, "/")
	scheme, hostPath, ok := strings.Cut(base, "://")
	if !ok {
		return "", fmt.Errorf("relative url %q needs --gitea-base or an http(s) origin remote", raw)
	}
	host, p, _ := strings.Cut(hostPath, "/")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if ups > len(parts) {
		return "", fmt.Errorf("relative url %q climbs above the remote %q", raw, base)
	}
	parts = parts[:len(parts)-ups]
	joined := scheme + "://" + host
	if len(parts) > 0 {
		joined += "/" + path.Join(parts...)
	}
	return withGitSuffix(joined + "/" + rest), nil
}

func withGitSuffix(u string) string {
	if (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) && !strings.HasSuffix(u, ".git") {
		return u + ".git"
	}
	return u
}

// BuildPackages joins .gitmodules and the tree gitlinks into package
// manifests, sorted by name. Packages present on only one side are reported
// and skipped. defaultBranch is used when .gitmodules gives no branch; empty
// leaves the branch unset.
func BuildPackages(mods []Submodule, links []Gitlink, res URLResolver, defaultBranch string) ([]*manifest.PackageManifest, *GitImportReport) {
	rep := &GitImportReport{
		InGitmodules: len(mods), InTree: len(links),
		MissingInTree: []string{}, MissingInModules: []string{}, Skipped: []Skipped{},
	}
	byPath := make(map[string]Submodule, len(mods))
	for _, m := range mods {
		byPath[m.Path] = m
	}
	inTree := make(map[string]bool, len(links))
	var pkgs []*manifest.PackageManifest
	for _, l := range links {
		inTree[l.Path] = true
		m, ok := byPath[l.Path]
		if !ok {
			rep.MissingInModules = append(rep.MissingInModules, l.Path)
			continue
		}
		pkg, err := newPackage(l, m, res, defaultBranch)
		if err != nil {
			rep.Skipped = append(rep.Skipped, Skipped{Package: l.Path, Reason: err.Error()})
			continue
		}
		pkgs = append(pkgs, pkg)
	}
	for _, m := range mods {
		if m.Path != "" && !inTree[m.Path] {
			rep.MissingInTree = append(rep.MissingInTree, m.Path)
		}
	}
	sort.Strings(rep.MissingInTree)
	sort.Strings(rep.MissingInModules)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Package.Name < pkgs[j].Package.Name })
	rep.Packages = len(pkgs)
	return pkgs, rep
}

func newPackage(l Gitlink, m Submodule, res URLResolver, defaultBranch string) (*manifest.PackageManifest, error) {
	if strings.ContainsAny(l.Path, "/\\") || strings.Contains(l.Path, "..") {
		return nil, fmt.Errorf("invalid package name %q", l.Path)
	}
	url, err := res.Resolve(m.URL)
	if err != nil {
		return nil, err
	}
	branch := m.Branch
	if branch == "" {
		branch = defaultBranch
	}
	return &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: l.Path},
		Source:        manifest.PackageSource{Git: url, Branch: branch, Commit: l.Commit},
		Build:         manifest.PackageBuild{Enabled: true},
	}, nil
}

// AddBootstrapDependency appends a URL dependency named name after the
// existing dependencies of each listed target (Bootstrap Dependency, plan D4).
func AddBootstrapDependency(prj *manifest.ProjectManifest, url, name string, targets []string) error {
	if url == "" || name == "" || len(targets) == 0 {
		return errors.New("a bootstrap dependency needs url, name and at least one target")
	}
	for _, t := range targets {
		bt, ok := prj.Targets[t]
		if !ok {
			return fmt.Errorf("bootstrap target %q is not a target of %s", t, prj.Project.Name)
		}
		for _, d := range bt.Dependencies {
			if d.Name == name {
				return fmt.Errorf("target %q already has a dependency named %q", t, name)
			}
		}
		bt.Dependencies = append(bt.Dependencies, manifest.DependencySource{URL: url, Type: "rpm-md", Name: name})
		prj.Targets[t] = bt
	}
	return nil
}

// RemoveDependency drops the dependency named name from target and reports
// whether one was removed.
func RemoveDependency(prj *manifest.ProjectManifest, target, name string) (bool, error) {
	bt, ok := prj.Targets[target]
	if !ok {
		return false, fmt.Errorf("target %q is not a target of %s", target, prj.Project.Name)
	}
	kept := make([]manifest.DependencySource, 0, len(bt.Dependencies))
	for _, d := range bt.Dependencies {
		if d.Name != name {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(bt.Dependencies) {
		return false, nil
	}
	bt.Dependencies = kept
	prj.Targets[target] = bt
	return true, nil
}
