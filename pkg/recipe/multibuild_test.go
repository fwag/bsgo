package recipe_test

import (
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/recipe"
)

func TestParseMultibuild(t *testing.T) {
	tests := []struct {
		name    string
		xml     string
		want    recipe.Multibuild
		wantErr bool
	}{
		{"flavors", "<multibuild><flavor>a</flavor><flavor>b</flavor></multibuild>",
			recipe.Multibuild{BuildEmptyFlavor: true, Flavors: []string{"a", "b"}}, false},
		{"package alias", "<multibuild><package>docs</package></multibuild>",
			recipe.Multibuild{BuildEmptyFlavor: true, Flavors: []string{"docs"}}, false},
		{"no empty flavor", `<multibuild buildemptyflavor="false"><flavor>a</flavor></multibuild>`,
			recipe.Multibuild{BuildEmptyFlavor: false, Flavors: []string{"a"}}, false},
		{"zero", `<multibuild buildemptyflavor="0"><flavor>a</flavor></multibuild>`,
			recipe.Multibuild{BuildEmptyFlavor: false, Flavors: []string{"a"}}, false},
		{"empty", "<multibuild/>", recipe.Multibuild{BuildEmptyFlavor: true}, false},
		{"bad boolean", `<multibuild buildemptyflavor="maybe"/>`, recipe.Multibuild{}, true},
		{"both element kinds", "<multibuild><flavor>a</flavor><package>b</package></multibuild>", recipe.Multibuild{}, true},
		{"colon in flavor", "<multibuild><flavor>a:b</flavor></multibuild>", recipe.Multibuild{}, true},
		{"not xml", "<multibuild", recipe.Multibuild{}, true},
		{"wrong root", "<other/>", recipe.Multibuild{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recipe.ParseMultibuild([]byte(tt.xml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestFindRecipe(t *testing.T) {
	tests := []struct {
		name   string
		lookup string
		repo   string
		files  []string
		want   string
	}{
		{"repo specific wins", "foo", "standard", []string{"foo.spec", "foo-standard.spec"}, "foo-standard.spec"},
		{"plain name", "foo", "standard", []string{"foo.spec", "bar.spec"}, "foo.spec"},
		{"strip dotted components", "foo.1.2", "r", []string{"foo.spec", "x.spec"}, "foo.spec"},
		{"single spec fallback", "foo", "r", []string{"other.spec", "_service"}, "other.spec"},
		{"prefix disambiguation", "foo", "r", []string{"foo-a.spec", "bar.spec"}, "foo-a.spec"},
		{"ambiguous with repo", "foo", "r", []string{"a.spec", "b.spec"}, ""},
		{"ambiguous without repo sorts", "foo", "", []string{"foo-b.spec", "foo-a.spec"}, "foo-a.spec"},
		{"repo prefix disambiguation", "foo", "r", []string{"foo-r1.spec", "foo-x.spec"}, "foo-r1.spec"},
		{"no spec", "foo", "r", []string{"README"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recipe.FindRecipe(tt.lookup, tt.repo, "spec", tt.files); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpand(t *testing.T) {
	files := []string{"gcc13.spec", "cross-arm-gcc13.spec", "cross-arm-gcc13-standard.spec", "other.spec", "_multibuild"}
	mb := &recipe.Multibuild{BuildEmptyFlavor: true, Flavors: []string{"cross-arm-gcc13", "mini"}}
	got := recipe.Expand("gcc13", files, mb, "standard")
	want := []recipe.Unit{
		{Name: "gcc13", Package: "gcc13", RecipeFile: "gcc13.spec"},
		{Name: "gcc13:cross-arm-gcc13", Package: "gcc13", Flavor: "cross-arm-gcc13", RecipeFile: "cross-arm-gcc13-standard.spec"},
		{Name: "gcc13:mini", Package: "gcc13", Flavor: "mini", RecipeFile: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}

	noEmpty := &recipe.Multibuild{BuildEmptyFlavor: false, Flavors: []string{"mini"}}
	got = recipe.Expand("systemd", []string{"systemd.spec"}, noEmpty, "standard")
	want = []recipe.Unit{{Name: "systemd:mini", Package: "systemd", Flavor: "mini", RecipeFile: "systemd.spec"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildemptyflavor=false: got %+v\nwant %+v", got, want)
	}

	got = recipe.Expand("solo", []string{"solo.spec"}, nil, "standard")
	if len(got) != 1 || got[0].Name != "solo" || got[0].RecipeFile != "solo.spec" {
		t.Errorf("no multibuild: got %+v", got)
	}
}
