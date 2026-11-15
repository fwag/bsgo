package solv_test

import (
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

const sumA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const sumB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func testInfos() []bininfo.BinInfo {
	return []bininfo.BinInfo{
		{
			Name: "libfoo1", Version: "1.0", Release: "1", Arch: "x86_64", Filename: "libfoo1.rpm", SHA256: sumA,
			Provides: []bininfo.Dep{{Name: "libfoo.so.1()(64bit)"}, {Name: "libfoo1", Op: "=", EVR: "1.0-1"}},
		},
		{
			Name: "app", Epoch: "1", Version: "2.0", Release: "3", Arch: "x86_64", Filename: "app.rpm", SHA256: sumB,
			Requires: []bininfo.Dep{
				{Name: "libfoo.so.1()(64bit)"},
				{Name: "rpmlib(PayloadIsZstd)", Op: "<=", EVR: "5.4.18-1"},
				{Name: "/bin/sh", Pre: true},
			},
			Conflicts: []bininfo.Dep{{Name: "oldapp", Op: "<", EVR: "1.0"}},
			Obsoletes: []bininfo.Dep{{Name: "ancientapp"}},
		},
		{
			Name: "shell", Version: "1", Release: "1", Arch: "noarch", Filename: "shell.rpm", SHA256: sumA,
			Provides: []bininfo.Dep{{Name: "/bin/sh"}},
		},
		{Name: "app", Version: "2.0", Release: "3", Arch: "src", Filename: "app.src.rpm", SHA256: sumB},
	}
}

func TestBuildSolvExpansion(t *testing.T) {
	blob, err := solv.BuildSolv("x86_64", testInfos())
	if err != nil {
		t.Fatalf("BuildSolv: %v", err)
	}
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "idx", Data: blob}})
	if err != nil {
		t.Fatalf("NewPathPool: %v", err)
	}
	defer pp.Close()

	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr string
	}{
		{name: "soname resolved through bininfo", args: []string{"app"}, want: []string{"app", "libfoo1", "shell"}},
		{name: "provides lookup", args: []string{"libfoo.so.1()(64bit)"}, want: []string{"libfoo1"}},
		{name: "versioned provide", args: []string{"libfoo1 >= 1.0"}, want: []string{"libfoo1"}},
		{name: "missing provider", args: []string{"nothing-here"}, wantErr: "nothing provides nothing-here"},
		{name: "source rpm not indexed", args: []string{"app"}, want: []string{"app", "libfoo1", "shell"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := pp.Expand(nil, tc.args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if got := sortedPkgNames(res); got != strings.Join(tc.want, " ") {
				t.Errorf("got %s, want %v", got, tc.want)
			}
			for _, p := range res.Packages {
				if p.SHA256 == "" {
					t.Errorf("%s has no sha256 checksum", p.Name)
				}
			}
		})
	}
}

func TestBuildSolvDeterministicAndEVR(t *testing.T) {
	a, err := solv.BuildSolv("x86_64", testInfos())
	if err != nil {
		t.Fatal(err)
	}
	rev := testInfos()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	b, err := solv.BuildSolv("x86_64", rev)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("blob depends on input order")
	}
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "idx", Data: a}})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	res, err := pp.Expand(nil, "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Packages {
		if p.Name == "app" && (p.EVR != "1:2.0-3" || p.SHA256 != sumB) {
			t.Errorf("app = %+v", p)
		}
	}
}

func TestBuildSolvErrors(t *testing.T) {
	tests := []struct {
		name  string
		arch  string
		infos []bininfo.BinInfo
	}{
		{"no arch", "", testInfos()},
		{"incomplete identity", "x86_64", []bininfo.BinInfo{{Name: "x"}}},
		{"bad dependency", "x86_64", []bininfo.BinInfo{{
			Name: "x", Version: "1", Release: "1", Arch: "noarch",
			Requires: []bininfo.Dep{{Name: "(unbalanced"}},
		}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := solv.BuildSolv(tc.arch, tc.infos); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
