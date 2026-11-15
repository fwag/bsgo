//go:build ignore

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	ctx := context.Background()
	out := "docs/guides/examples/build-repo-demo/out"
	must(os.MkdirAll(out, 0o755))
	rpms, _ := filepath.Glob(os.Getenv("DEMO_RPMS") + "/*.rpm")
	sort.Strings(rpms)
	units := map[string][]bininfo.BinInfo{}
	for _, r := range rpms {
		bi, err := bininfo.Extract(ctx, r)
		must(err)
		unit := bininfo.SourceName(bi.SourceRPM)
		units[unit] = append(units[unit], bi)
	}
	prpa := buildrepo.Prpa{Project: "home:demo", Repo: "standard", Arch: "x86_64"}
	var entries []buildrepo.Entry
	var ubs []solv.UnitBinary
	for _, unit := range []string{"hello", "libgreet"} {
		raw, err := bininfo.Marshal(units[unit])
		must(err)
		must(os.WriteFile(filepath.Join(out, unit+".bininfo.json"), raw, 0o644))
		for _, b := range units[unit] {
			key := buildrepo.ArtifactPrefix(prpa, unit) + b.Filename
			entries = append(entries, buildrepo.NewEntry(unit, key, b))
			ubs = append(ubs, solv.UnitBinary{Unit: unit, BinInfo: b})
		}
	}
	raw, err := buildrepo.EncodeEntries(entries)
	must(err)
	must(os.WriteFile(filepath.Join(out, "entries.json"), raw, 0o644))
	blob, err := solv.BuildUnitSolv("x86_64", ubs)
	must(err)
	must(os.WriteFile(filepath.Join(out, "full.solv"), blob, 0o644))

	// The scheduler expands hello's BuildRequires against the full solv.
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "home:demo/standard", Hash: "x", Data: blob}})
	must(err)
	defer pp.Close()
	res, err := pp.GetBuild(&solv.ExpandConfig{}, nil, []string{"pkgconfig(greet) >= 1.0"})
	must(err)
	fmt.Println("== expansion of hello's BuildRequires: pkgconfig(greet) >= 1.0")
	var ids []string
	for _, p := range res.Packages {
		fmt.Printf("  bdep %-16s %s.%s sha256=%s\n", p.Name, p.EVR, p.Arch, p.SHA256[:16])
		ids = append(ids, p.SHA256)
	}
	sort.Strings(ids)
	commit := "8acaba8fdfdbb81588ab597901a5e668cdb9ea7332506428610634117d428f8a"
	h := sha256.New()
	h.Write([]byte("bsgo-meta-v1\n" + commit + "\n"))
	for _, id := range ids {
		h.Write([]byte(id + "\n"))
	}
	fmt.Println("== meta of hello = sha256(bsgo-meta-v1, source commit, sorted bdep sha256s)")
	fmt.Println("  ", hex.EncodeToString(h.Sum(nil)))
	fmt.Printf("== files written to %s, solv blob %d bytes\n", out, len(blob))
}
