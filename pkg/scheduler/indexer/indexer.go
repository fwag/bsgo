// Package indexer integrates the binaries of finished jobs into the build
// repository of their prpa (the OBS "full" tree). The binary set of a unit is
// replaced as a whole; the prpa index (libsolv blob plus entry list) is
// rewritten content-addressed and published through a pointer row, and an
// event tells the scheduler that dependent prpas must be checked again.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Pointers is the pointer table; storage.Store satisfies it.
type Pointers interface {
	GetBuildRepoIndex(ctx context.Context, project, repo, arch string) (*storage.BuildRepoIndex, error)
	SaveBuildRepoIndex(ctx context.Context, idx *storage.BuildRepoIndex) error
}

// Notifier announces that the binaries of a prpa changed.
type Notifier interface {
	RepoChanged(ctx context.Context, p buildrepo.Prpa) error
}

// Artifact is one uploaded file of a finished job, as reported in the
// job-finished message.
type Artifact struct {
	Filename string `json:"filename"`
	S3Key    string `json:"s3_key"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size_bytes"`
}

// Finished describes a successful job.
type Finished struct {
	Prpa buildrepo.Prpa
	// Unit is "pkg" or "pkg:flavor".
	Unit      string
	Artifacts []Artifact
}

// Indexer writes build repository indexes.
type Indexer struct {
	Pointers Pointers
	Blobs    buildrepo.Blobs
	Reader   *buildrepo.Reader
	// Leases serializes writers of one prpa across replicas.
	Leases coord.Coordinator
	Owner  string
	Notify Notifier
	// Exports routes binaries to other architectures; nil keeps every
	// binary in the own repository.
	Exports ExportSource
	// Sleep waits between lease attempts; nil means time.Sleep.
	Sleep func(time.Duration)
}

// leaseAttempts and leaseWait bound how long Integrate waits for the writer
// lease of a prpa.
const (
	leaseAttempts = 100
	leaseWait     = 100 * time.Millisecond
)

// ExportPolicy is how the binaries of builds in one prpa are routed.
type ExportPolicy struct {
	// Router is the ExportFilter of the prpa's configuration.
	Router *buildconfig.ExportRouter
	// Archs are the architectures of the same project and repository; only
	// those can receive exports.
	Archs []string
}

// ExportSource resolves the export policy of a prpa.
type ExportSource interface {
	Exports(ctx context.Context, p buildrepo.Prpa) (ExportPolicy, error)
}

// Integrate replaces the binary set of f.Unit in the build repository of
// f.Prpa with the binaries listed in the job's bininfo.json that the
// ExportFilter keeps for the own architecture. Binaries exported to other
// architectures replace the import set "<unit>:import:<arch>" in the build
// repositories of those prpas (same project and repository). It returns
// whether the own repository content changed.
func (ix *Indexer) Integrate(ctx context.Context, f Finished) (bool, error) {
	infos, keys, err := ix.readBinaries(ctx, f)
	if err != nil {
		return false, err
	}
	own, others, archs, err := ix.route(ctx, f, infos)
	if err != nil {
		return false, err
	}
	changed, err := ix.integrateUnit(ctx, f.Prpa, f.Unit, own, keys)
	if err != nil {
		return changed, err
	}
	imp := buildrepo.ImportUnit(f.Unit, f.Prpa.Arch)
	for _, arch := range archs {
		target := buildrepo.Prpa{Project: f.Prpa.Project, Repo: f.Prpa.Repo, Arch: arch}
		if _, err := ix.integrateUnit(ctx, target, imp, others[arch], keys); err != nil {
			return changed, fmt.Errorf("export to %s: %w", target, err)
		}
	}
	return changed, nil
}

// route splits infos by the ExportFilter into the binaries of the own
// repository and those per other architecture. archs lists every other
// architecture of the repository, including those that receive nothing now:
// their previous import set must be emptied.
func (ix *Indexer) route(ctx context.Context, f Finished, infos []bininfo.BinInfo) (own []bininfo.BinInfo, others map[string][]bininfo.BinInfo, archs []string, err error) {
	if ix.Exports == nil || buildrepo.IsImportUnit(f.Unit) {
		return infos, nil, nil, nil
	}
	pol, err := ix.Exports.Exports(ctx, f.Prpa)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("export policy of %s: %w", f.Prpa, err)
	}
	if pol.Router == nil {
		return infos, nil, nil, nil
	}
	others = map[string][]bininfo.BinInfo{}
	for _, b := range infos {
		stay, targets := pol.Router.Route(b.Filename)
		if stay {
			own = append(own, b)
		}
		for _, t := range targets {
			others[t] = append(others[t], b)
		}
	}
	for _, a := range pol.Archs {
		if a != f.Prpa.Arch {
			archs = append(archs, a)
		}
	}
	sort.Strings(archs)
	return own, others, archs, nil
}

// integrateUnit replaces the entries of unit in the index of p and announces
// the change.
func (ix *Indexer) integrateUnit(ctx context.Context, p buildrepo.Prpa, unit string, infos []bininfo.BinInfo, keys map[string]string) (bool, error) {
	lease, err := ix.lease(ctx, p)
	if err != nil {
		return false, err
	}
	defer func() { _ = lease.Release(context.WithoutCancel(ctx)) }()

	cur, err := ix.Reader.Index(ctx, p)
	if err != nil {
		return false, err
	}
	entries, same := replaceUnit(cur.Entries, unit, infos, keys)
	if same {
		// Equal sha256 means equal content, so the solv blob is still right.
		return false, nil
	}
	blob, err := solv.UpdateSolv(p.Arch, cur.Solv, unit, infos)
	if err != nil {
		return false, fmt.Errorf("index %s: %w", p, err)
	}
	changed, err := ix.publish(ctx, p, cur, blob, entries)
	if err != nil || !changed {
		return false, err
	}
	if ix.Notify != nil {
		if err := ix.Notify.RepoChanged(ctx, p); err != nil {
			return true, fmt.Errorf("announce %s: %w", p, err)
		}
	}
	return true, nil
}

func (ix *Indexer) lease(ctx context.Context, p buildrepo.Prpa) (coord.Lease, error) {
	sleep := ix.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	key := "index/" + p.String()
	for range leaseAttempts {
		l, err := ix.Leases.TryLease(ctx, key, ix.Owner)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, coord.ErrHeld) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		sleep(leaseWait)
	}
	return nil, fmt.Errorf("index of %s is locked by another writer", p)
}

// readBinaries loads and verifies the job's bininfo.json: every listed RPM
// must be an uploaded artifact with the same sha256. It returns the binaries
// that belong into the build repository (UseForBuild) and their object keys.
func (ix *Indexer) readBinaries(ctx context.Context, f Finished) ([]bininfo.BinInfo, map[string]string, error) {
	byName := map[string]Artifact{}
	var info *Artifact
	for i, a := range f.Artifacts {
		byName[a.Filename] = a
		if a.Filename == bininfo.FileName {
			info = &f.Artifacts[i]
		}
	}
	if info == nil {
		for _, a := range f.Artifacts {
			if strings.HasSuffix(a.Filename, ".rpm") {
				return nil, nil, fmt.Errorf("job of %s uploaded %s but no %s", f.Unit, a.Filename, bininfo.FileName)
			}
		}
		return nil, nil, nil
	}
	raw, err := ix.Blobs.GetObject(ctx, info.S3Key)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", info.S3Key, err)
	}
	all, err := bininfo.Unmarshal(raw)
	if err != nil {
		return nil, nil, err
	}
	var infos []bininfo.BinInfo
	keys := map[string]string{}
	for _, b := range all {
		art, ok := byName[b.Filename]
		if !ok || art.SHA256 != b.SHA256 {
			return nil, nil, fmt.Errorf("binary %s of %s does not match an uploaded artifact (sha256 %s)", b.Filename, f.Unit, b.SHA256)
		}
		if buildrepo.UseForBuild(b) {
			infos = append(infos, b)
			keys[b.Filename] = art.S3Key
		}
	}
	return infos, keys, nil
}

// replaceUnit returns old with the entries of unit replaced by the binaries
// infos, and whether that left the entry set unchanged.
func replaceUnit(old []buildrepo.Entry, unit string, infos []bininfo.BinInfo, keys map[string]string) ([]buildrepo.Entry, bool) {
	out := make([]buildrepo.Entry, 0, len(old)+len(infos))
	var before []buildrepo.Entry
	for _, e := range old {
		if e.Unit == unit {
			before = append(before, e)
			continue
		}
		out = append(out, e)
	}
	var after []buildrepo.Entry
	for _, b := range infos {
		after = append(after, buildrepo.NewEntry(unit, keys[b.Filename], b))
	}
	return append(out, after...), sameEntries(before, after)
}

// sameEntries compares two entry sets of one unit regardless of order.
func sameEntries(a, b []buildrepo.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[buildrepo.Entry]int, len(a))
	for _, e := range a {
		set[e]++
	}
	for _, e := range b {
		if set[e] == 0 {
			return false
		}
		set[e]--
	}
	return true
}

// publish writes the index for blob and entries and moves the pointer;
// unchanged content is not written.
func (ix *Indexer) publish(ctx context.Context, p buildrepo.Prpa, cur *buildrepo.Index, blob []byte, entries []buildrepo.Entry) (bool, error) {
	raw, err := buildrepo.EncodeEntries(entries)
	if err != nil {
		return false, err
	}
	hash := buildrepo.ContentHash(blob, raw)
	if hash == cur.Hash {
		return false, nil
	}
	solvKey, entriesKey := buildrepo.SolvKey(p, hash), buildrepo.EntriesKey(p, hash)
	if err := ix.Blobs.PutObject(ctx, solvKey, blob); err != nil {
		return false, fmt.Errorf("writing %s: %w", solvKey, err)
	}
	if err := ix.Blobs.PutObject(ctx, entriesKey, raw); err != nil {
		return false, fmt.Errorf("writing %s: %w", entriesKey, err)
	}
	ptr := &storage.BuildRepoIndex{
		Project: p.Project, Repo: p.Repo, Arch: p.Arch,
		ContentHash: hash, SolvKey: solvKey, EntriesKey: entriesKey,
	}
	if err := ix.Pointers.SaveBuildRepoIndex(ctx, ptr); err != nil {
		return false, err
	}
	return true, nil
}

// RepoChangedEvent is the payload of the repo-changed event.
type RepoChangedEvent struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Arch    string `json:"arch"`
}

// EncodeEvent serializes the repo-changed event of a prpa.
func EncodeEvent(p buildrepo.Prpa) ([]byte, error) {
	return json.Marshal(RepoChangedEvent{Project: p.Project, Repo: p.Repo, Arch: p.Arch})
}

// DecodeEvent parses a repo-changed event.
func DecodeEvent(data []byte) (buildrepo.Prpa, error) {
	var ev RepoChangedEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return buildrepo.Prpa{}, fmt.Errorf("decoding repo changed event: %w", err)
	}
	p := buildrepo.Prpa{Project: ev.Project, Repo: ev.Repo, Arch: ev.Arch}
	if !p.Valid() {
		return buildrepo.Prpa{}, fmt.Errorf("incomplete repo changed event %+v", ev)
	}
	return p, nil
}
