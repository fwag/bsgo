package solv

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
)

// WorkerPool resolves expansion requests for one architecture. Repositories
// passed with a request are loaded into a long-lived PathPool cached by
// content hash, so only the first request for a given repository set parses
// anything. At most numWorkers resolutions run at the same time.
type WorkerPool struct {
	arch  string
	slots chan struct{}
	cache *PoolCache
	mu    sync.Mutex
	stop  chan struct{}
}

// defaultCachedPools bounds the number of distinct repository sets kept warm.
const defaultCachedPools = 8

func NewWorkerPool(arch string, numWorkers int) (*WorkerPool, error) {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}
	return &WorkerPool{
		arch:  arch,
		slots: make(chan struct{}, numWorkers),
		cache: NewPoolCache(defaultCachedPools),
		stop:  make(chan struct{}),
	}, nil
}

func (wp *WorkerPool) Close() {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	select {
	case <-wp.stop:
		return
	default:
	}
	close(wp.stop)
	wp.cache.Close()
}

// blobsFromRequest orders the legacy repo map by name; the map carries no
// priority so the name order defines it.
func blobsFromRequest(repos map[string][]byte) []RepoBlob {
	names := make([]string, 0, len(repos))
	for n := range repos {
		names = append(names, n)
	}
	sort.Strings(names)
	blobs := make([]RepoBlob, len(names))
	for i, n := range names {
		blobs[i] = RepoBlob{Name: n, Data: repos[n]}
	}
	return blobs
}

func (wp *WorkerPool) Resolve(ctx context.Context, req ExpansionRequest) (*ExpansionResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wp.stop:
		return nil, fmt.Errorf("worker pool is stopped")
	case wp.slots <- struct{}{}:
	}
	defer func() { <-wp.slots }()

	pp, err := wp.cache.Get(wp.arch, blobsFromRequest(req.RPMMDRepos))
	if err != nil {
		return nil, err
	}
	req.RPMMDRepos = nil
	return pp.Resolve(req)
}
