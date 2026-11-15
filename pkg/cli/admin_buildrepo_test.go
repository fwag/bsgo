package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestAdminBuildRepoRebuild(t *testing.T) {
	ctx := context.Background()
	p := buildrepo.Prpa{Project: "p:q", Repo: "standard", Arch: "x86_64"}
	tests := []struct {
		name      string
		args      []string
		units     []string
		wantErr   string
		wantObjs  bool
		wantUnits int
	}{
		{"dry run writes nothing", []string{"p:q", "standard", "x86_64", "--dry-run", "--json"}, []string{"a", "b:flavor"}, "", false, 2},
		{"rebuild writes the index", []string{"p:q", "standard", "x86_64", "--json"}, []string{"a"}, "", true, 1},
		{"no uploaded units", []string{"p:q", "standard", "x86_64", "--json"}, nil, "no unit has uploaded", false, 0},
		{"missing arguments", []string{"p:q"}, nil, "accepts 3 arg", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storage.NewMemoryStore()
			blobs := buildrepo.NewMemoryBlobs()
			for _, u := range tt.units {
				b := bininfo.BinInfo{Name: strings.SplitN(u, ":", 2)[0], Version: "1", Release: "1", Arch: "x86_64", Filename: "x.rpm", SHA256: "aa", Size: 1}
				raw, err := bininfo.Marshal([]bininfo.BinInfo{b})
				if err != nil {
					t.Fatal(err)
				}
				if err := blobs.PutObject(ctx, buildrepo.ArtifactPrefix(p, u)+bininfo.FileName, raw); err != nil {
					t.Fatal(err)
				}
			}
			before := len(blobs.Keys())
			open := func(context.Context, storage.Store) (*cli.BuildRepoAdmin, error) {
				return &cli.BuildRepoAdmin{
					Indexer: &indexer.Indexer{
						Pointers: store, Blobs: blobs, Reader: buildrepo.NewReader(store, blobs),
						Leases: &coord.Memory{}, Owner: "test",
					},
					ListKeys: func(context.Context, string) ([]string, error) { return blobs.Keys(), nil },
				}, nil
			}
			root := cli.NewRootCommand(store, cli.WithBuildRepoOpener(open))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(append([]string{"admin", "buildrepo", "rebuild"}, tt.args...))
			err := root.Execute()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var res indexer.RebuildResult
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("stdout is not JSON: %v: %s", err, out.String())
			}
			if res.Units != tt.wantUnits || res.DryRun == tt.wantObjs || res.Changed != tt.wantObjs {
				t.Errorf("result %+v", res)
			}
			if wrote := len(blobs.Keys()) > before; wrote != tt.wantObjs {
				t.Errorf("objects written = %v, want %v", wrote, tt.wantObjs)
			}
		})
	}
}
