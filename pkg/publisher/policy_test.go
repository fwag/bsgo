package publisher_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestManifestPolicy(t *testing.T) {
	prj := func(target manifest.BuildTarget, cfg *manifest.ProjectConfig) *manifest.ProjectManifest {
		return &manifest.ProjectManifest{
			SchemaVersion: "v1",
			Project:       manifest.ProjectInfo{Name: "SUSE:SLFO:1.2"},
			PublicConfig:  cfg,
			Targets:       map[string]manifest.BuildTarget{"standard": target},
		}
	}
	filter := &manifest.ProjectConfig{RawConfig: "ExportFilter: \\.i586\\.rpm$ i586\n"}
	tests := []struct {
		name string
		prj  *manifest.ProjectManifest
		prpa buildrepo.Prpa
		want publisher.Policy
		err  error
	}{
		{
			name: "flags and export filter",
			prj: prj(manifest.BuildTarget{
				Architectures: []string{"x86_64"},
				Flags:         manifest.TargetFlags{Build: true, Publish: true, Debuginfo: true},
			}, filter),
			prpa: testPrpa,
			want: publisher.Policy{Publish: true, Debuginfo: true, RPMMD: true, ExportFilter: map[string][]string{`\.i586\.rpm$`: {"i586"}}},
		},
		{
			name: "publish off needs no configuration",
			prj:  prj(manifest.BuildTarget{Architectures: []string{"x86_64"}}, filter),
			prpa: testPrpa,
			want: publisher.Policy{RPMMD: true},
		},
		{
			name: "repo type none",
			prj: prj(manifest.BuildTarget{
				Architectures: []string{"x86_64"}, Flags: manifest.TargetFlags{Publish: true},
				Config: &manifest.ProjectConfig{RepoTypes: []string{"none"}},
			}, nil),
			prpa: testPrpa,
			want: publisher.Policy{Publish: true},
		},
		{
			name: "architecture not in target",
			prj:  prj(manifest.BuildTarget{Architectures: []string{"aarch64"}}, nil),
			prpa: testPrpa,
			err:  publisher.ErrUnknownTarget,
		},
		{
			name: "unknown repository",
			prj:  prj(manifest.BuildTarget{Architectures: []string{"x86_64"}}, nil),
			prpa: buildrepo.Prpa{Project: testPrpa.Project, Repo: "other", Arch: "x86_64"},
			err:  publisher.ErrUnknownTarget,
		},
		{
			name: "unknown project",
			prj:  nil,
			prpa: testPrpa,
			err:  publisher.ErrUnknownTarget,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storage.NewMemoryStore()
			if tt.prj != nil {
				if err := store.CreateProject(context.Background(), tt.prj); err != nil {
					t.Fatal(err)
				}
			}
			got, err := publisher.ManifestPolicy{Projects: store}.Policy(context.Background(), tt.prpa)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error %v, want %v", err, tt.err)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("policy %+v, want %+v", got, tt.want)
			}
		})
	}
}
