package buildrepo

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

func sortEntries(e []Entry) {
	sort.Slice(e, func(i, j int) bool {
		if e[i].Unit != e[j].Unit {
			return e[i].Unit < e[j].Unit
		}
		return e[i].Filename < e[j].Filename
	})
}

// MemoryBlobs is an in-memory Blobs for tests and single-process setups.
type MemoryBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemoryBlobs returns an empty MemoryBlobs.
func NewMemoryBlobs() *MemoryBlobs { return &MemoryBlobs{m: map[string][]byte{}} }

// GetObject implements Blobs.
func (b *MemoryBlobs) GetObject(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.m[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return append([]byte(nil), data...), nil
}

// PutObject implements Blobs.
func (b *MemoryBlobs) PutObject(_ context.Context, key string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[key] = append([]byte(nil), data...)
	return nil
}

// Keys returns the sorted object keys.
func (b *MemoryBlobs) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]string, 0, len(b.m))
	for k := range b.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
