package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
)

// generated is the metadata of one sub-repository, ready for upload.
type generated struct {
	// repodataDir holds repomd.xml and the metadata files it references.
	repodataDir string
	// signature and key are the local paths of repomd.xml.asc and
	// repomd.xml.key; empty when the repository is not signed.
	signature, key string
	// repoFile is the local path of <project>.repo; empty when no download
	// URL is configured.
	repoFile string
}

func (p *Publisher) publishSubRepo(ctx context.Context, tmp string, prpa buildrepo.Prpa, sr SubRepoType, bins []Binary) (*SubRepoResult, error) {
	prefix := SubRepoS3Prefix(prpa.Project, prpa.Repo, prpa.Arch, sr)
	work := filepath.Join(tmp, string(sr))
	listing, err := listRepo(ctx, p.Store, prefix)
	if err != nil {
		return nil, err
	}
	old, err := fetchOldRepodata(ctx, p.Store, prefix, filepath.Join(work, "old"), listing)
	if err != nil {
		return nil, err
	}
	gen, err := p.generateWithFallback(ctx, work, prpa, sr, bins, old)
	if err != nil {
		return nil, err
	}
	res := &SubRepoResult{Prefix: prefix, Packages: len(bins)}
	if res.Transferred, err = p.transfer(ctx, prefix, bins, listing, old); err != nil {
		return nil, err
	}
	if res.Uploaded, err = p.uploadRepodata(ctx, prefix, gen); err != nil {
		return nil, err
	}
	res.RepomdKey = prefix + "repodata/repomd.xml"
	if res.Deleted, err = p.deleteStale(ctx, prefix, bins, listing); err != nil {
		return nil, err
	}
	return res, nil
}

// generateWithFallback generates incrementally from the previously published
// metadata. The result is verified against the expected checksums, so if the
// old metadata was incomplete or inconsistent in a way createrepo_c cannot
// cope with, it generates again from the RPM files alone.
func (p *Publisher) generateWithFallback(ctx context.Context, work string, prpa buildrepo.Prpa, sr SubRepoType, bins []Binary, old oldRepodata) (*generated, error) {
	gen, err := p.generate(ctx, work, prpa, sr, bins, old)
	if err == nil || !old.usable() || ctx.Err() != nil {
		return gen, err
	}
	gen, err2 := p.generate(ctx, work, prpa, sr, bins, oldRepodata{})
	if err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return gen, nil
}

func (p *Publisher) generate(ctx context.Context, work string, prpa buildrepo.Prpa, sr SubRepoType, bins []Binary, old oldRepodata) (*generated, error) {
	pkgDir, outDir := filepath.Join(work, "pkgs"), filepath.Join(work, "out")
	for _, d := range []string{pkgDir, outDir} {
		if err := os.RemoveAll(d); err != nil {
			return nil, fmt.Errorf("cleaning %s: %w", d, err)
		}
	}
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating %s: %w", pkgDir, err)
	}
	if err := p.fetchChanged(ctx, pkgDir, bins, old); err != nil {
		return nil, err
	}
	opts := CreateRepoOptions{Dir: pkgDir, OutputDir: outDir, Pkglist: make([]string, 0, len(bins))}
	want := make(map[string]string, len(bins))
	for _, b := range bins {
		opts.Pkglist = append(opts.Pkglist, b.Filename)
		want[b.Filename] = b.SHA256
	}
	if old.usable() {
		opts.OldRepoDir = old.Dir
	}
	creator := p.CreateRepo
	if creator == nil {
		creator = NewRealCreateRepoExecutor()
	}
	if err := creator.Run(ctx, opts); err != nil {
		return nil, fmt.Errorf("metadata generation failed: %w", err)
	}
	repodata := filepath.Join(outDir, "repodata")
	if err := p.enrich(ctx, repodata, work, prpa, sr); err != nil {
		return nil, err
	}
	if err := verifyRepodata(repodata, want); err != nil {
		return nil, err
	}
	gen, err := p.sign(ctx, repodata, prpa)
	if err != nil {
		return nil, err
	}
	if err := p.writeRepoFile(gen, prpa.Project, SubRepoS3Prefix(prpa.Project, prpa.Repo, prpa.Arch, sr)); err != nil {
		return nil, err
	}
	return gen, nil
}

// fetchChanged downloads the RPMs whose sha256 is not in the old metadata and
// checks each against the index.
func (p *Publisher) fetchChanged(ctx context.Context, dir string, bins []Binary, old oldRepodata) error {
	var todo []Binary
	for _, b := range bins {
		if old.Sums[b.Filename] != b.SHA256 {
			todo = append(todo, b)
		}
	}
	return p.forEach(ctx, len(todo), func(ctx context.Context, i int) error {
		b := todo[i]
		dst := filepath.Join(dir, b.Filename)
		if err := p.Store.DownloadFile(ctx, b.Source, dst); err != nil {
			return fmt.Errorf("downloading %s: %w", b.Source, err)
		}
		sum, err := fileSHA256(dst)
		if err != nil {
			return err
		}
		if sum != b.SHA256 {
			return fmt.Errorf("%s has sha256 %s, the build repository index says %s", b.Source, sum, b.SHA256)
		}
		return nil
	})
}

func fileSHA256(file string) (string, error) {
	// #nosec G304 -- file is inside the publish temp dir
	f, err := os.Open(filepath.Clean(file))
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", file, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing %s: %w", file, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// enrich injects updateinfo and AppStream metadata into the standard
// sub-repository.
func (p *Publisher) enrich(ctx context.Context, repodata, work string, prpa buildrepo.Prpa, sr SubRepoType) error {
	if p.Enrich == nil || sr != SubRepoStandard {
		return nil
	}
	enr, err := p.Enrich(ctx, prpa)
	if err != nil {
		return fmt.Errorf("loading metadata enrichment: %w", err)
	}
	if enr.Empty() {
		return nil
	}
	mr := p.ModifyRepo
	if mr == nil {
		mr = NewRealModifyRepoExecutor()
	}
	if err := EnrichMetadata(ctx, mr, repodata, enr, work); err != nil {
		return fmt.Errorf("metadata enrichment failed: %w", err)
	}
	return nil
}

func (p *Publisher) sign(ctx context.Context, repodata string, prpa buildrepo.Prpa) (*generated, error) {
	gen := &generated{repodataDir: repodata}
	if p.Signer == nil {
		return gen, nil
	}
	sig, key, err := p.Signer.SignRepositoryMetadata(ctx, repodata, SignOptions{Project: prpa.Project})
	if err != nil {
		return nil, fmt.Errorf("signing repomd.xml: %w", err)
	}
	gen.signature, gen.key = sig, key
	return gen, nil
}

// transfer copies the binaries that are not yet in the published
// repository from the artifact prefix, inside object storage. A binary is
// already published when the old metadata lists its sha256 and the object
// has the expected size.
func (p *Publisher) transfer(ctx context.Context, prefix string, bins []Binary, listing *Listing, old oldRepodata) ([]string, error) {
	var todo []Binary
	for _, b := range bins {
		size, exists := listing.Objects[b.Filename]
		if exists && old.Sums[b.Filename] == b.SHA256 && (b.Size == 0 || size == b.Size) {
			continue
		}
		todo = append(todo, b)
	}
	var mu sync.Mutex
	names := make([]string, 0, len(todo))
	err := p.forEach(ctx, len(todo), func(ctx context.Context, i int) error {
		b := todo[i]
		if err := p.Store.CopyObject(ctx, b.Source, prefix+b.Filename); err != nil {
			return fmt.Errorf("publishing %s: %w", b.Filename, err)
		}
		mu.Lock()
		names = append(names, b.Filename)
		mu.Unlock()
		return nil
	})
	sort.Strings(names)
	return names, err
}

// uploadRepodata writes the metadata files, then the signature, and
// repomd.xml last: repomd.xml is the pointer that switches the repository to
// the new content, so a client never sees metadata that references objects
// which are not there yet.
func (p *Publisher) uploadRepodata(ctx context.Context, prefix string, gen *generated) ([]string, error) {
	entries, err := os.ReadDir(gen.repodataDir)
	if err != nil {
		return nil, fmt.Errorf("reading generated repodata: %w", err)
	}
	var keys []string
	put := func(local, key string) error {
		if err := p.Store.UploadFile(ctx, local, key); err != nil {
			return fmt.Errorf("uploading %s: %w", key, err)
		}
		keys = append(keys, key)
		return nil
	}
	hasRepomd := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == "repomd.xml" {
			hasRepomd = true
			continue
		}
		if err := put(filepath.Join(gen.repodataDir, e.Name()), prefix+"repodata/"+e.Name()); err != nil {
			return nil, err
		}
	}
	if !hasRepomd {
		return nil, fmt.Errorf("generated repodata has no repomd.xml")
	}
	if gen.signature != "" {
		if err := put(gen.signature, prefix+"repodata/repomd.xml.asc"); err != nil {
			return nil, err
		}
		if err := put(gen.key, prefix+"repodata/repomd.xml.key"); err != nil {
			return nil, err
		}
	}
	if gen.repoFile != "" {
		if err := put(gen.repoFile, prefix+filepath.Base(gen.repoFile)); err != nil {
			return nil, err
		}
	}
	if err := put(filepath.Join(gen.repodataDir, "repomd.xml"), prefix+"repodata/repomd.xml"); err != nil {
		return nil, err
	}
	return keys, nil
}

// deleteStale removes the RPMs of binaries that are no longer published. It
// runs after the repomd.xml switch, so no metadata references them any more.
func (p *Publisher) deleteStale(ctx context.Context, prefix string, bins []Binary, listing *Listing) ([]string, error) {
	keep := make(map[string]bool, len(bins))
	for _, b := range bins {
		keep[b.Filename] = true
	}
	var stale []string
	for name := range listing.Objects {
		if path.Ext(name) == ".rpm" && !keep[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		if err := p.Store.DeleteObject(ctx, prefix+name); err != nil {
			return stale, fmt.Errorf("deleting %s: %w", prefix+name, err)
		}
	}
	return stale, nil
}

// forEach runs fn for 0..n-1 with bounded parallelism and returns the first
// error.
func (p *Publisher) forEach(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	limit := p.Parallelism
	if limit <= 0 {
		limit = 8
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for i := range n {
		g.Go(func() error { return fn(gctx, i) })
	}
	return g.Wait()
}
