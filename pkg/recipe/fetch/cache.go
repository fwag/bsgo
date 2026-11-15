package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// MemoryCache is an in-memory Cache, safe for concurrent use.
type MemoryCache struct {
	mu sync.Mutex
	m  map[string]Files
}

func memKey(url, commit string) string { return url + "\x00" + commit }

// Get implements Cache.
func (c *MemoryCache) Get(_ context.Context, url, commit string) (Files, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.m[memKey(url, commit)]
	return f, ok, nil
}

// Put implements Cache.
func (c *MemoryCache) Put(_ context.Context, url, commit string, files Files) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Files{}
	}
	c.m[memKey(url, commit)] = files
	return nil
}

// ObjectStore is the subset of the S3 client the cache needs; it is
// implemented by *s3.S3Storage from pkg/storage/s3. GetObject must return an
// error wrapping a NoSuchKey for a missing key.
type ObjectStore interface {
	UploadFile(ctx context.Context, localPath, s3Key string) error
	GetObject(ctx context.Context, s3Key string) (io.ReadCloser, int64, string, error)
}

// markerName is written after all files of an entry, so a reader never sees a
// partially uploaded entry. It lists the file names. It cannot collide with a
// recipe name (those are *.spec, _multibuild and _constraints).
const markerName = ".complete"

// S3Cache stores recipe files in S3 under
// recipes/<sha256(url)>/<commit>/<file>.
type S3Cache struct {
	Store ObjectStore
}

// KeyPrefix returns the S3 key prefix of an entry.
func KeyPrefix(url, commit string) string {
	h := sha256.Sum256([]byte(url))
	return "recipes/" + hex.EncodeToString(h[:]) + "/" + commit + "/"
}

// Get implements Cache.
func (c *S3Cache) Get(ctx context.Context, url, commit string) (Files, bool, error) {
	prefix := KeyPrefix(url, commit)
	raw, ok, err := c.read(ctx, prefix+markerName)
	if err != nil || !ok {
		return nil, false, err
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, false, fmt.Errorf("decode %s%s: %w", prefix, markerName, err)
	}
	files := Files{}
	for _, n := range names {
		data, ok, err := c.read(ctx, prefix+n)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("cache entry %s is missing %s", prefix, n)
		}
		files[n] = data
	}
	return files, true, nil
}

func (c *S3Cache) read(ctx context.Context, key string) ([]byte, bool, error) {
	body, _, _, err := c.Store.GetObject(ctx, key)
	if err != nil {
		var nsk *awss3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, false, nil
		}
		return nil, false, err
	}
	data, err := io.ReadAll(io.LimitReader(body, maxFileSize+1))
	if cerr := body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxFileSize {
		return nil, false, fmt.Errorf("cache object %s is too large", key)
	}
	return data, true, nil
}

// Put implements Cache.
func (c *S3Cache) Put(ctx context.Context, url, commit string, files Files) error {
	prefix := KeyPrefix(url, commit)
	names := make([]string, 0, len(files))
	for n, data := range files {
		if err := c.write(ctx, prefix+n, data); err != nil {
			return err
		}
		names = append(names, n)
	}
	slices.Sort(names)
	marker, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return c.write(ctx, prefix+markerName, marker)
}

func (c *S3Cache) write(ctx context.Context, key string, data []byte) error {
	dir, err := os.MkdirTemp("", "bsgo-recipe-cache-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmp := filepath.Join(dir, "object")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return c.Store.UploadFile(ctx, tmp, key)
}
