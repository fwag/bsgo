package indexer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

const (
	benchUnits    = 3000
	benchPerUnit  = 5 // 15000 binaries
	benchProvides = 25
	benchRequires = 12
	benchFiles    = 60
)

func benchBinary(unit, sub, gen int) bininfo.BinInfo {
	name := fmt.Sprintf("pkg%d-sub%d", unit, sub)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", name, gen)))
	b := bininfo.BinInfo{
		Name: name, Version: fmt.Sprintf("1.%d", gen), Release: "1.1", Arch: "x86_64",
		Filename: fmt.Sprintf("%s-1.%d-1.1.x86_64.rpm", name, gen), SHA256: hex.EncodeToString(sum[:]), Size: 123456,
		SourceRPM: fmt.Sprintf("pkg%d-1.%d-1.1.src.rpm", unit, gen),
	}
	for i := 0; i < benchProvides; i++ {
		b.Provides = append(b.Provides, bininfo.Dep{Name: fmt.Sprintf("%s-cap%d", name, i), Op: "=", EVR: b.Version})
	}
	for i := 0; i < benchRequires; i++ {
		b.Requires = append(b.Requires, bininfo.Dep{Name: fmt.Sprintf("pkg%d-sub0", (unit+i*7)%benchUnits)})
	}
	for i := 0; i < benchFiles; i++ {
		b.Files = append(b.Files, fmt.Sprintf("/usr/share/%s/file%d", name, i))
	}
	b.Files = append(b.Files, "/usr/bin/"+name)
	return b
}

func benchUnit(unit, gen int) []bininfo.BinInfo {
	out := make([]bininfo.BinInfo, benchPerUnit)
	for s := range out {
		out[s] = benchBinary(unit, s, gen)
	}
	return out
}

// legacyEntry is the pre-slimming entry: identity plus the full bininfo.
type legacyEntry struct {
	Unit string `json:"unit"`
	Key  string `json:"key"`
	bininfo.BinInfo
}

type benchWorld struct {
	e       *env
	units   []string
	current [][]bininfo.BinInfo
}

func newBenchWorld(b *testing.B) *benchWorld {
	b.Helper()
	e := newEnv(b)
	w := &benchWorld{e: e}
	for u := 0; u < benchUnits; u++ {
		unit := fmt.Sprintf("pkg%d", u)
		infos := benchUnit(u, 0)
		w.units = append(w.units, unit)
		w.current = append(w.current, infos)
		prefix := buildrepo.ArtifactPrefix(e.prpa, unit)
		raw, err := bininfo.Marshal(infos)
		if err != nil {
			b.Fatal(err)
		}
		if err := e.blobs.PutObject(context.Background(), prefix+bininfo.FileName, raw); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := e.ix.Rebuild(context.Background(), e.prpa, w.units, false); err != nil {
		b.Fatal(err)
	}
	return w
}

// BenchmarkIntegrateOneUnit15k measures the work of one finished job against
// a 15000-binary index: the incremental path (UpdateSolv plus slim entries,
// the code Integrate runs) against the former full rebuild.
func BenchmarkIntegrateOneUnit15k(b *testing.B) {
	w := newBenchWorld(b)
	ctx := context.Background()
	cur, err := w.e.ix.Reader.Index(ctx, w.e.prpa)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("new", func(b *testing.B) {
		var written int
		for i := 0; i < b.N; i++ {
			infos := benchUnit(1, i+1)
			if _, err := w.e.integrate("pkg1", w.e.upload("pkg1", infos...)); err != nil {
				b.Fatal(err)
			}
			idx, err := w.e.ix.Reader.Index(ctx, w.e.prpa)
			if err != nil {
				b.Fatal(err)
			}
			raw, err := buildrepo.EncodeEntries(idx.Entries)
			if err != nil {
				b.Fatal(err)
			}
			written = len(idx.Solv) + len(raw)
		}
		b.ReportMetric(float64(written), "bytes-written/op")
	})
	b.Run("old", func(b *testing.B) {
		var written int
		for i := 0; i < b.N; i++ {
			var infos []bininfo.BinInfo
			var entries []legacyEntry
			for u, cinfos := range w.current {
				use := cinfos
				if u == 1 {
					use = benchUnit(1, i+1)
				}
				for _, bi := range use {
					infos = append(infos, bi)
					entries = append(entries, legacyEntry{Unit: w.units[u], Key: "k/" + bi.Filename, BinInfo: bi})
				}
			}
			blob, err := solv.BuildSolv("x86_64", infos)
			if err != nil {
				b.Fatal(err)
			}
			raw, err := json.Marshal(entries)
			if err != nil {
				b.Fatal(err)
			}
			written = len(blob) + len(raw)
		}
		b.ReportMetric(float64(written), "bytes-written/op")
	})
	b.Run("read-slim-entries", func(b *testing.B) {
		raw, err := buildrepo.EncodeEntries(cur.Entries)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(len(raw)), "entries-bytes")
		for i := 0; i < b.N; i++ {
			var es []buildrepo.Entry
			if err := json.Unmarshal(raw, &es); err != nil {
				b.Fatal(err)
			}
		}
	})
}
