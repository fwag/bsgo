package solv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// RepoBlob is one repository of a build path, either a libsolv blob or rpm-md
// primary.xml(.gz) data. Hash identifies the content (any stable digest, for
// example the repomd checksum or an S3 object hash); when empty it is the
// sha256 of Data.
type RepoBlob struct {
	Name string
	Hash string
	Data []byte
}

// ContentHash returns the identity of the blob.
func (b RepoBlob) ContentHash() string {
	if b.Hash != "" {
		return b.Hash
	}
	return blobHash(b.Data)
}

var (
	blobHashMu    sync.Mutex
	blobHashCache = map[blobKey]string{}
)

type blobKey struct {
	ptr *byte
	n   int
}

// blobHash returns the sha256 of data. Results are memoized per backing array
// so that callers passing the same large slice repeatedly do not pay for
// rehashing it.
func blobHash(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	k := blobKey{ptr: &data[0], n: len(data)}
	blobHashMu.Lock()
	h, ok := blobHashCache[k]
	blobHashMu.Unlock()
	if ok {
		return h
	}
	sum := sha256.Sum256(data)
	h = hex.EncodeToString(sum[:])
	blobHashMu.Lock()
	if len(blobHashCache) > 256 {
		clear(blobHashCache)
	}
	blobHashCache[k] = h
	blobHashMu.Unlock()
	return h
}

func isSolvBlob(data []byte) bool {
	return len(data) >= 4 && string(data[:4]) == "SOLV"
}

// RPMMDToSolv converts rpm-md primary.xml(.gz) data into a libsolv blob that
// can be cached and passed back as RepoBlob data. Architecture filtering is
// not applied: the blob keeps every package, the consuming pool decides.
func RPMMDToSolv(name string, primary []byte) ([]byte, error) {
	pool, err := NewPool("")
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	repo, err := pool.LoadRepoFromRPMMD(name, primary)
	if err != nil {
		return nil, err
	}
	return repo.DumpMemory()
}

func (p *Pool) loadBlob(b RepoBlob) error {
	var err error
	if isSolvBlob(b.Data) {
		_, err = p.LoadRepoFromMemory(b.Name, b.Data)
	} else {
		_, err = p.LoadRepoFromRPMMD(b.Name, b.Data)
	}
	if err != nil {
		return fmt.Errorf("repository %s: %w", b.Name, err)
	}
	return nil
}

// PathPool is a long-lived pool for one architecture and one ordered build
// path. Repositories are loaded once; Resolve, Expand, GetBuild and GetDeps
// never reparse them.
type PathPool struct {
	*Pool
	key string
}

// NewPathPool builds a pool from repos in priority order: repos[0] is the
// project's own repository and has the highest priority. A package name that
// exists in a higher-priority repository hides every package of that name in
// lower-priority repositories (BSSched::Checker createpool semantics). Packages
// whose architecture is not compatible with arch are never installable.
func NewPathPool(arch string, repos []RepoBlob) (*PathPool, error) {
	pool, err := NewPool(arch)
	if err != nil {
		return nil, err
	}
	for _, b := range repos {
		if err := pool.loadBlob(b); err != nil {
			pool.Close()
			return nil, err
		}
	}
	pool.mu.Lock()
	err = pool.prepareLocked()
	pool.mu.Unlock()
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &PathPool{Pool: pool, key: poolKey(arch, repos)}, nil
}

// Key returns the cache key (architecture plus ordered repo content hashes).
func (pp *PathPool) Key() string { return pp.key }

func poolKey(arch string, repos []RepoBlob) string {
	var sb strings.Builder
	sb.WriteString(arch)
	for _, r := range repos {
		sb.WriteByte('|')
		sb.WriteString(r.ContentHash())
	}
	return sb.String()
}

type cacheEntry struct {
	once sync.Once
	pool *PathPool
	err  error
	used uint64
}

// PoolCache keeps built path pools keyed by (arch, ordered repo hashes). A
// changed repository content yields a different key, so stale pools are never
// served; least recently used pools are closed when the cache is full.
type PoolCache struct {
	mu      sync.Mutex
	max     int
	tick    uint64
	entries map[string]*cacheEntry
}

// NewPoolCache returns a cache holding at most capacity pools (minimum 1).
func NewPoolCache(capacity int) *PoolCache {
	return &PoolCache{max: max(capacity, 1), entries: map[string]*cacheEntry{}}
}

// Get returns the pool for arch and repos, building it on first use. The first
// caller builds; concurrent callers for the same key wait for the result.
func (c *PoolCache) Get(arch string, repos []RepoBlob) (*PathPool, error) {
	key := poolKey(arch, repos)
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &cacheEntry{}
		c.entries[key] = e
		c.evictLocked(key)
	}
	c.tick++
	e.used = c.tick
	c.mu.Unlock()

	e.once.Do(func() { e.pool, e.err = NewPathPool(arch, repos) })
	if e.err != nil {
		c.mu.Lock()
		if c.entries[key] == e {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, e.err
	}
	return e.pool, nil
}

func (c *PoolCache) evictLocked(keep string) {
	for len(c.entries) > c.max {
		var victim string
		var oldest uint64
		for k, e := range c.entries {
			if k == keep {
				continue
			}
			if victim == "" || e.used < oldest {
				victim, oldest = k, e.used
			}
		}
		if victim == "" {
			return
		}
		e := c.entries[victim]
		delete(c.entries, victim)
		go func() {
			e.once.Do(func() {})
			if e.pool != nil {
				e.pool.Close()
			}
		}()
	}
}

// Close closes every cached pool.
func (c *PoolCache) Close() {
	c.mu.Lock()
	entries := c.entries
	c.entries = map[string]*cacheEntry{}
	c.mu.Unlock()
	for _, e := range entries {
		e.once.Do(func() {})
		if e.pool != nil {
			e.pool.Close()
		}
	}
}

// Len returns the number of cached pools.
func (c *PoolCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
