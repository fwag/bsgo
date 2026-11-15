package obsimport_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/obsimport"
	"gopkg.in/yaml.v3"
)

// This is the ADR 0010 acceptance gate: converting an OBS prjconf and layering
// the result must parse to the same Config as parsing the original prjconf
// through the getconfig text assembly (buildconfig.LayerTexts).
//
// The whole Config is compared field by field (obsimport.DiffConfig). The
// differences that are tolerated are listed in obsimport.Canonical: Warnings
// (line numbers of the generated text) and the order of the set-like lists
// Prefer, Ignore, Conflict, Support, Required, ExpandFlags, BuildFlags and
// PublishFlags (plus the ConflictH values), which scopes may reorder. All other
// fields, including Preinstall, Vminstall, Macros and RawMacros, must match
// exactly.

const fixtureDir = "../../test/fixtures/prjconf"

var (
	arches    = []string{"x86_64", "aarch64", "i586"}
	repoNames = []string{"standard", "images", "containerfile"}
)

type fixture struct {
	name     string
	prjconf  string
	manifest *manifest.ProjectManifest
}

func loadFixture(t *testing.T, base string) fixture {
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
	// Go through YAML so that the committed form, not the in-memory model,
	// is what is verified.
	data, err := yaml.Marshal(res.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	prj, err := manifest.ParseProject(data)
	if err != nil {
		t.Fatalf("converted manifest does not load: %v", err)
	}
	return fixture{name: meta.Name, prjconf: string(conf), manifest: prj}
}

func parseBoth(t *testing.T, text, arch string) *buildconfig.Config {
	t.Helper()
	c, err := buildconfig.Parse(text, arch)
	if err != nil {
		t.Fatalf("parse (%s): %v", arch, err)
	}
	return c
}

type layerCase struct {
	consumer string
	repo     string
	path     []buildconfig.PathElem
	load     buildconfig.Loader
	ref      []buildconfig.Source
}

func (lc layerCase) check(t *testing.T) {
	t.Helper()
	gotS, gotW, err := buildconfig.Layer(lc.consumer, lc.repo, lc.path, lc.load)
	if err != nil {
		t.Fatal(err)
	}
	wantS, wantW := buildconfig.LayerTexts(lc.consumer, lc.ref)
	for _, arch := range arches {
		for kind, pair := range map[string][2]string{"scheduler": {gotS, wantS}, "worker": {gotW, wantW}} {
			got, want := parseBoth(t, pair[0], arch), parseBoth(t, pair[1], arch)
			if d := obsimport.DiffConfig(obsimport.Canonical(got), obsimport.Canonical(want)); len(d) > 0 {
				t.Errorf("%s/%s %s arch %s: configs differ in %v", lc.consumer, lc.repo, kind, arch, d)
			}
		}
	}
}

func loaderOf(prjs ...*manifest.ProjectManifest) buildconfig.Loader {
	return func(name string) (*manifest.ProjectManifest, error) {
		for _, p := range prjs {
			if p.Project.Name == name {
				return p, nil
			}
		}
		return nil, nil
	}
}

func TestRoundTripSingleProject(t *testing.T) {
	for _, base := range []string{"SUSE_SLFO_1.1", "SUSE_SLFO_1.2"} {
		t.Run(base, func(t *testing.T) {
			fx := loadFixture(t, base)
			for _, repo := range repoNames {
				if _, ok := fx.manifest.Targets[repo]; !ok {
					t.Fatalf("fixture meta lacks repository %s", repo)
				}
			}
			mismatches, err := obsimport.Verify(fx.manifest, fx.prjconf, arches)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range mismatches {
				t.Errorf("%s/%s consumer %s arch %s %s: differs in %v", fx.name, m.Repo, m.Consumer, m.Arch, m.Output, m.Fields)
			}
		})
	}
}

// TestConverterIsNotVacuous guards the gate: the model must carry real
// structure, so that an empty or all-raw conversion cannot pass unnoticed.
func TestConverterIsNotVacuous(t *testing.T) {
	fx := loadFixture(t, "SUSE_SLFO_1.2")
	pub := fx.manifest.PublicConfig
	if pub == nil || len(pub.Prefer) < 10 || len(pub.Defines) < 5 || len(pub.FileProvides) < 50 {
		t.Fatalf("public_config is not structured")
	}
	if fx.manifest.ProtectedConfig == nil || len(fx.manifest.ProtectedConfig.BuildFlags) == 0 {
		t.Errorf("protected_config missing")
	}
	if fx.manifest.Targets["images"].Config == nil {
		t.Errorf("targets.images.config missing")
	}
	if len(fx.manifest.ArchConfig) == 0 {
		t.Errorf("arch_config missing")
	}
}

func TestRoundTripLayeredSLFO12OverSLFO11(t *testing.T) {
	f12 := loadFixture(t, "SUSE_SLFO_1.2")
	f11 := loadFixture(t, "SUSE_SLFO_1.1")
	path := []buildconfig.PathElem{
		{Project: f12.name, Repo: "standard"},
		{Project: f11.name, Repo: "standard"},
	}
	for _, consumer := range []string{f12.name, f12.name + ":Sub", "home:x"} {
		t.Run(consumer, func(t *testing.T) {
			layerCase{
				consumer: consumer, repo: "standard", path: path,
				load: loaderOf(f12.manifest, f11.manifest),
				ref: []buildconfig.Source{
					{Project: f11.name, Repo: "standard", Text: f11.prjconf},
					{Project: f12.name, Repo: "standard", Text: f12.prjconf},
				},
			}.check(t)
		})
	}
}

func TestLayerTextsHeaders(t *testing.T) {
	srcs := []buildconfig.Source{
		{Project: "A", Repo: "r1", Text: "Prefer: a\n"},
		{Project: "A:B", Repo: "r2", Text: "Prefer: b\n"},
	}
	sched, _ := buildconfig.LayerTexts("A:B", srcs)
	for _, want := range []string{
		"%define _project A:B\n",
		"### from A\n%define _repository r1\n%define _is_this_project 0\n%define _is_in_project 1\n#!!line A:0\n",
		"### from A:B\n%define _repository r2\n%define _is_this_project 1\n#!!line A:B:0\n",
	} {
		if !strings.Contains(sched, want) {
			t.Errorf("missing %q in\n%s", want, sched)
		}
	}
	if strings.Count(sched, "%define _is_in_project") != 1 {
		t.Errorf("_is_in_project must only be redefined when it changes:\n%s", sched)
	}
}
