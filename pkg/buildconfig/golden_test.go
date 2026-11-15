package buildconfig_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

const fixtureDir = "../../test/fixtures/prjconf"

// TestGolden verifies the parser against golden files that were generated
// from the Perl oracle (see test/oracle). It needs no Perl.
func TestGolden(t *testing.T) {
	var files []string
	for _, pat := range []string{"*.prjconf", "handmade/*.conf"} {
		m, err := filepath.Glob(filepath.Join(fixtureDir, pat))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, file := range files {
		for _, arch := range []string{"x86_64", "aarch64", "i586"} {
			base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
			t.Run(base+"/"+arch, func(t *testing.T) {
				checkGolden(t, file, filepath.Join(fixtureDir, "golden", base+"."+arch+".json"), arch)
			})
		}
	}
}

func checkGolden(t *testing.T, file, golden, arch string) {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := buildconfig.Parse(string(text), arch)
	got, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var g, w map[string]any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	for k := range w {
		if !reflect.DeepEqual(g[k], w[k]) {
			t.Errorf("field %q differs from golden (regenerate with the oracle test only if the Perl output changed)", k)
		}
	}
	if len(g) != len(w) {
		t.Errorf("field count: got %d, golden %d", len(g), len(w))
	}
}

func TestSLFOSanity(t *testing.T) {
	text, err := os.ReadFile(filepath.Join(fixtureDir, "SUSE_SLFO_1.2.prjconf"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := buildconfig.Parse(string(text), "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Prefer) != 836 {
		t.Errorf("prefer entries: got %d, want 836", len(cfg.Prefer))
	}
	if cfg.Release != "<CI_CNT>.<B_CNT>" {
		t.Errorf("release: got %q", cfg.Release)
	}
}
