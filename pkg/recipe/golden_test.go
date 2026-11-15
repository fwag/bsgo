package recipe_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/recipe/recipetest"
)

const (
	specDir   = "../../test/fixtures/specs"
	goldenRef = "../../test/fixtures/specs/golden.json"
	prjconf   = "../../test/fixtures/prjconf/SUSE_SLFO_1.2.prjconf"
)

// TestGoldenCorpus checks the curated spec corpus against the golden file that
// the Perl oracle (test/oracle, -update) generated from Build::parse.
func TestGoldenCorpus(t *testing.T) {
	cfg, _, err := recipetest.LoadConfig(prjconf, "SUSE:SLFO:1.2", "standard", "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(goldenRef)
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string]recipetest.Result{}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	cases, err := recipetest.Cases(specDir, "standard")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, c := range cases {
		if c.Path == "" {
			continue
		}
		seen++
		t.Run(c.Name, func(t *testing.T) {
			want, ok := golden[c.Name]
			if !ok {
				t.Fatalf("no golden entry for %s", c.Name)
			}
			data, err := os.ReadFile(c.Path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := recipe.Parse(cfg, c.RecipeFile, data, recipe.Options{Package: c.Package, Flavor: c.Flavor})
			if err != nil {
				t.Fatal(err)
			}
			if got := recipetest.FromInfo(info); !reflect.DeepEqual(got, want) {
				t.Errorf("mismatch\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
	if seen != len(golden) {
		t.Errorf("golden has %d entries, corpus has %d units", len(golden), seen)
	}
}
