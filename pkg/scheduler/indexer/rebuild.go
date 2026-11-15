package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

// RebuildResult describes a full index rebuild.
type RebuildResult struct {
	Prpa         string `json:"prpa"`
	Units        int    `json:"units"`
	Binaries     int    `json:"binaries"`
	SolvBytes    int    `json:"solv_bytes"`
	EntriesBytes int    `json:"entries_bytes"`
	OldHash      string `json:"old_hash"`
	NewHash      string `json:"new_hash"`
	// Changed reports whether the pointer moved; always false for a dry run.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run"`
}

// UnitsFromKeys extracts the unit names of prpa p from the object keys below
// ArtifactPrefix that are a unit's bininfo.json. The result is sorted.
func UnitsFromKeys(p buildrepo.Prpa, keys []string) []string {
	prefix := strings.TrimSuffix(buildrepo.ArtifactPrefix(p, ""), "/")
	var units []string
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		unit, file, ok := strings.Cut(rest, "/")
		if ok && unit != "" && file == bininfo.FileName {
			units = append(units, unit)
		}
	}
	sort.Strings(units)
	return units
}

// Rebuild is the repair path: it recreates the index of p from scratch out of
// the bininfo.json of every unit in units instead of updating the current
// blob incrementally. It writes nothing when dryRun is set.
func (ix *Indexer) Rebuild(ctx context.Context, p buildrepo.Prpa, units []string, dryRun bool) (*RebuildResult, error) {
	bins, entries, err := ix.collectUnits(ctx, p, units)
	if err != nil {
		return nil, err
	}
	blob, err := solv.BuildUnitSolv(p.Arch, bins)
	if err != nil {
		return nil, fmt.Errorf("index %s: %w", p, err)
	}
	raw, err := buildrepo.EncodeEntries(entries)
	if err != nil {
		return nil, err
	}
	res := &RebuildResult{
		Prpa: p.String(), Units: len(units), Binaries: len(entries), DryRun: dryRun,
		SolvBytes: len(blob), EntriesBytes: len(raw), NewHash: buildrepo.ContentHash(blob, raw),
	}
	if dryRun {
		cur, err := ix.Reader.Index(ctx, p)
		if err != nil {
			return nil, err
		}
		res.OldHash = cur.Hash
		return res, nil
	}
	lease, err := ix.lease(ctx, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lease.Release(context.WithoutCancel(ctx)) }()
	cur, err := ix.Reader.Index(ctx, p)
	if err != nil {
		return nil, err
	}
	res.OldHash = cur.Hash
	if res.Changed, err = ix.publish(ctx, p, cur, blob, entries); err != nil {
		return nil, err
	}
	if res.Changed && ix.Notify != nil {
		if err := ix.Notify.RepoChanged(ctx, p); err != nil {
			return res, fmt.Errorf("announce %s: %w", p, err)
		}
	}
	return res, nil
}

// collectUnits reads the bininfo.json of each unit and returns the binaries
// that belong into the build repository.
func (ix *Indexer) collectUnits(ctx context.Context, p buildrepo.Prpa, units []string) ([]solv.UnitBinary, []buildrepo.Entry, error) {
	var bins []solv.UnitBinary
	var entries []buildrepo.Entry
	for _, unit := range units {
		prefix := buildrepo.ArtifactPrefix(p, unit)
		raw, err := ix.Blobs.GetObject(ctx, prefix+bininfo.FileName)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s%s: %w", prefix, bininfo.FileName, err)
		}
		list, err := bininfo.Unmarshal(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s%s: %w", prefix, bininfo.FileName, err)
		}
		for _, b := range list {
			if !buildrepo.UseForBuild(b) {
				continue
			}
			bins = append(bins, solv.UnitBinary{Unit: unit, BinInfo: b})
			entries = append(entries, buildrepo.NewEntry(unit, prefix+b.Filename, b))
		}
	}
	return bins, entries, nil
}
