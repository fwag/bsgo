package solv

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// MultiArchPool manages on-demand libsolv worker pools partitioned per architecture.
type MultiArchPool struct {
	mu         sync.Mutex
	pools      map[string]*WorkerPool
	numWorkers int
	closed     bool
}

func NewMultiArchPool(workersPerArch int) *MultiArchPool {
	if workersPerArch <= 0 {
		workersPerArch = 2
	}
	return &MultiArchPool{
		pools:      make(map[string]*WorkerPool),
		numWorkers: workersPerArch,
	}
}

// PoolForArch returns or lazily allocates a WorkerPool for the target architecture.
func (m *MultiArchPool) PoolForArch(arch string) (*WorkerPool, error) {
	if arch == "" {
		arch = runtime.GOARCH
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, fmt.Errorf("multi-arch pool is closed")
	}

	if pool, exists := m.pools[arch]; exists {
		return pool, nil
	}

	pool, err := NewWorkerPool(arch, m.numWorkers)
	if err != nil {
		return nil, fmt.Errorf("failed allocating solver pool for arch %s: %w", arch, err)
	}
	m.pools[arch] = pool
	return pool, nil
}

// Resolve delegates dependency resolution to the worker pool corresponding to the request's architecture.
func (m *MultiArchPool) Resolve(ctx context.Context, req ExpansionRequest) (*ExpansionResult, error) {
	arch := req.Architecture
	if arch == "" {
		arch = runtime.GOARCH
	}
	pool, err := m.PoolForArch(arch)
	if err != nil {
		return nil, err
	}
	return pool.Resolve(ctx, req)
}

// Close shuts down all worker pools across all architectures.
func (m *MultiArchPool) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, p := range m.pools {
		p.Close()
	}
}
