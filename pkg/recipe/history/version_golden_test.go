package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

// TestVersionExtractionGolden pins the versions the engine reads from spec
// preambles to what the OBS Perl parser (Build::parse_typed) yields: macros
// that are not defined stay literal, %if/%else picks the live branch, and
// %include is not followed.
func TestVersionExtractionGolden(t *testing.T) {
	cfg, err := buildconfig.Parse("", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec string
		want string
	}{
		{"undefined macro stays literal", "Name: foo\nVersion: 2+git20240805.fd24d50%{git_version}\n", "2+git20240805.fd24d50%{git_version}"},
		{"define", "Name: foo\n%define ver 3.1\nVersion: %{ver}\n", "3.1"},
		{"if else takes else for an undefined condition", "Name: foo\n%if 0%{?suse_version} > 1\nVersion: 1.0\n%else\nVersion: 2.0\n%endif\n", "2.0"},
		{"if takes the live branch", "Name: foo\n%define suse_version 1600\n%if 0%{?suse_version} > 1\nVersion: 1.0\n%else\nVersion: 2.0\n%endif\n", "1.0"},
		{"conditional macro with a defined name", "Name: foo\n%define undef 1\nVersion: %{?undef}1.0\n", "11.0"},
		{"conditional macro with an undefined name", "Name: foo\nVersion: %{?undef}1.0\n", "1.0"},
		{"include is ignored", "Name: foo\n%include common.inc\nVersion: 4.0\n", "4.0"},
	}
	for _, tc := range tests {
		v := judge(cfg, "foo", candidate{Name: "foo.spec"}, []byte(tc.spec))
		if v.Version != tc.want {
			t.Errorf("%s: version %q, want %q", tc.name, v.Version, tc.want)
		}
	}
}

func TestSweepTempRemovesOnlyStaleHistoryDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	old := time.Now().Add(-2 * time.Hour)
	for name, mod := range map[string]time.Time{"bsgo-history-old": old, "bsgo-history-new": time.Now(), "other-old": old} {
		if err := os.Mkdir(filepath.Join(tmp, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(tmp, name), mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	n, err := SweepTemp(time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v; want 1", n, err)
	}
	for name, want := range map[string]bool{"bsgo-history-old": false, "bsgo-history-new": true, "other-old": true} {
		_, err := os.Stat(filepath.Join(tmp, name))
		if (err == nil) != want {
			t.Errorf("%s exists %v, want %v", name, err == nil, want)
		}
	}
}
