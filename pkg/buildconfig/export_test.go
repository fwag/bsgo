package buildconfig_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

func TestExportRouterSLFO(t *testing.T) {
	text, err := os.ReadFile("../../test/fixtures/prjconf/SUSE_SLFO_1.2.prjconf")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		arch, file string
		own        bool
		others     []string
	}{
		{"i586", "glibc-32bit-2.40-1.1.x86_64.rpm", false, []string{"x86_64"}},
		{"i586", "glibc-devel-32bit-2.40-1.1.x86_64.rpm", false, []string{"x86_64"}},
		{"i586", "glibc-2.40-1.1.i686.rpm", true, nil},
		{"i586", "foo-1-1.noarch.rpm", true, nil},
		{"i586", "foo-ia32-1-1.i586.rpm", false, nil},
		// OBS tries the rules in reverse text order, so the arch rule wins.
		{"i586", "foo-ia32-1-1.x86_64.rpm", false, []string{"x86_64"}},
		{"x86_64", "glibc-2.40-1.1.x86_64.rpm", true, nil},
		{"x86_64", "glibc-2.40-1.1.i686.rpm", true, nil},
		{"x86_64", "qemu-ipxe-1-1.noarch.rpm", true, []string{"armv7l", "i586", "ppc", "ppc64", "ppc64le", "riscv64", "s390x"}},
		{"i586", "acroread-9-1.i586.rpm", true, []string{"x86_64"}},
	}
	for _, tt := range tests {
		t.Run(tt.arch+"/"+tt.file, func(t *testing.T) {
			cfg, err := buildconfig.Parse(string(text), tt.arch)
			if err != nil {
				t.Fatal(err)
			}
			r, err := buildconfig.NewExportRouter(cfg.ExportFilter, tt.arch)
			if err != nil {
				t.Fatal(err)
			}
			own, others := r.Route(tt.file)
			if own != tt.own || !reflect.DeepEqual(others, tt.others) {
				t.Errorf("Route(%s) = %v %v, want %v %v", tt.file, own, others, tt.own, tt.others)
			}
		})
	}
}

func TestExportRouterDefaultsAndErrors(t *testing.T) {
	r, err := buildconfig.NewExportRouter(nil, "i586")
	if err != nil {
		t.Fatal(err)
	}
	if own, others := r.Route("a-32bit-1-1.x86_64.rpm"); own || !reflect.DeepEqual(others, []string{"x86_64"}) {
		t.Errorf("default i586 filter: %v %v", own, others)
	}
	if own, others := r.Route("a-debuginfo-1-1.i586.rpm"); own || others != nil {
		t.Errorf("default filter keeps debuginfo: %v %v", own, others)
	}
	if _, err := buildconfig.NewExportRouter(map[string][]string{"(": {"x86_64"}}, "i586"); err == nil {
		t.Error("invalid regular expression accepted")
	}
	// Reverse sort: "b" is tried before "a", so its rule decides.
	r, err = buildconfig.NewExportRouter(map[string][]string{"a": {"x86_64"}, "b": {"."}}, "i586")
	if err != nil {
		t.Fatal(err)
	}
	if own, others := r.Route("ab"); !own || others != nil {
		t.Errorf("first match must win: %v %v", own, others)
	}
}
