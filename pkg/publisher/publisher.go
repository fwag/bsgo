// Package publisher turns the build repository of a prpa into a published
// rpm-md repository in object storage.
//
// A publish is a pure function of three inputs: the current build repository
// index (which binaries are current), the publication policy of the target
// and the previously published repository in S3. It keeps no state on local
// disk: every publish works in a fresh temp dir that is removed afterwards.
// The published binary set replaces the previous one, so the old NEVR of a
// rebuilt package disappears.
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

func isValidPathSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "..") {
		return false
	}
	return true
}

// S3ObjectInfo represents metadata for an S3 object.
type S3ObjectInfo = s3.S3ObjectInfo

// ObjectStore is the object storage of build artifacts and published
// repositories.
type ObjectStore interface {
	ReadObject(ctx context.Context, key string) ([]byte, error)
	DownloadFile(ctx context.Context, key, localPath string) error
	UploadFile(ctx context.Context, localPath, key string) error
	// CopyObject copies inside the bucket, without moving data through the
	// publisher.
	CopyObject(ctx context.Context, srcKey, dstKey string) error
	ListObjects(ctx context.Context, prefix string) ([]S3ObjectInfo, error)
	DeleteObject(ctx context.Context, key string) error
}

// IndexReader reads the current build repository of a prpa;
// buildrepo.Reader implements it.
type IndexReader interface {
	Index(ctx context.Context, p buildrepo.Prpa) (*buildrepo.Index, error)
}

// EventPublisher announces finished publications.
type EventPublisher interface {
	PublishEvent(ctx context.Context, subject string, data []byte) error
}

// SignOptions selects the signing key.
type SignOptions struct {
	Project    string
	Package    string
	SignFlavor string
	SignType   string
	HashAlgo   string
	KeyFile    string
}

// MetadataSigner signs repomd.xml and returns the paths of the detached
// signature and the public key.
type MetadataSigner interface {
	SignRepositoryMetadata(ctx context.Context, repodataDir string, opts SignOptions) (string, string, error)
}

// StateRecorder stores repository states; storage.Store satisfies it.
type StateRecorder interface {
	SetPrpaState(ctx context.Context, st *storage.PrpaState) error
}

// EnrichmentSource supplies optional updateinfo and AppStream metadata of a
// prpa.
type EnrichmentSource func(ctx context.Context, p buildrepo.Prpa) (Enrichment, error)

// Publisher publishes prpas. The zero values of CreateRepo and ModifyRepo
// select the real createrepo_c and modifyrepo_c.
type Publisher struct {
	Store      ObjectStore
	Index      IndexReader
	Policy     PolicySource
	CreateRepo CreateRepoExecutor
	ModifyRepo ModifyRepoExecutor
	Signer     MetadataSigner
	Events     EventPublisher
	Enrich     EnrichmentSource
	// States, when set, receives the repository state (publishing, published,
	// unpublished) of every publish.
	States StateRecorder
	// DownloadURL is the public URL of the object storage root. When set,
	// every repository gets a <project>.repo file; empty skips it.
	DownloadURL string
	// TempRoot is the parent of the per-publish temp dirs; empty means the
	// system temp dir.
	TempRoot string
	// GCGrace, when positive, removes repodata files that the new repomd.xml
	// no longer references and that are older than this.
	GCGrace time.Duration
	// Parallelism bounds concurrent object transfers; zero means 8.
	Parallelism int
}

// SubRepoResult describes one published sub-repository.
type SubRepoResult struct {
	Prefix    string
	RepomdKey string
	// Packages is the number of published binaries.
	Packages int
	// Transferred are the file names copied into the repository.
	Transferred []string
	// Provenance are the names of the signed SLSA provenance files exported
	// next to the transferred binaries.
	Provenance []string
	// Deleted are the file names of binaries that are no longer published.
	Deleted []string
	// Uploaded lists every key written to the repository, in write order;
	// repomd.xml is the last.
	Uploaded []string
}

// Result is the outcome of one publish.
type Result struct {
	Prpa buildrepo.Prpa
	// Skipped says why nothing was published: "", or a short reason.
	Skipped string
	// SubRepos is keyed by sub-repository.
	SubRepos map[SubRepoType]SubRepoResult
	// Conflicts lists file names that units produced with different content;
	// the binary of the last unit in artifact key order was published.
	Conflicts []NameConflict
}

// Publish publishes the current build repository of prpa. Callers serialize
// publishes of one prpa (see Service).
func (p *Publisher) Publish(ctx context.Context, prpa buildrepo.Prpa) (*Result, error) {
	if !isValidPathSegment(prpa.Project) || !isValidPathSegment(prpa.Repo) || !isValidPathSegment(prpa.Arch) {
		return nil, fmt.Errorf("invalid prpa %q: path traversal detected", prpa)
	}
	pl, err := p.plan(ctx, prpa)
	if err != nil {
		return nil, err
	}
	if pl.skipped != "" {
		return &Result{Prpa: prpa, Skipped: pl.skipped}, p.noteSkipped(ctx, prpa, pl.skipped)
	}
	if err := p.setState(ctx, prpa, storage.RepoPublishing, "", pl.hash); err != nil {
		return nil, err
	}
	res, err := p.execute(ctx, prpa, pl.pol, pl.sel)
	if err != nil {
		return nil, errors.Join(err, p.setState(ctx, prpa, storage.RepoFinished, "publish failed: "+err.Error(), pl.hash))
	}
	res.Conflicts = pl.conflicts
	if err := p.setState(ctx, prpa, storage.RepoPublished, "", pl.hash); err != nil {
		return res, err
	}
	if p.GCGrace > 0 {
		gc := NewMetadataGarbageCollector(p.Store)
		if _, err := gc.CollectGarbage(ctx, prpa.Project, prpa.Repo, prpa.Arch, p.GCGrace); err != nil {
			return res, err
		}
	}
	if err := p.emit(ctx, res); err != nil {
		return res, err
	}
	return res, nil
}

// publishPlan is what a publish will do.
type publishPlan struct {
	pol Policy
	sel Selection
	// conflicts are the file names that several units produced differently.
	conflicts []NameConflict
	// skipped is the reason when there is nothing to do.
	skipped string
	// hash is the content hash of the build repository index being published.
	hash string
}

const skipPublishingDisabled = "publishing disabled"

// plan decides what to publish: it records the reason when there is nothing
// to do, otherwise the binary selection.
func (p *Publisher) plan(ctx context.Context, prpa buildrepo.Prpa) (*publishPlan, error) {
	pol, err := p.Policy.Policy(ctx, prpa)
	if errors.Is(err, ErrUnknownTarget) {
		return &publishPlan{pol: pol, skipped: "unknown repository"}, nil
	}
	if err != nil {
		return nil, err
	}
	if !pol.Publish || !pol.RPMMD {
		return &publishPlan{pol: pol, skipped: skipPublishingDisabled}, nil
	}
	idx, err := p.Index.Index(ctx, prpa)
	if err != nil {
		return nil, err
	}
	if idx.Hash == "" {
		return &publishPlan{pol: pol, skipped: "build repository is empty"}, nil
	}
	cands, err := p.candidates(ctx, prpa, idx, pol.Debuginfo)
	if err != nil {
		return nil, err
	}
	sel, conflicts, err := selectBinaries(cands, pol, prpa.Arch)
	if err != nil {
		return nil, fmt.Errorf("selecting binaries of %s: %w", prpa, err)
	}
	return &publishPlan{pol: pol, sel: sel, conflicts: conflicts, hash: idx.Hash}, nil
}

// setState records the repository state of prpa; it does nothing without a
// States store.
func (p *Publisher) setState(ctx context.Context, prpa buildrepo.Prpa, state, details, indexHash string) error {
	if p.States == nil {
		return nil
	}
	err := p.States.SetPrpaState(ctx, &storage.PrpaState{
		Project: prpa.Project, Repo: prpa.Repo, Arch: prpa.Arch,
		State: state, Details: details, IndexHash: indexHash,
	})
	if err != nil {
		return fmt.Errorf("recording repository state %s of %s: %w", state, prpa, err)
	}
	return nil
}

// noteSkipped records unpublished for a target that does not publish.
func (p *Publisher) noteSkipped(ctx context.Context, prpa buildrepo.Prpa, reason string) error {
	if reason != skipPublishingDisabled {
		return nil
	}
	return p.setState(ctx, prpa, storage.RepoUnpublished, "", "")
}

// execute publishes the selection in a temp dir that is removed afterwards,
// whatever the outcome.
func (p *Publisher) execute(ctx context.Context, prpa buildrepo.Prpa, pol Policy, sel Selection) (res *Result, err error) {
	tmp, err := os.MkdirTemp(p.TempRoot, "bsgo-publish-")
	if err != nil {
		return nil, fmt.Errorf("creating publish temp dir: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(tmp); rerr != nil {
			err = errors.Join(err, fmt.Errorf("removing publish temp dir: %w", rerr))
		}
	}()
	res = &Result{Prpa: prpa, SubRepos: map[SubRepoType]SubRepoResult{}}
	subs := []SubRepoType{SubRepoStandard}
	if pol.Debuginfo {
		subs = append(subs, SubRepoDebug)
	}
	for _, sr := range subs {
		srRes, err := p.publishSubRepo(ctx, tmp, prpa, sr, sel[sr])
		if err != nil {
			return nil, fmt.Errorf("publishing %s of %s: %w", sr, prpa, err)
		}
		res.SubRepos[sr] = *srRes
	}
	return res, nil
}

// candidates lists the binaries the policy may choose from. The build
// repository index holds the current runtime binaries of every unit. Debug
// packages are not part of it (they are no build dependencies), so with
// debuginfo enabled they are read from the bininfo.json of each unit that
// the index knows.
func (p *Publisher) candidates(ctx context.Context, prpa buildrepo.Prpa, idx *buildrepo.Index, debug bool) ([]Binary, error) {
	cands := make([]Binary, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		cands = append(cands, Binary{BinInfo: e.Info(), Source: e.Key})
	}
	if !debug {
		return cands, nil
	}
	units := make([]string, 0)
	for u := range idx.Units() {
		if !buildrepo.IsImportUnit(u) { // exported from another arch: no artifacts here
			units = append(units, u)
		}
	}
	sort.Strings(units)
	for _, u := range units {
		prefix := buildrepo.ArtifactPrefix(prpa, u)
		raw, err := p.Store.ReadObject(ctx, prefix+bininfo.FileName)
		if err != nil {
			return nil, fmt.Errorf("reading %s%s: %w", prefix, bininfo.FileName, err)
		}
		list, err := bininfo.Unmarshal(raw)
		if err != nil {
			return nil, fmt.Errorf("%s%s: %w", prefix, bininfo.FileName, err)
		}
		for _, b := range list {
			if classifyBinary(b) == SubRepoDebug {
				cands = append(cands, Binary{BinInfo: b, Source: prefix + b.Filename})
			}
		}
	}
	return cands, nil
}

// emit announces the publication. The repository is already switched when it
// runs, so a failure only makes the caller publish again, which is idempotent.
func (p *Publisher) emit(ctx context.Context, res *Result) error {
	if p.Events == nil {
		return nil
	}
	std := res.SubRepos[SubRepoStandard]
	ev := v1.NewPublicationEvent(res.Prpa.Project, res.Prpa.Repo, res.Prpa.Arch, "", std.Transferred, std.RepomdKey)
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encoding publication event: %w", err)
	}
	if err := p.Events.PublishEvent(ctx, natsqueue.SubjectEventsPublished, data); err != nil {
		return fmt.Errorf("announcing publication of %s: %w", res.Prpa, err)
	}
	return nil
}
