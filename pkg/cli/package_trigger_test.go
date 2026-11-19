package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestParsePackageRef(t *testing.T) {
	tests := []struct {
		arg     string
		want    packageRef
		wantErr bool
	}{
		{arg: "SUSE:SLFO:1.2/gcc", want: packageRef{"SUSE:SLFO:1.2", "gcc", ""}},
		{arg: "SUSE:SLFO:1.2/gcc:build", want: packageRef{"SUSE:SLFO:1.2", "gcc", "build"}},
		{arg: "home:bot/pkg:fl", want: packageRef{"home:bot", "pkg", "fl"}},
		{arg: "gcc", wantErr: true},
		{arg: "/gcc", wantErr: true},
		{arg: "P/", wantErr: true},
		{arg: "P/gcc:", wantErr: true},
		{arg: "P/:build", wantErr: true},
		{arg: "P/gcc:a:b", wantErr: true},
		{arg: "P/gcc:a/b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			got, err := parsePackageRef(tt.arg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPackageTriggerFlavor(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	const prj = "SUSE:SLFO:1.2"
	if err := store.UpsertPackage(ctx, prj, &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "gcc"},
		Source:        manifest.PackageSource{Git: "https://git/gcc", Commit: "abc"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, fl := range []string{"", "build", "test"} {
		if err := store.UpsertBuildState(ctx, &storage.PackageBuildState{
			Project: prj, Repo: "standard", Arch: "x86_64", Package: "gcc", Flavor: fl, State: "succeeded",
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		args       []string
		wantFlavor string
		wantSubj   string
		wantErr    string
	}{
		{"plain", []string{prj + "/gcc"}, "", "bsgo.triggers.low." + prj + ".gcc", ""},
		{"flavor", []string{prj + "/gcc:build"}, "build", "bsgo.triggers.low." + prj + ".gcc:build", ""},
		{"priority high", []string{prj + "/gcc", "--priority", "high"}, "", "bsgo.triggers.high." + prj + ".gcc", ""},
		{"invalid priority", []string{prj + "/gcc", "--priority", "urgent"}, "", "", "invalid --priority"},
		{"unknown flavor", []string{prj + "/gcc:nope"}, "", "", "unknown flavor"},
		{"unknown flavor skipped", []string{prj + "/gcc:nope", "--skip-flavor-check"}, "nope", "bsgo.triggers.low." + prj + ".gcc:nope", ""},
		{"unknown package", []string{prj + "/nope:build"}, "", "", "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewRootCommand(store)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(append([]string{"package", "trigger", "--dry-run", "--json", "--repo", "standard", "--arch", "x86_64"}, tt.args...))
			err := root.Execute()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var res map[string]any
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			if got, _ := res["flavor"].(string); got != tt.wantFlavor {
				t.Errorf("flavor = %q, want %q", got, tt.wantFlavor)
			}
			if res["subject"] != tt.wantSubj || res["package"] != "gcc" || res["repo"] != "standard" || res["arch"] != "x86_64" {
				t.Errorf("unexpected result %v", res)
			}
		})
	}
}
