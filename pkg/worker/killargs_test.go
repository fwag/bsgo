package worker

import (
	"slices"
	"testing"
)

func TestKillArgs(t *testing.T) {
	tests := []struct {
		name, mode, disk string
		want             []string
	}{
		{"kvm passes the vm disk", "kvm", "/d.img", []string{"--root", "/r", "--vm-type", "kvm", "--vm-disk", "/d.img", "--kill"}},
		{"kvm without disk", "kvm", "", []string{"--root", "/r", "--kill"}},
		{"chroot", "obs-build", "/d.img", []string{"--root", "/r", "--kill"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := killArgs(tc.mode, "/r", tc.disk); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
