package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

func newAdminReleaseCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Inspect and correct the release numbers (CI_CNT, vrev) of packages",
		Long: `Release numbers of builds are <CI_CNT>.<B_CNT> (for example 160000.25.1 in
SLFO). CI_CNT is the vrev of the package revision a build was made from, like the
OBS revision counter: a revision is appended to the package's log whenever its pinned
(git url, commit) changes, and its vrev is computed in the background by the server
from the first-parent git history of the commit (OBS rule: it counts up per version of
the main recipe and restarts for a new one), never below the vrev of an earlier
revision with the same version. B_CNT counts the builds of one <version>-<vrev> of a
unit.

These commands talk to the database directly, with the deployment credentials of the
other admin commands (DATABASE_URL). Access tokens need the scope admin:release, or
admin:release:<project> for one project.`,
	}
	cmd.AddCommand(newReleaseShowCmd(store, opts))
	cmd.AddCommand(newReleaseRecomputeCmd(store, opts))
	cmd.AddCommand(newReleaseSetCmd(store, opts))
	return cmd
}

// releaseUnit is the last release identity of one unit in one repository.
type releaseUnit struct {
	Repo    string `json:"repo"`
	Arch    string `json:"arch"`
	Unit    string `json:"unit"`
	State   string `json:"state"`
	Versrel string `json:"versrel,omitempty"`
	Bcnt    int    `json:"bcnt,omitempty"`
	Commit  string `json:"source_commit,omitempty"`
}

type releasePackage struct {
	Package   string                    `json:"package"`
	Revisions []storage.PackageRevision `json:"revisions"`
	Units     []releaseUnit             `json:"units"`
}

type releaseShowReport struct {
	Project  string           `json:"project"`
	Packages []releasePackage `json:"packages"`
}

func newReleaseShowCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "show <project> [<package>...]",
		Short: "Show the revisions and release numbers of the packages of a project",
		Long: `Show, per package, its latest revision (sequence number, commit, version, git
vrev, final vrev, state, last resolver error) and, per unit known to the build state,
the versrel and B_CNT of its last job. States: pending (the server has not computed the
vrev yet; units of it wait as blocked), resolved, skipped (a superseded revision that
could not be resolved).

Without package arguments every package of the project is shown. --all-revisions
shows the whole revision log instead of the latest revision. With --json stdout is
a single deterministic document and nothing else.

Examples:
  # The two kernel packages of SLFO, machine readable:
  bsgo admin release show SUSE:SLFO:1.2 kernel-source kernel-source-azure --json

  # Every package whose revision is still being resolved:
  bsgo admin release show SUSE:SLFO:1.2 --json | jq '.packages[] | select(.revisions[0].state == "pending") | .package'

  # The full revision history of one package:
  bsgo admin release show SUSE:SLFO:1.2 glibc --all-revisions`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := buildReleaseReport(cmd, store, args[0], args[1:], all)
			if err != nil {
				return err
			}
			return renderReleaseShow(cmd.OutOrStdout(), rep, *opts)
		},
	}
	cmd.Flags().BoolVar(&all, "all-revisions", false, "Show every revision of a package instead of the latest one")
	return cmd
}

func buildReleaseReport(cmd *cobra.Command, store storage.Store, project string, pkgs []string, all bool) (*releaseShowReport, error) {
	ctx := cmd.Context()
	revs, err := store.ListRevisions(ctx, project, pkgs)
	if err != nil {
		return nil, err
	}
	byPkg := map[string][]storage.PackageRevision{}
	for _, r := range revs {
		byPkg[r.Package] = append(byPkg[r.Package], r)
	}
	if missing := missingPackages(pkgs, byPkg); len(missing) > 0 {
		return nil, fmt.Errorf("no revision log in project %s for: %s", project, strings.Join(missing, ", "))
	}
	states, err := store.ListBuildStates(ctx, project, storage.BuildStateFilter{})
	if err != nil {
		return nil, err
	}
	units := map[string][]releaseUnit{}
	for _, st := range states {
		if _, ok := byPkg[st.Package]; !ok {
			continue
		}
		name := st.Package
		if st.Flavor != "" {
			name += ":" + st.Flavor
		}
		units[st.Package] = append(units[st.Package], releaseUnit{
			Repo: st.Repo, Arch: st.Arch, Unit: name, State: st.State, Versrel: st.Versrel, Bcnt: st.Bcnt, Commit: st.SourceCommit,
		})
	}
	rep := &releaseShowReport{Project: project, Packages: []releasePackage{}}
	for _, name := range sortedKeys(byPkg) {
		list := byPkg[name]
		if !all {
			list = list[len(list)-1:]
		}
		u := units[name]
		if u == nil {
			u = []releaseUnit{}
		}
		rep.Packages = append(rep.Packages, releasePackage{Package: name, Revisions: list, Units: u})
	}
	return rep, nil
}

func missingPackages(want []string, have map[string][]storage.PackageRevision) []string {
	var missing []string
	for _, p := range want {
		if _, ok := have[p]; !ok {
			missing = append(missing, p)
		}
	}
	return missing
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func renderReleaseShow(w io.Writer, rep *releaseShowReport, opts OutputOptions) error {
	if opts.JSON {
		return encodeJSON(w, rep)
	}
	for _, p := range rep.Packages {
		for _, r := range p.Revisions {
			fmt.Fprintf(w, "%s seq %d commit %s state %s version %s git_vrev %d vrev %d\n",
				p.Package, r.Seq, shortCommit(r.Commit), r.State, orDash(r.Version), r.GitVrev, r.Vrev)
			if r.LastError != "" {
				fmt.Fprintf(w, "  last error (attempt %d): %s\n", r.Attempts, r.LastError)
			}
		}
		for _, u := range p.Units {
			fmt.Fprintf(w, "  %s/%s %s %s versrel %s bcnt %d\n", u.Repo, u.Arch, u.Unit, u.State, orDash(u.Versrel), u.Bcnt)
		}
	}
	return nil
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// confirmMutation enforces the mutation contract of the admin commands: with
// --dry-run nothing is changed, with --yes no question is asked, otherwise an
// interactive terminal is asked and anything else fails. It reports whether
// the mutation should go on.
func confirmMutation(cmd *cobra.Command, opts *OutputOptions, dryRun, yes bool, what string) (bool, error) {
	if dryRun {
		return false, nil
	}
	if yes {
		return true, nil
	}
	if !isTerminal(cmd.InOrStdin()) || opts.JSON {
		return false, errors.New("refusing to change data without confirmation: pass --yes to apply or --dry-run to only report")
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", what)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if a := strings.ToLower(strings.TrimSpace(line)); a == "y" || a == "yes" {
		return true, nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
	return false, nil
}

// releaseChange is the deterministic JSON document of a mutation.
type releaseChange struct {
	DryRun   bool     `json:"dry_run"`
	Applied  bool     `json:"applied"`
	Project  string   `json:"project"`
	Packages []string `json:"packages"`
	// Revision is set by "set": the revision as it is (dry run) or became.
	Revision *storage.PackageRevision `json:"revision,omitempty"`
}

func renderReleaseChange(w io.Writer, ch releaseChange, verb string, opts OutputOptions) error {
	if opts.JSON {
		if ch.Packages == nil {
			ch.Packages = []string{}
		}
		return encodeJSON(w, ch)
	}
	if !ch.Applied {
		verb = "would " + verb
	}
	fmt.Fprintf(w, "%s %d package(s) of %s\n", verb, len(ch.Packages), ch.Project)
	for _, p := range ch.Packages {
		fmt.Fprintf(w, "  %s\n", p)
	}
	if r := ch.Revision; r != nil {
		fmt.Fprintf(w, "  revision seq %d commit %s state %s version %s vrev %d\n",
			r.Seq, shortCommit(r.Commit), r.State, orDash(r.Version), r.Vrev)
	}
	return nil
}

type recomputeFlags struct {
	all, allowDecrease, dryRun, yes bool
}

func newReleaseRecomputeCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var f recomputeFlags
	cmd := &cobra.Command{
		Use:   "recompute <project> [<package>...] [--all]",
		Short: "Compute the vrev of the latest revision of packages again from the git history",
		Long: `Drop the memoized history of the packages and make their latest revision
pending again; the server resolves it again in the background (the unit waits as
blocked until then). The new vrev is at least the old one, so releases never go down,
unless --allow-decrease is given (a warning is printed; binaries that already exist
are not changed). Use it after a fix of the version rules, or when the memo is
suspected to be wrong.

Name the packages, or pass --all for every package of the project. Without --yes
the command asks on an interactive terminal and fails otherwise; --dry-run lists
the packages that would be recomputed and changes nothing. With --json stdout is
a single deterministic document.

Examples:
  # See what would be recomputed:
  bsgo admin release recompute SUSE:SLFO:1.2 kernel-source --dry-run --json

  # Recompute two packages without asking:
  bsgo admin release recompute SUSE:SLFO:1.2 kernel-source kernel-source-azure --yes

  # Recompute everything, allowing lower numbers:
  bsgo admin release recompute SUSE:SLFO:1.2 --all --allow-decrease --yes`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecompute(cmd, store, opts, args[0], args[1:], f)
		},
	}
	cmd.Flags().BoolVar(&f.all, "all", false, "Recompute every package of the project")
	cmd.Flags().BoolVar(&f.allowDecrease, "allow-decrease", false, "Allow the new vrev to be lower than the current one")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "List the packages that would be recomputed and change nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "Apply without asking for confirmation")
	return cmd
}

func runRecompute(cmd *cobra.Command, store storage.Store, opts *OutputOptions, project string, pkgs []string, f recomputeFlags) error {
	if f.all == (len(pkgs) > 0) {
		return errors.New("name the packages to recompute, or pass --all (not both)")
	}
	ctx := cmd.Context()
	revs, err := store.ListRevisions(ctx, project, pkgs)
	if err != nil {
		return err
	}
	latest := latestRevisions(revs)
	if missing := missingPackages(pkgs, latest); len(missing) > 0 {
		return fmt.Errorf("no revision log in project %s for: %s", project, strings.Join(missing, ", "))
	}
	var affected []string
	for _, name := range sortedKeys(latest) {
		if latest[name][0].State != storage.RevisionPending {
			affected = append(affected, name)
		}
	}
	ch := releaseChange{DryRun: f.dryRun, Project: project, Packages: affected}
	if f.allowDecrease {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: --allow-decrease lets a release number go below the current one; existing binaries are not changed")
	}
	ok, err := confirmMutation(cmd, opts, f.dryRun, f.yes, fmt.Sprintf("Recompute %d package(s) of %s?", len(affected), project))
	if err != nil || !ok {
		if err == nil {
			err = renderReleaseChange(cmd.OutOrStdout(), ch, "recompute", *opts)
		}
		return err
	}
	done, err := store.ResetRevisions(ctx, project, pkgs, f.allowDecrease)
	if err != nil {
		return err
	}
	ch.Applied, ch.Packages = true, done
	return renderReleaseChange(cmd.OutOrStdout(), ch, "recomputed", *opts)
}

// latestRevisions maps each package to a one element slice with its latest
// revision.
func latestRevisions(revs []storage.PackageRevision) map[string][]storage.PackageRevision {
	out := map[string][]storage.PackageRevision{}
	for _, r := range revs {
		out[r.Package] = []storage.PackageRevision{r}
	}
	return out
}

type setFlags struct {
	vrev            int
	commit, version string
	dryRun, yes     bool
}

func newReleaseSetCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var f setFlags
	cmd := &cobra.Command{
		Use:   "set <project> <package> --vrev N [--commit C] [--version V]",
		Short: "Set the vrev of the latest revision of a package by hand",
		Long: `Set the vrev (the CI_CNT of the next builds) of the latest revision of a
package, marking it as set by an administrator. Later revisions never get a lower number
for the same version, so this also raises the floor. Existing binaries are not changed;
rebuild the package (bsgo admin trigger --force) to publish the new release.

--commit makes the command fail unless the latest revision is pinned to that commit
(a guard against a pin that moved meanwhile). --version is needed only when the
revision has no version yet (still pending).

Without --yes the command asks on an interactive terminal and fails otherwise;
--dry-run shows the revision as it is now and changes nothing. With --json stdout is
a single deterministic document.

Examples:
  # Continue the numbering of an OBS-built package:
  bsgo admin release set SUSE:SLFO:1.2 kernel-source --vrev 41 --commit 3adc0dd8c228f1a --yes

  # Preview:
  bsgo admin release set SUSE:SLFO:1.2 kernel-source --vrev 41 --dry-run --json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSet(cmd, store, opts, args[0], args[1], f)
		},
	}
	cmd.Flags().IntVar(&f.vrev, "vrev", 0, "The vrev to set (at least 1, required)")
	cmd.Flags().StringVar(&f.commit, "commit", "", "Fail unless the latest revision is pinned to this commit (full id or a prefix)")
	cmd.Flags().StringVar(&f.version, "version", "", "Version of the revision, only needed while it is pending")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Show the current revision and change nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "Apply without asking for confirmation")
	return cmd
}

func runSet(cmd *cobra.Command, store storage.Store, opts *OutputOptions, project, pkg string, f setFlags) error {
	if f.vrev < 1 {
		return errors.New("--vrev is required and must be at least 1")
	}
	revs, err := store.ListRevisions(cmd.Context(), project, []string{pkg})
	if err != nil {
		return err
	}
	if len(revs) == 0 {
		return fmt.Errorf("no revision log in project %s for: %s", project, pkg)
	}
	cur := revs[len(revs)-1]
	if f.commit != "" && !strings.HasPrefix(cur.Commit, f.commit) {
		return fmt.Errorf("the latest revision of %s is pinned to %s, not %s", pkg, shortCommit(cur.Commit), f.commit)
	}
	ch := releaseChange{DryRun: f.dryRun, Project: project, Packages: []string{pkg}, Revision: &cur}
	ok, err := confirmMutation(cmd, opts, f.dryRun, f.yes, fmt.Sprintf("Set vrev %d on %s/%s?", f.vrev, project, pkg))
	if err != nil || !ok {
		if err == nil {
			err = renderReleaseChange(cmd.OutOrStdout(), ch, "set vrev on", *opts)
		}
		return err
	}
	rev, err := store.SetRevisionVrev(cmd.Context(), project, pkg, cur.Commit, f.version, f.vrev)
	if err != nil {
		return err
	}
	ch.Applied, ch.Revision = true, rev
	return renderReleaseChange(cmd.OutOrStdout(), ch, "set vrev on", *opts)
}
