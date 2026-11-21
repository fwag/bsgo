package gc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryObjects is an in-memory Objects for tests and single-process setups.
type MemoryObjects struct {
	mu sync.Mutex
	m  map[string]memObject
	// Batches records the size of every DeleteBatch call.
	Batches []int
}

type memObject struct {
	data []byte
	mod  time.Time
}

// NewMemoryObjects returns an empty MemoryObjects.
func NewMemoryObjects() *MemoryObjects { return &MemoryObjects{m: map[string]memObject{}} }

// Put stores data under key with the given modification time.
func (o *MemoryObjects) Put(key string, data []byte, mod time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = memObject{data: append([]byte(nil), data...), mod: mod}
}

// List implements Objects.
func (o *MemoryObjects) List(_ context.Context, prefix string) ([]Object, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Object
	for k, v := range o.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k, LastModified: v.mod, Size: int64(len(v.data))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Get implements Objects.
func (o *MemoryObjects) Get(_ context.Context, key string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.m[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return append([]byte(nil), v.data...), nil
}

// DeleteBatch implements Objects.
func (o *MemoryObjects) DeleteBatch(_ context.Context, keys []string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(keys) > MaxBatch {
		return fmt.Errorf("batch of %d keys exceeds %d", len(keys), MaxBatch)
	}
	o.Batches = append(o.Batches, len(keys))
	for _, k := range keys {
		delete(o.m, k)
	}
	return nil
}

// Has reports whether key exists.
func (o *MemoryObjects) Has(key string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.m[key]
	return ok
}

// Keys returns the sorted keys.
func (o *MemoryObjects) Keys() []string {
	objs, _ := o.List(context.Background(), "")
	keys := make([]string, len(objs))
	for i, ob := range objs {
		keys[i] = ob.Key
	}
	return keys
}
