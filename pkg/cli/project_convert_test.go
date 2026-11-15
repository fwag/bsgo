package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const convertMeta = `<project name="test:prj">
  <repository name="standard"><arch>x86_64</arch></repository>
</project>`

const convertPrjconf = "%define a 1\nPrefer: x\n%ifarch x86_64\nPrefer: y\n%endif\n%if 0%{?is_stage_project}\nRelease: r\n%endif\n"

func runConvert(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := cli.NewRootCommand(storage.NewMemoryStore())
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"project", "convert-obs"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func convertInputs(t *testing.T) (meta, conf, dir string) {
	t.Helper()
	dir = t.TempDir()
	meta, conf = filepath.Join(dir, "_meta"), filepath.Join(dir, "_config")
	if err := os.WriteFile(meta, []byte(convertMeta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte(convertPrjconf), 0o600); err != nil {
		t.Fatal(err)
	}
	return meta, conf, dir
}

func TestConvertOBSWritesManifestAndReportsJSON(t *testing.T) {
	meta, conf, dir := convertInputs(t)
	out := filepath.Join(dir, "project.yaml")
	stdout, stderr, err := runConvert(t, "--meta", meta, "--prjconf", conf, "--out", out, "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("--json must keep stderr clean here, got %q", stderr)
	}
	var doc struct {
		Out      string `json:"out"`
		Verified bool   `json:"verified"`
		Report   struct {
			Project      string `json:"project"`
			RawFragments int    `json:"raw_fragments"`
		} `json:"report"`
		Manifest *manifest.ProjectManifest `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Out != out || !doc.Verified || doc.Report.Project != "test:prj" || doc.Report.RawFragments != 1 || doc.Manifest != nil {
		t.Errorf("doc = %+v", doc)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("ANSI escapes in --json output")
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	prj, err := manifest.ParseProject(data)
	if err != nil {
		t.Fatalf("written manifest does not load: %v", err)
	}
	if prj.ArchConfig["x86_64"] == nil || !strings.Contains(prj.PublicConfig.RawConfig, "is_stage_project") {
		t.Errorf("manifest = %+v", prj)
	}
}

func TestConvertOBSStdoutYAMLAndSummaryOnStderr(t *testing.T) {
	meta, conf, _ := convertInputs(t)
	stdout, stderr, err := runConvert(t, "--meta", meta, "--prjconf", conf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.ParseProject([]byte(stdout)); err != nil {
		t.Errorf("stdout is not a manifest: %v", err)
	}
	if !strings.Contains(stderr, "converted test:prj") || strings.Contains(stderr, "\x1b[") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestConvertOBSDryRunAndForce(t *testing.T) {
	meta, conf, dir := convertInputs(t)
	out := filepath.Join(dir, "project.yaml")
	if _, _, err := runConvert(t, "--meta", meta, "--prjconf", conf, "--out", out, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("--dry-run wrote the file")
	}
	if _, _, err := runConvert(t, "--meta", meta, "--prjconf", conf, "--out", out); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runConvert(t, "--meta", meta, "--prjconf", conf, "--out", out); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second run must refuse to overwrite, got %v", err)
	}
	if _, _, err := runConvert(t, "--meta", meta, "--prjconf", conf, "--out", out, "--force"); err != nil {
		t.Fatal(err)
	}
}

func TestConvertOBSInputErrors(t *testing.T) {
	meta, conf, dir := convertInputs(t)
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("%if 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
	}{
		{"missing flags", nil},
		{"missing meta file", []string{"--meta", filepath.Join(dir, "none"), "--prjconf", conf}},
		{"missing prjconf file", []string{"--meta", meta, "--prjconf", filepath.Join(dir, "none")}},
		{"broken meta", []string{"--meta", bad, "--prjconf", conf}},
		{"broken prjconf", []string{"--meta", meta, "--prjconf", bad}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := runConvert(t, tc.args...); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
