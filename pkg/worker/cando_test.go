package worker

import (
	"reflect"
	"testing"
)

func TestDefaultArchsAndBuildArchs(t *testing.T) {
	tests := []struct {
		name string
		cfg  WorkerConfig
		want []string
	}{
		{"x86_64 host builds the 32 bit archs", WorkerConfig{Architecture: "x86_64"}, []string{"x86_64", "i586", "i686"}},
		{"explicit list wins", WorkerConfig{Architecture: "x86_64", Archs: []string{"x86_64", "i586"}}, []string{"x86_64", "i586"}},
		{"i586 host", WorkerConfig{Architecture: "i586"}, []string{"i586"}},
		{"unknown host builds itself", WorkerConfig{Architecture: "foo"}, []string{"foo"}},
	}
	for _, tt := range tests {
		if got := tt.cfg.BuildArchs(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: BuildArchs() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPersonalityCommand(t *testing.T) {
	tests := []struct {
		host, arch, wantHelper string
	}{
		{"x86_64", "i586", "linux32"},
		{"x86_64", "i686", "linux32"},
		{"x86_64", "x86_64", ""},
		{"i586", "i586", ""},
		{"aarch64", "armv7l", "linux32"},
	}
	for _, tt := range tests {
		helper := PersonalityHelper(tt.host, tt.arch)
		if helper != tt.wantHelper {
			t.Errorf("PersonalityHelper(%s, %s) = %q, want %q", tt.host, tt.arch, helper, tt.wantHelper)
		}
		bin, args := personalityCommand(helper, "/usr/bin/build", []string{"--arch", tt.arch})
		wantBin, wantArgs := "/usr/bin/build", []string{"--arch", tt.arch}
		if helper != "" {
			wantBin, wantArgs = helper, []string{"/usr/bin/build", "--arch", tt.arch}
		}
		if bin != wantBin || !reflect.DeepEqual(args, wantArgs) {
			t.Errorf("personalityCommand(%q) = %s %v", helper, bin, args)
		}
	}
}
