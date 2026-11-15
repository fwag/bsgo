package publisher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory ObjectStore for tests and single-process
// setups. It logs every mutation in order, which lets tests assert the write
// order of a publish.
type MemoryStore struct {
	mu        sync.Mutex
	files     map[string][]byte
	times     map[string]time.Time
	ops       []string
	downloads []string
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{files: map[string][]byte{}, times: map[string]time.Time{}}
}

// Set stores an object without logging a mutation.
func (s *MemoryStore) Set(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[key] = append([]byte(nil), data...)
	s.times[key] = time.Now()
}

// SetModTime changes the modification time of an object.
func (s *MemoryStore) SetModTime(key string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.times[key] = t
}

// Has reports whether an object exists.
func (s *MemoryStore) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[key]
	return ok
}

// Keys returns the sorted keys with the given prefix.
func (s *MemoryStore) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.files {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// TakeOps returns and clears the mutation log: "put:<key>" for uploads,
// "copy:<key>" for copies (the destination) and "del:<key>" for deletions.
func (s *MemoryStore) TakeOps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops := s.ops
	s.ops = nil
	return ops
}

// TakeDownloads returns and clears the keys fetched with DownloadFile.
func (s *MemoryStore) TakeDownloads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.downloads
	s.downloads = nil
	return d
}

func (s *MemoryStore) logOp(op string) {
	s.mu.Lock()
	s.ops = append(s.ops, op)
	s.mu.Unlock()
}

// ReadObject implements ObjectStore.
func (s *MemoryStore) ReadObject(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return append([]byte(nil), data...), nil
}

// DownloadFile implements ObjectStore.
func (s *MemoryStore) DownloadFile(ctx context.Context, key, local string) error {
	data, err := s.ReadObject(ctx, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.downloads = append(s.downloads, key)
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(local), 0o750); err != nil {
		return err
	}
	return os.WriteFile(local, data, 0o600)
}

// UploadFile implements ObjectStore.
func (s *MemoryStore) UploadFile(_ context.Context, local, key string) error {
	// #nosec G304 -- local is a path chosen by the caller, as in the real store
	data, err := os.ReadFile(filepath.Clean(local))
	if err != nil {
		return err
	}
	s.Set(key, data)
	s.logOp("put:" + key)
	return nil
}

// CopyObject implements ObjectStore.
func (s *MemoryStore) CopyObject(ctx context.Context, src, dst string) error {
	data, err := s.ReadObject(ctx, src)
	if err != nil {
		return err
	}
	s.Set(dst, data)
	s.logOp("copy:" + dst)
	return nil
}

// ListObjects implements ObjectStore.
func (s *MemoryStore) ListObjects(_ context.Context, prefix string) ([]S3ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []S3ObjectInfo
	for k, v := range s.files {
		if strings.HasPrefix(k, prefix) {
			out = append(out, S3ObjectInfo{Key: k, Size: int64(len(v)), LastModified: s.times[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// DeleteObject implements ObjectStore.
func (s *MemoryStore) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, key)
	s.ops = append(s.ops, "del:"+key)
	return nil
}
