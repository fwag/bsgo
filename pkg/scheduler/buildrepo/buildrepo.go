// Package buildrepo defines the per-prpa build repository: the set of binaries
// built inside one (project, repository, architecture), the "full" tree of
// OBS. Its index is a libsolv blob plus the list of entries it was built from,
// both stored content-addressed in S3 and referenced by a pointer row in
// PostgreSQL. The checker reads the index to build its dependency pool, the
// indexer replaces a unit's binary set in it when a job finishes.
package buildrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Prpa identifies one build repository: project, repository and architecture.
type Prpa struct {
	Project string
	Repo    string
	Arch    string
}

// String renders "project/repo/arch", the OBS prpa notation.
func (p Prpa) String() string { return p.Project + "/" + p.Repo + "/" + p.Arch }

// Valid reports whether all three parts are set.
func (p Prpa) Valid() bool { return p.Project != "" && p.Repo != "" && p.Arch != "" }

// ParsePrpa parses the String form. The project may contain colons but no slash.
func ParsePrpa(s string) (Prpa, error) {
	parts := strings.Split(s, "/")
	p := Prpa{}
	if len(parts) == 3 {
		p = Prpa{Project: parts[0], Repo: parts[1], Arch: parts[2]}
	}
	if !p.Valid() {
		return Prpa{}, fmt.Errorf("invalid prpa %q: want project/repo/arch", s)
	}
	return p, nil
}

// UnitName returns the identity of a package unit: "pkg" or "pkg:flavor".
func UnitName(pkg, flavor string) string {
	if flavor == "" {
		return pkg
	}
	return pkg + ":" + flavor
}

// SplitUnit is the inverse of UnitName.
func SplitUnit(unit string) (pkg, flavor string) {
	pkg, flavor, _ = strings.Cut(unit, ":")
	return pkg, flavor
}

// ArtifactPrefix is the S3 prefix the worker uploads a unit's results to.
func ArtifactPrefix(p Prpa, unit string) string {
	return "artifacts/" + p.Project + "/" + p.Repo + "/" + p.Arch + "/" + unit + "/"
}

// Entry is one binary of the build repository.
type Entry struct {
	// Unit is the package unit ("pkg" or "pkg:flavor") that produced it.
	Unit string `json:"unit"`
	// Key is the S3 object key of the RPM.
	Key string `json:"key"`
	bininfo.BinInfo
}

// UseForBuild reports whether a binary belongs into the build repository.
// Like OBS it leaves out source and debug packages.
func UseForBuild(b bininfo.BinInfo) bool {
	if b.Arch == "src" || b.Arch == "nosrc" {
		return false
	}
	return !strings.HasSuffix(b.Name, "-debuginfo") && !strings.HasSuffix(b.Name, "-debugsource")
}

// Index is the current content of a build repository.
type Index struct {
	// Hash is the content address: sha256 over the solv blob and the entries.
	Hash string
	// Solv is the libsolv blob; empty for an empty repository.
	Solv    []byte
	Entries []Entry
}

// Units returns the sorted unit names that have at least one binary.
func (i *Index) Units() map[string][]Entry {
	out := map[string][]Entry{}
	if i == nil {
		return out
	}
	for _, e := range i.Entries {
		out[e.Unit] = append(out[e.Unit], e)
	}
	return out
}

// Lookup finds the entry of a binary by its identity.
func (i *Index) Lookup(name, evr, arch, sha string) (Entry, bool) {
	if i == nil {
		return Entry{}, false
	}
	for _, e := range i.Entries {
		if e.Name == name && e.EVR() == evr && e.Arch == arch && (sha == "" || e.SHA256 == sha) {
			return e, true
		}
	}
	return Entry{}, false
}

// SolvKey returns the S3 key of the blob of a content hash.
func SolvKey(p Prpa, hash string) string {
	return "buildrepo/" + p.String() + "/" + hash + ".solv"
}

// EntriesKey returns the S3 key of the entry list of a content hash.
func EntriesKey(p Prpa, hash string) string {
	return "buildrepo/" + p.String() + "/" + hash + ".entries.json"
}

// ContentHash computes the content address of a blob and its entries.
func ContentHash(solvBlob, entriesJSON []byte) string {
	h := sha256.New()
	h.Write(solvBlob)
	h.Write([]byte{0})
	h.Write(entriesJSON)
	return hex.EncodeToString(h.Sum(nil))
}

// EncodeEntries serializes entries deterministically (sorted by unit, then
// file name).
func EncodeEntries(entries []Entry) ([]byte, error) {
	sorted := append([]Entry(nil), entries...)
	sortEntries(sorted)
	data, err := json.Marshal(sorted)
	if err != nil {
		return nil, fmt.Errorf("encoding build repo entries: %w", err)
	}
	return data, nil
}

// Blobs is the object storage the index lives in. It is satisfied by the S3
// adapter of the server and by in-memory fakes.
type Blobs interface {
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, data []byte) error
}

// Pointers is the pointer table. storage.Store satisfies it.
type Pointers interface {
	GetBuildRepoIndex(ctx context.Context, project, repo, arch string) (*storage.BuildRepoIndex, error)
}

// Reader loads build repository indexes, keeping the most recent ones in
// memory by content hash.
type Reader struct {
	pointers Pointers
	blobs    Blobs

	mu    sync.Mutex
	cache map[string]*Index
}

// NewReader returns a Reader.
func NewReader(p Pointers, b Blobs) *Reader {
	return &Reader{pointers: p, blobs: b, cache: map[string]*Index{}}
}

// maxCached bounds the number of indexes kept in memory.
const maxCached = 64

// Index returns the current index of p. A prpa that was never indexed yields
// an empty index with an empty Hash.
func (r *Reader) Index(ctx context.Context, p Prpa) (*Index, error) {
	ptr, err := r.pointers.GetBuildRepoIndex(ctx, p.Project, p.Repo, p.Arch)
	if errors.Is(err, storage.ErrNotFound) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("build repo pointer of %s: %w", p, err)
	}
	r.mu.Lock()
	idx, ok := r.cache[ptr.ContentHash]
	r.mu.Unlock()
	if ok {
		return idx, nil
	}
	idx, err = r.load(ctx, ptr)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if len(r.cache) >= maxCached {
		clear(r.cache)
	}
	r.cache[ptr.ContentHash] = idx
	r.mu.Unlock()
	return idx, nil
}

func (r *Reader) load(ctx context.Context, ptr *storage.BuildRepoIndex) (*Index, error) {
	solvBlob, err := r.blobs.GetObject(ctx, ptr.SolvKey)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ptr.SolvKey, err)
	}
	raw, err := r.blobs.GetObject(ctx, ptr.EntriesKey)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ptr.EntriesKey, err)
	}
	if got := ContentHash(solvBlob, raw); got != ptr.ContentHash {
		return nil, fmt.Errorf("build repo %s/%s/%s: content hash mismatch: pointer %s, objects %s",
			ptr.Project, ptr.Repo, ptr.Arch, ptr.ContentHash, got)
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", ptr.EntriesKey, err)
	}
	return &Index{Hash: ptr.ContentHash, Solv: solvBlob, Entries: entries}, nil
}
