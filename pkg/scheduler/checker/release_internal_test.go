package checker

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

func TestReleaseBuildTypeOverride(t *testing.T) {
	tests := []struct {
		name      string
		list      []string
		buildType string
		want      string
	}{
		{"default template", nil, "spec", "3.7"},
		{"single field", []string{"<CI_CNT>.<B_CNT>"}, "spec", "3.7"},
		{"spec override (SLFO)", []string{"<CI_CNT>.<B_CNT>", "spec:160000.<CI_CNT>.<B_CNT>"}, "spec", "160000.3.7"},
		{"override for another type", []string{"<CI_CNT>.<B_CNT>", "kiwi:k<B_CNT>"}, "spec", "3.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &buildconfig.Config{Release: "<CI_CNT>.<B_CNT>", ReleaseList: tt.list}
			if len(tt.list) > 0 {
				cfg.Release = tt.list[0]
			}
			ps := &pass{sched: cfg}
			if got := ps.release(tt.buildType, 3, 7); got != tt.want {
				t.Errorf("release = %q, want %q", got, tt.want)
			}
		})
	}
}
