package solv_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

// semantics returns the provides and requires of a blob, loaded back through
// libsolv.
func semantics(t *testing.T, blob []byte) (provs, reqs []string) {
	t.Helper()
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.LoadRepoFromMemory("idx", blob); err != nil {
		t.Fatal(err)
	}
	return pool.AllProvides(), pool.AllRequires()
}

func randomUnitBinaries(r *rand.Rand, unit string, gen int) []bininfo.BinInfo {
	n := 1 + r.Intn(4)
	out := make([]bininfo.BinInfo, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s-sub%d", unit, i)
		b := bininfo.BinInfo{
			Name: name, Version: fmt.Sprintf("%d", gen), Release: "1", Arch: "x86_64",
			Filename: fmt.Sprintf("%s-%d.rpm", name, gen), SHA256: sumA,
			Provides: []bininfo.Dep{{Name: fmt.Sprintf("lib%s.so.%d()(64bit)", name, gen)}},
			Files:    []string{"/usr/bin/" + name},
		}
		for j := 0; j < r.Intn(4); j++ {
			b.Requires = append(b.Requires, bininfo.Dep{Name: fmt.Sprintf("u%d-sub0", r.Intn(6)), Pre: j == 0})
		}
		if r.Intn(3) == 0 {
			b.Conflicts = []bininfo.Dep{{Name: "other", Op: "<", EVR: "2"}}
		}
		out = append(out, b)
	}
	return out
}

func TestUpdateSolvMatchesFullRebuild(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			current := map[string][]bininfo.BinInfo{}
			var blob []byte
			for step := 0; step < 25; step++ {
				unit := fmt.Sprintf("u%d", r.Intn(6))
				if r.Intn(5) == 0 {
					unit += ":flavor"
				}
				var add []bininfo.BinInfo
				if r.Intn(6) != 0 {
					add = randomUnitBinaries(r, unit, step)
				}
				var err error
				blob, err = solv.UpdateSolv("x86_64", blob, unit, add)
				if err != nil {
					t.Fatalf("step %d: %v", step, err)
				}
				if len(add) == 0 {
					delete(current, unit)
				} else {
					current[unit] = add
				}
				assertEquivalent(t, blob, current)
			}
		})
	}
}

func assertEquivalent(t *testing.T, blob []byte, current map[string][]bininfo.BinInfo) {
	t.Helper()
	var all []solv.UnitBinary
	for u, bins := range current {
		for _, b := range bins {
			all = append(all, solv.UnitBinary{Unit: u, BinInfo: b})
		}
	}
	full, err := solv.BuildUnitSolv("x86_64", all)
	if err != nil {
		t.Fatal(err)
	}
	gp, gr := semantics(t, blob)
	wp, wr := semantics(t, full)
	if !slices.Equal(gp, wp) {
		t.Fatalf("provides differ:\nincremental %v\nfull        %v", gp, wp)
	}
	if !slices.Equal(gr, wr) {
		t.Fatalf("requires differ:\nincremental %v\nfull        %v", gr, wr)
	}
}

func TestUpdateSolvDeterministicAndOrderOfOtherUnits(t *testing.T) {
	a := []bininfo.BinInfo{{Name: "a", Version: "1", Release: "1", Arch: "x86_64", Filename: "a.rpm", SHA256: sumA}}
	b := []bininfo.BinInfo{{Name: "b", Version: "1", Release: "1", Arch: "x86_64", Filename: "b.rpm", SHA256: sumB}}
	build := func() []byte {
		blob, err := solv.UpdateSolv("x86_64", nil, "a", a)
		if err != nil {
			t.Fatal(err)
		}
		if blob, err = solv.UpdateSolv("x86_64", blob, "b", b); err != nil {
			t.Fatal(err)
		}
		if blob, err = solv.UpdateSolv("x86_64", blob, "a", a); err != nil {
			t.Fatal(err)
		}
		return blob
	}
	if !reflect.DeepEqual(build(), build()) {
		t.Fatal("same update sequence produced different blobs")
	}
}

func TestUpdateSolvErrors(t *testing.T) {
	tests := []struct {
		name    string
		arch    string
		current []byte
		unit    string
	}{
		{"no arch", "", nil, "u"},
		{"no unit", "x86_64", nil, ""},
		{"corrupt blob", "x86_64", []byte("not a solv file"), "u"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := solv.UpdateSolv(tt.arch, tt.current, tt.unit, nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
