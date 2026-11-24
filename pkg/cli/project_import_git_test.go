package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const (
	commitA = "1111111111111111111111111111111111111111"
	commitB = "2222222222222222222222222222222222222222"
	commitC = "3333333333333333333333333333333333333333"
)

const importModules = `[submodule "alpha"]
	path = alpha
	url = ../../pool/alpha
	branch = slfo-1.2
[submodule "beta"]
	path = beta
	url = https://other.example/pool/beta
[submodule "ghost"]
	path = ghost
	url = ../../pool/ghost
	branch = slfo-1.2
`

const importMeta = `<project name="test:prj">
  <repository name="standard"><path project="own:base" repository="standard"/><arch>x86_64</arch></repository>
  <repository name="images"><arch>x86_64</arch></repository>
</project>`

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.CommandContext(context.Background(), "git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// syntheticSuperproject builds a repository whose branch "rel" has gitlinks for
// alpha, beta and orphan (not in .gitmodules); ghost is only in .gitmodules.
func syntheticSuperproject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "rel")
	files := map[string]string{".gitmodules": importModules, "_config": "Prefer: x\n", "_meta": importMeta}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", name)
	}
	for name, sha := range map[string]string{"alpha": commitA, "beta": commitB, "orphan": commitC} {
		runGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+name)
	}
	runGit(t, dir, "commit", "-q", "-m", "init")
	runGit(t, dir, "remote", "add", "origin", "https://forge.example/products/SUPER.git")
	return dir
}

func runCLI(t *testing.T, store storage.Store, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := cli.NewRootCommand(store)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"project"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func readPackage(t *testing.T, out, name string) *manifest.PackageManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, manifest.ShardPath(name)))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := manifest.ParsePackage(data)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestImportOBSGit(t *testing.T) {
	repo := syntheticSuperproject(t)
	tests := []struct {
		name      string
		extra     []string
		wantAlpha string
		wantBoot  bool
	}{
		{"origin remote resolves relative urls", nil, "https://forge.example/pool/alpha.git", false},
		{"gitea base wins", []string{"--gitea-base", "https://src.example"}, "https://src.example/pool/alpha.git", false},
		{"bootstrap dependency", []string{"--bootstrap-url", "http://b:8088/", "--bootstrap-name", "OLD:1.1/standard", "--bootstrap-targets", "standard"}, "https://forge.example/pool/alpha.git", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			args := append([]string{"import-obs-git", "--repo", repo, "--ref", "rel", "--out", out, "--json"}, tc.extra...)
			stdout, stderr, err := runCLI(t, storage.NewMemoryStore(), args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			checkImportOutput(t, out, stdout, stderr, tc.wantAlpha, tc.wantBoot)
		})
	}
}

func checkImportOutput(t *testing.T, out, stdout, stderr, wantAlpha string, wantBoot bool) {
	t.Helper()
	var doc struct {
		Verified bool `json:"verified"`
		Packages struct {
			Packages         int      `json:"packages"`
			MissingInTree    []string `json:"missing_in_tree"`
			MissingInModules []string `json:"missing_in_gitmodules"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if !doc.Verified || doc.Packages.Packages != 2 ||
		strings.Join(doc.Packages.MissingInTree, ",") != "ghost" || strings.Join(doc.Packages.MissingInModules, ",") != "orphan" {
		t.Errorf("doc = %+v", doc)
	}
	if !strings.Contains(stderr, "skipped ghost") || !strings.Contains(stderr, "skipped orphan") {
		t.Errorf("stderr lacks diagnostics: %q", stderr)
	}
	alpha, beta := readPackage(t, out, "alpha"), readPackage(t, out, "beta")
	if alpha.Source.Git != wantAlpha || alpha.Source.Branch != "slfo-1.2" || alpha.Source.Commit != commitA ||
		alpha.Source.Recipe != "" || !alpha.Build.Enabled || alpha.Resources != (manifest.ResourceLimits{}) {
		t.Errorf("alpha = %+v", alpha)
	}
	if beta.Source.Git != "https://other.example/pool/beta.git" || beta.Source.Branch != "" || beta.Source.Commit != commitB {
		t.Errorf("beta = %+v", beta)
	}
	if _, err := os.Stat(filepath.Join(out, manifest.ShardPath("orphan"))); err == nil {
		t.Error("orphan was written")
	}
	checkBootstrap(t, out, wantBoot)
}

func checkBootstrap(t *testing.T, out string, want bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "macros_file") || strings.Contains(string(data), "debug_package") {
		t.Error("project.yaml contains macros_file or debug_package")
	}
	prj, err := manifest.ParseProject(data)
	if err != nil {
		t.Fatal(err)
	}
	std, images := prj.Targets["standard"].Dependencies, prj.Targets["images"].Dependencies
	if !want {
		if len(std) != 1 || len(images) != 0 {
			t.Errorf("unexpected dependencies %+v %+v", std, images)
		}
		return
	}
	if len(std) != 2 || std[0].Project != "own:base" || std[1].URL != "http://b:8088/" || std[1].Name != "OLD:1.1/standard" {
		t.Errorf("standard deps = %+v", std)
	}
	if len(images) != 0 {
		t.Errorf("images must not get the bootstrap dependency: %+v", images)
	}
}

func TestImportOBSGitErrors(t *testing.T) {
	repo := syntheticSuperproject(t)
	out := filepath.Join(t.TempDir(), "out")
	base := []string{"import-obs-git", "--repo", repo, "--out", out}
	tests := []struct {
		name string
		args []string
	}{
		{"unknown ref", append(base, "--ref", "nope")},
		{"option-like ref", append(base, "--ref", "--help=x")},
		{"missing prjconf", append(base, "--ref", "rel", "--prjconf-path", "nothere")},
		{"bootstrap url without targets", append(base, "--ref", "rel", "--bootstrap-url", "http://b/", "--bootstrap-name", "n")},
		{"bootstrap name without url", append(base, "--ref", "rel", "--bootstrap-name", "n")},
		{"bootstrap unknown target", append(base, "--ref", "rel", "--bootstrap-url", "http://b/", "--bootstrap-name", "n", "--bootstrap-targets", "zzz")},
		{"missing meta file", append(base, "--ref", "rel", "--meta", filepath.Join(t.TempDir(), "none"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := runCLI(t, storage.NewMemoryStore(), tc.args...); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Stat(filepath.Join(out, "project.yaml")); err == nil {
				t.Fatal("output written despite error")
			}
		})
	}
}

func TestImportOBSGitDryRunAndForce(t *testing.T) {
	repo := syntheticSuperproject(t)
	out := filepath.Join(t.TempDir(), "out")
	args := []string{"import-obs-git", "--repo", repo, "--ref", "rel", "--out", out}
	if _, _, err := runCLI(t, storage.NewMemoryStore(), append(args, "--dry-run")...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("--dry-run wrote output")
	}
	if _, _, err := runCLI(t, storage.NewMemoryStore(), args...); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, storage.NewMemoryStore(), args...); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second run must refuse, got %v", err)
	}
	if _, _, err := runCLI(t, storage.NewMemoryStore(), append(args, "--force")...); err != nil {
		t.Fatal(err)
	}
}

func TestApplyAndDependencyRemove(t *testing.T) {
	repo := syntheticSuperproject(t)
	out := filepath.Join(t.TempDir(), "out")
	_, stderr, err := runCLI(t, storage.NewMemoryStore(), "import-obs-git", "--repo", repo, "--ref", "rel", "--out", out,
		"--bootstrap-url", "http://b/", "--bootstrap-name", "OLD:1.1/standard", "--bootstrap-targets", "standard")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	store := storage.NewMemoryStore()
	if _, _, err := runCLI(t, store, "apply", out, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProject(context.Background(), "test:prj"); err == nil {
		t.Fatal("--dry-run stored the project")
	}
	stdout, _, err := runCLI(t, store, "apply", out, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Applied  int   `json:"packages_applied"`
		Failures []any `json:"failures"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.Applied != 2 || len(doc.Failures) != 0 {
		t.Fatalf("doc = %+v, %v", doc, err)
	}
	checkDependencyRemove(t, store)
}

func checkDependencyRemove(t *testing.T, store storage.Store) {
	t.Helper()
	rm := []string{"dependency", "remove", "test:prj", "--target", "standard", "--name", "OLD:1.1/standard"}
	if _, _, err := runCLI(t, store, rm...); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("must demand --yes, got %v", err)
	}
	if _, _, err := runCLI(t, store, append(rm, "--dry-run")...); err != nil {
		t.Fatal(err)
	}
	prj, _ := store.GetProject(context.Background(), "test:prj")
	if len(prj.Targets["standard"].Dependencies) != 2 {
		t.Fatal("--dry-run modified the project")
	}
	stdout, _, err := runCLI(t, store, append(rm, "--yes", "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Removed   bool     `json:"removed"`
		Remaining []string `json:"remaining"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || !doc.Removed || len(doc.Remaining) != 1 || doc.Remaining[0] != "own:base/standard" {
		t.Fatalf("doc = %+v, %v", doc, err)
	}
	prj, _ = store.GetProject(context.Background(), "test:prj")
	if d := prj.Targets["standard"].Dependencies; len(d) != 1 || d[0].Project != "own:base" {
		t.Errorf("deps = %+v", d)
	}
	if _, _, err := runCLI(t, store, append(rm, "--yes")...); err == nil {
		t.Error("removing twice must fail")
	}
	bad := []string{"dependency", "remove", "test:prj", "--target", "nope", "--name", "x", "--yes"}
	if _, _, err := runCLI(t, store, bad...); err == nil {
		t.Error("unknown target must fail")
	}
	if _, _, err := runCLI(t, store, "dependency", "remove", "none:prj", "--target", "standard", "--name", "x", "--yes"); err == nil {
		t.Error("unknown project must fail")
	}
}

func TestApplyReportsFailures(t *testing.T) {
	repo := syntheticSuperproject(t)
	out := filepath.Join(t.TempDir(), "out")
	if _, _, err := runCLI(t, storage.NewMemoryStore(), "import-obs-git", "--repo", repo, "--ref", "rel", "--out", out); err != nil {
		t.Fatal(err)
	}
	alpha := filepath.Join(out, manifest.ShardPath("alpha"))
	if err := os.WriteFile(alpha, []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	misplaced := filepath.Join(out, "packages", "zz", "beta.yaml")
	if err := os.MkdirAll(filepath.Dir(misplaced), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(out, manifest.ShardPath("beta")), misplaced); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCLI(t, storage.NewMemoryStore(), "apply", out, "--json")
	if err == nil {
		t.Fatal("apply must exit non-zero on failures")
	}
	var doc struct {
		Applied  int `json:"packages_applied"`
		Failures []struct {
			File string `json:"file"`
		} `json:"failures"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.Applied != 0 || len(doc.Failures) != 2 {
		t.Fatalf("doc = %+v, %v\n%s", doc, err, stdout)
	}
	if _, _, err := runCLI(t, storage.NewMemoryStore(), "apply", t.TempDir()); err == nil {
		t.Error("directory without project.yaml must fail")
	}
}

func TestImportOBSGitPackageOverrides(t *testing.T) {
	tests := []struct {
		name        string
		overrides   string
		wantAlpha   string
		wantBeta    string
		wantUnused  string
		wantApplied int
		wantErr     string
	}{
		{name: "no file", wantApplied: 0},
		{name: "sandbox for one package", overrides: "packages:\n  alpha:\n    build:\n      constraints:\n        sandbox: kvm\n",
			wantAlpha: "kvm", wantApplied: 1},
		{name: "two packages and an unknown one",
			overrides: "packages:\n  alpha: {build: {constraints: {sandbox: secure !chroot}}}\n  beta: {build: {constraints: {sandbox: none}}}\n  zzz: {build: {constraints: {sandbox: kvm}}}\n",
			wantAlpha: "secure !chroot", wantBeta: "none", wantApplied: 2, wantUnused: "zzz"},
		{name: "unknown key", overrides: "packages:\n  alpha:\n    sources: {x: 1}\n", wantErr: "sources"},
		{name: "invalid resources", overrides: "packages:\n  alpha: {resources: {memory: lots}}\n", wantErr: "resources.memory"},
		{name: "invalid sandbox", overrides: "packages:\n  alpha: {build: {constraints: {sandbox: 'kvm;x'}}}\n", wantErr: "sandbox"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := syntheticSuperproject(t)
			if tc.overrides != "" {
				if err := os.MkdirAll(filepath.Join(repo, "_bsgo"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "_bsgo", "packages.yaml"), []byte(tc.overrides), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, repo, "add", "_bsgo/packages.yaml")
				runGit(t, repo, "commit", "-q", "-m", "overrides")
			}
			out := filepath.Join(t.TempDir(), "out")
			stdout, stderr, err := runCLI(t, storage.NewMemoryStore(), "import-obs-git", "--repo", repo, "--ref", "rel", "--out", out, "--json")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want mention of %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			var doc struct {
				Packages struct {
					Applied int      `json:"overrides_applied"`
					Unused  []string `json:"unused_overrides"`
				} `json:"packages"`
			}
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Packages.Applied != tc.wantApplied || strings.Join(doc.Packages.Unused, ",") != tc.wantUnused {
				t.Errorf("report = %+v", doc.Packages)
			}
			if got := readPackage(t, out, "alpha").Build.Constraints.Sandbox; got != tc.wantAlpha {
				t.Errorf("alpha sandbox = %q, want %q", got, tc.wantAlpha)
			}
			if got := readPackage(t, out, "beta").Build.Constraints.Sandbox; got != tc.wantBeta {
				t.Errorf("beta sandbox = %q, want %q", got, tc.wantBeta)
			}
		})
	}
}
