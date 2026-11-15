//go:build oracle

package oracle

// ADR 0010 acceptance gate against the reference implementation: the Go
// parse of the layered, converted manifest must equal obs-build's own
// Build::read_config of the original prjconf assembled the way OBS getconfig
// does. Same tolerances as obsimport.Canonical: the order of the set-like
// lists and the hash value of multi-valued flags (excludebuild, dockerarg).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/obsimport"
	"gopkg.in/yaml.v3"
)

var (
	setListKeys   = []string{"prefer", "ignore", "conflict", "support", "required", "expandflags", "buildflags", "publishflags"}
	multiFlagKeys = []string{"excludebuild", "dockerarg"}
)

type converted struct {
	name    string
	prjconf string
	prj     *manifest.ProjectManifest
}

func convertFixture(t *testing.T, base string) converted {
	t.Helper()
	conf, err := os.ReadFile(filepath.Join(fixtureDir, base+".prjconf"))
	if err != nil {
		t.Fatal(err)
	}
	metaXML, err := os.ReadFile(filepath.Join(fixtureDir, base+".meta.xml"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := obsimport.ParseMeta(metaXML)
	if err != nil {
		t.Fatal(err)
	}
	res, err := obsimport.Convert(meta, string(conf))
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(res.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	prj, err := manifest.ParseProject(data)
	if err != nil {
		t.Fatal(err)
	}
	return converted{name: meta.Name, prjconf: string(conf), prj: prj}
}

// canonicalJSON sorts the set-like lists and blanks multi-valued flag values.
func canonicalJSON(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for _, k := range setListKeys {
		if l, ok := m[k].([]any); ok {
			sort.Slice(l, func(i, j int) bool { return fmt.Sprint(l[i]) < fmt.Sprint(l[j]) })
		}
	}
	if ch, ok := m["conflicth"].(map[string]any); ok {
		for _, l := range ch {
			if list, ok := l.([]any); ok {
				sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]) < fmt.Sprint(list[j]) })
			}
		}
	}
	for _, h := range []string{"expandflags_h", "buildflags_h", "publishflags_h"} {
		if hm, ok := m[h].(map[string]any); ok {
			for _, f := range multiFlagKeys {
				if _, ok := hm[f]; ok {
					hm[f] = ""
				}
			}
		}
	}
	return m
}

func goLayerJSON(t *testing.T, text, arch string) any {
	t.Helper()
	cfg, err := buildconfig.Parse(text, arch)
	if err != nil {
		t.Fatalf("go parse: %v", err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalJSON(normalize(t, b))
}

func perlJSON(t *testing.T, text, arch string) any {
	t.Helper()
	file := filepath.Join(t.TempDir(), "reference.conf")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return canonicalJSON(normalize(t, runOracle(t, arch, file)))
}

type oracleCell struct {
	consumer string
	repo     string
	path     []buildconfig.PathElem
	load     buildconfig.Loader
	ref      []buildconfig.Source
}

func (c oracleCell) check(t *testing.T) {
	t.Helper()
	gotS, gotW, err := buildconfig.Layer(c.consumer, c.repo, c.path, c.load)
	if err != nil {
		t.Fatal(err)
	}
	wantS, wantW := buildconfig.LayerTexts(c.consumer, c.ref)
	for _, arch := range arches {
		for kind, pair := range map[string][2]string{"scheduler": {gotS, wantS}, "worker": {gotW, wantW}} {
			var diffs []string
			diff("", perlJSON(t, pair[1], arch), goLayerJSON(t, pair[0], arch), &diffs)
			if len(diffs) > 0 {
				sort.Strings(diffs)
				if len(diffs) > 10 {
					diffs = diffs[:10]
				}
				t.Errorf("%s arch %s %s differs from Perl read_config:\n%s", c.consumer, arch, kind, strings.Join(diffs, "\n"))
			}
		}
	}
}

func loadOf(ps ...converted) buildconfig.Loader {
	return func(name string) (*manifest.ProjectManifest, error) {
		for _, p := range ps {
			if p.name == name {
				return p.prj, nil
			}
		}
		return nil, nil
	}
}

func TestRoundTripMatchesPerlOracle(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not available")
	}
	for _, base := range []string{"SUSE_SLFO_1.1", "SUSE_SLFO_1.2"} {
		fx := convertFixture(t, base)
		for _, repo := range []string{"standard", "images", "containerfile"} {
			for _, consumer := range []string{fx.name, fx.name + ":Sub", "home:x"} {
				t.Run(base+"/"+repo+"/"+consumer, func(t *testing.T) {
					oracleCell{
						consumer: consumer, repo: repo,
						path: []buildconfig.PathElem{{Project: fx.name, Repo: repo}},
						load: loadOf(fx),
						ref:  []buildconfig.Source{{Project: fx.name, Repo: repo, Text: fx.prjconf}},
					}.check(t)
				})
			}
		}
	}
}

func TestLayeredRoundTripMatchesPerlOracle(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not available")
	}
	f12, f11 := convertFixture(t, "SUSE_SLFO_1.2"), convertFixture(t, "SUSE_SLFO_1.1")
	path := []buildconfig.PathElem{{Project: f12.name, Repo: "standard"}, {Project: f11.name, Repo: "standard"}}
	for _, consumer := range []string{f12.name, f12.name + ":Sub", "home:x"} {
		t.Run(consumer, func(t *testing.T) {
			oracleCell{
				consumer: consumer, repo: "standard", path: path,
				load: loadOf(f12, f11),
				ref: []buildconfig.Source{
					{Project: f11.name, Repo: "standard", Text: f11.prjconf},
					{Project: f12.name, Repo: "standard", Text: f12.prjconf},
				},
			}.check(t)
		})
	}
}
