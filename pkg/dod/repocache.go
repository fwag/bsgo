package dod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// DefaultRepomdTTL is how long a repository is served without asking the
// upstream whether its repomd.xml changed.
const DefaultRepomdTTL = 5 * time.Minute

// PointerStore persists which solv blob belongs to which remote repository.
type PointerStore interface {
	GetDoDRepo(ctx context.Context, url string) (*storage.DoDRepo, error)
	SaveDoDRepo(ctx context.Context, repo *storage.DoDRepo) error
}

// RepoCacheConfig configures a RepoCache.
type RepoCacheConfig struct {
	// TTL is the repomd.xml re-check interval; zero means DefaultRepomdTTL.
	TTL time.Duration
	// Clock returns the current time; nil means time.Now.
	Clock func() time.Time
}

type cachedRepo struct {
	blob           solv.RepoBlob
	repomdChecksum string
	checkedAt      time.Time
}

// RepoCache ingests remote rpm-md repositories into libsolv blobs. A
// repository is converted once per repomd.xml content: the blob lives in S3 at
// dod/<sha256(url)>/<primary-checksum>.solv and a pointer row records the
// repomd.xml checksum it was built from. Get never re-parses primary.xml for
// an unchanged repository, in this process or after a restart.
type RepoCache struct {
	getter HTTPGetter
	s3     S3Store
	store  PointerStore
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	entries map[string]*cachedRepo
}

// NewRepoCache creates a RepoCache. getter may be nil to use the default HTTP
// client.
func NewRepoCache(getter HTTPGetter, s3 S3Store, store PointerStore, cfg RepoCacheConfig) *RepoCache {
	if getter == nil {
		getter = NewDefaultHTTPGetter()
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultRepomdTTL
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &RepoCache{
		getter: getter, s3: s3, store: store, ttl: cfg.TTL, now: cfg.Clock,
		locks: map[string]*sync.Mutex{}, entries: map[string]*cachedRepo{},
	}
}

// Get returns the solv blob of the repository at repoURL, refreshing it when
// its repomd.xml changed since the last ingest.
func (c *RepoCache) Get(ctx context.Context, repoURL string) (solv.RepoBlob, error) {
	repoURL = normalizeURL(repoURL)
	lock := c.urlLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	c.mu.Lock()
	entry := c.entries[repoURL]
	c.mu.Unlock()
	if entry != nil && c.now().Sub(entry.checkedAt) < c.ttl {
		return entry.blob, nil
	}

	repomd, err := c.getter.Get(ctx, repoURL+"repodata/repomd.xml")
	if err != nil {
		return solv.RepoBlob{}, fmt.Errorf("fetching repomd.xml of %s: %w", repoURL, err)
	}
	sum := sha256Hex(repomd)
	if entry != nil && entry.repomdChecksum == sum {
		return c.remember(repoURL, entry.blob, sum), nil
	}
	if blob, ok := c.fromPointer(ctx, repoURL, sum); ok {
		return c.remember(repoURL, blob, sum), nil
	}
	blob, key, err := c.ingest(ctx, repoURL, repomd)
	if err != nil {
		return solv.RepoBlob{}, err
	}
	err = c.store.SaveDoDRepo(ctx, &storage.DoDRepo{URL: repoURL, Name: repoURL, RepomdChecksum: sum, SolvKey: key})
	if err != nil {
		return solv.RepoBlob{}, fmt.Errorf("recording dod pointer for %s: %w", repoURL, err)
	}
	return c.remember(repoURL, blob, sum), nil
}

func (c *RepoCache) urlLock(repoURL string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.locks[repoURL]
	if !ok {
		l = &sync.Mutex{}
		c.locks[repoURL] = l
	}
	return l
}

func (c *RepoCache) remember(repoURL string, blob solv.RepoBlob, repomdSum string) solv.RepoBlob {
	c.mu.Lock()
	c.entries[repoURL] = &cachedRepo{blob: blob, repomdChecksum: repomdSum, checkedAt: c.now()}
	c.mu.Unlock()
	return blob
}

// fromPointer returns the stored blob when the pointer row matches the
// current repomd.xml checksum and the S3 object is readable.
func (c *RepoCache) fromPointer(ctx context.Context, repoURL, repomdSum string) (solv.RepoBlob, bool) {
	ptr, err := c.store.GetDoDRepo(ctx, repoURL)
	if err != nil || ptr.RepomdChecksum != repomdSum {
		return solv.RepoBlob{}, false
	}
	data, err := c.s3.GetObject(ctx, ptr.SolvKey)
	if err != nil || len(data) == 0 {
		return solv.RepoBlob{}, false
	}
	return solv.RepoBlob{Name: repoURL, Hash: primaryChecksumFromKey(ptr.SolvKey), Data: data}, true
}

// ingest downloads and verifies primary metadata and stores the solv blob. An
// existing blob for the same primary checksum is reused.
func (c *RepoCache) ingest(ctx context.Context, repoURL string, repomd []byte) (solv.RepoBlob, string, error) {
	loc, err := parsePrimary(repomd)
	if err != nil {
		return solv.RepoBlob{}, "", fmt.Errorf("repomd.xml of %s: %w", repoURL, err)
	}
	key := fmt.Sprintf("dod/%s/%s.solv", sha256Hex([]byte(repoURL)), loc.checksum)
	if data, err := c.s3.GetObject(ctx, key); err == nil && len(data) > 0 {
		return solv.RepoBlob{Name: repoURL, Hash: loc.checksum, Data: data}, key, nil
	}
	primary, err := c.getter.Get(ctx, repoURL+loc.href)
	if err != nil {
		return solv.RepoBlob{}, "", fmt.Errorf("fetching primary metadata of %s: %w", repoURL, err)
	}
	if got := sha256Hex(primary); got != loc.checksum {
		return solv.RepoBlob{}, "", fmt.Errorf("primary metadata of %s: sha256 mismatch: repomd.xml says %s, got %s", repoURL, loc.checksum, got)
	}
	data, err := solv.RPMMDToSolv(repoURL, primary)
	if err != nil {
		return solv.RepoBlob{}, "", fmt.Errorf("converting primary metadata of %s: %w", repoURL, err)
	}
	if err := c.s3.PutObject(ctx, key, data); err != nil {
		return solv.RepoBlob{}, "", fmt.Errorf("storing %s: %w", key, err)
	}
	return solv.RepoBlob{Name: repoURL, Hash: loc.checksum, Data: data}, key, nil
}

func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func primaryChecksumFromKey(key string) string {
	base := key[strings.LastIndex(key, "/")+1:]
	return strings.TrimSuffix(base, ".solv")
}

type primaryLocation struct {
	href     string
	checksum string
}

type repomdChecksumXML struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type repomdPrimaryXML struct {
	XMLName xml.Name `xml:"repomd"`
	Data    []struct {
		Type     string            `xml:"type,attr"`
		Checksum repomdChecksumXML `xml:"checksum"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

// parsePrimary locates the primary section of a repomd.xml by type and
// returns its location with the sha256 checksum the repository declares.
// Only sha256 is accepted: repositories declaring weaker digests are rejected.
func parsePrimary(repomd []byte) (primaryLocation, error) {
	var r repomdPrimaryXML
	if err := xml.Unmarshal(repomd, &r); err != nil {
		return primaryLocation{}, fmt.Errorf("unmarshal error: %w", err)
	}
	for _, d := range r.Data {
		if d.Type != "primary" || d.Location.Href == "" {
			continue
		}
		sum := strings.ToLower(strings.TrimSpace(d.Checksum.Value))
		if _, err := hex.DecodeString(sum); err != nil || d.Checksum.Type != "sha256" || len(sum) != sha256.Size*2 {
			return primaryLocation{}, errors.New("primary metadata has no valid sha256 checksum")
		}
		return primaryLocation{href: strings.TrimPrefix(d.Location.Href, "/"), checksum: sum}, nil
	}
	return primaryLocation{}, errors.New("no primary metadata section found")
}
