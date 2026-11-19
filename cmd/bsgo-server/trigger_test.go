package main

import (
	"context"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestRecordForce(t *testing.T) {
	tests := []struct {
		name string
		trig v1.BuildTrigger
		want []storage.ForcedRebuild
	}{
		{name: "not forced", trig: v1.BuildTrigger{Project: "P", Package: "gcc", Priority: "high"}},
		{
			name: "plain package",
			trig: v1.BuildTrigger{Project: "SUSE:SLFO:1.2", Package: "gcc", ForceRebuild: true},
			want: []storage.ForcedRebuild{{Project: "SUSE:SLFO:1.2", Package: "gcc", Priority: "high"}},
		},
		{
			name: "explicit tier",
			trig: v1.BuildTrigger{Project: "P", Package: "gcc", ForceRebuild: true, Priority: "med"},
			want: []storage.ForcedRebuild{{Project: "P", Package: "gcc", Priority: "med"}},
		},
		{
			name: "flavor field",
			trig: v1.BuildTrigger{Project: "SUSE:SLFO:1.2", Package: "gcc", Flavor: "build", ForceRebuild: true},
			want: []storage.ForcedRebuild{{Project: "SUSE:SLFO:1.2", Package: "gcc", Flavor: "build", Priority: "high"}},
		},
		{
			name: "legacy pkg:flavor package",
			trig: v1.BuildTrigger{Project: "P", Package: "gcc:build", ForceRebuild: true},
			want: []storage.ForcedRebuild{{Project: "P", Package: "gcc", Flavor: "build", Priority: "high"}},
		},
		{
			name: "repo and arch filters",
			trig: v1.BuildTrigger{Project: "P", Package: "gcc", Flavor: "build", Target: "standard", Architecture: "x86_64", ForceRebuild: true},
			want: []storage.ForcedRebuild{{Project: "P", Package: "gcc", Flavor: "build", Repo: "standard", Arch: "x86_64", Priority: "high"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := storage.NewMemoryStore()
			if err := recordForce(ctx, store, &tt.trig); err != nil {
				t.Fatal(err)
			}
			got, err := store.ListForcedRebuilds(ctx, tt.trig.Project)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d rows, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, w := range tt.want {
				g := got[i]
				g.RequestedAt = w.RequestedAt
				if g != w {
					t.Errorf("row %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}
