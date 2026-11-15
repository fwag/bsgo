package worker

import (
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestJobTargetArchRequiresBoth(t *testing.T) {
	tests := []struct {
		name    string
		job     *v1.BuildJob
		target  string
		arch    string
		wantErr bool
	}{
		{"both set", &v1.BuildJob{Target: "t", Architecture: "a"}, "t", "a", false},
		{"no target", &v1.BuildJob{Architecture: "a"}, "", "", true},
		{"no arch", &v1.BuildJob{Target: "t"}, "", "", true},
		{"nil job", nil, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, arch, err := jobTargetArch(tc.job)
			if (err != nil) != tc.wantErr || target != tc.target || arch != tc.arch {
				t.Errorf("got (%q, %q, %v)", target, arch, err)
			}
		})
	}
}
