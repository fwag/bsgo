package storage

import "testing"

func TestForcedRebuildApplies(t *testing.T) {
	tests := []struct {
		name string
		f    ForcedRebuild
		repo string
		arch string
		u    UnitRef
		want bool
	}{
		{"plain matches plain", ForcedRebuild{Package: "gcc"}, "std", "x86_64", UnitRef{Package: "gcc"}, true},
		{"plain skips flavor", ForcedRebuild{Package: "gcc"}, "std", "x86_64", UnitRef{Package: "gcc", Flavor: "build"}, false},
		{"flavor skips plain", ForcedRebuild{Package: "gcc", Flavor: "build"}, "std", "x86_64", UnitRef{Package: "gcc"}, false},
		{"flavor matches flavor", ForcedRebuild{Package: "gcc", Flavor: "build"}, "std", "x86_64", UnitRef{Package: "gcc", Flavor: "build"}, true},
		{"other flavor", ForcedRebuild{Package: "gcc", Flavor: "build"}, "std", "x86_64", UnitRef{Package: "gcc", Flavor: "test"}, false},
		{"other package", ForcedRebuild{Package: "gcc"}, "std", "x86_64", UnitRef{Package: "glibc"}, false},
		{"repo filter hit", ForcedRebuild{Package: "gcc", Repo: "std"}, "std", "x86_64", UnitRef{Package: "gcc"}, true},
		{"repo filter miss", ForcedRebuild{Package: "gcc", Repo: "std"}, "other", "x86_64", UnitRef{Package: "gcc"}, false},
		{"arch filter miss", ForcedRebuild{Package: "gcc", Arch: "aarch64"}, "std", "x86_64", UnitRef{Package: "gcc"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Applies(tt.repo, tt.arch, tt.u); got != tt.want {
				t.Errorf("Applies = %t, want %t", got, tt.want)
			}
		})
	}
}
