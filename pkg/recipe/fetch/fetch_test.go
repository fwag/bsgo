package fetch_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixtureRepo creates a bare repo with two commits and returns its path and
// the commit ids. The second commit changes foo.spec and adds a large blob.
func fixtureRepo(t *testing.T) (bare, first, second string) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare = filepath.Join(root, "repo.git")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, work, "init", "-q", "-b", "main")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("foo.spec", "Name: foo\nVersion: 1\n")
	write("_multibuild", "<multibuild><flavor>a</flavor></multibuild>")
	write("foo.tar.gz", "tarball-1")
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "one")
	first = git(t, work, "rev-parse", "HEAD")
	write("foo.spec", "Name: foo\nVersion: 2\n")
	write("_constraints", "<constraints/>")
	write("README", "ignored")
	write("foo.tar.gz", strings.Repeat("x", 1<<20))
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "two")
	second = git(t, work, "rev-parse", "HEAD")
	git(t, root, "clone", "-q", "--bare", work, bare)
	return bare, first, second
}

func TestFetchPinnedCommit(t *testing.T) {
	bare, first, second := fixtureRepo(t)
	f := &fetch.Fetcher{}

	got, err := f.Fetch(context.Background(), bare, first)
	if err != nil {
		t.Fatal(err)
	}
	want := fetch.Files{
		"foo.spec":    []byte("Name: foo\nVersion: 1\n"),
		"_multibuild": []byte("<multibuild><flavor>a</flavor></multibuild>"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("first commit: got %v want %v", got, want)
	}

	got, err = f.Fetch(context.Background(), bare, second)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["foo.spec"]) != "Name: foo\nVersion: 2\n" || string(got["_constraints"]) != "<constraints/>" {
		t.Errorf("second commit: got %v", got)
	}
	if _, ok := got["foo.tar.gz"]; ok {
		t.Error("source tarball must not be returned")
	}
	if _, ok := got["README"]; ok {
		t.Error("non-recipe file must not be returned")
	}
}

func TestFetchErrors(t *testing.T) {
	bare, _, _ := fixtureRepo(t)
	f := &fetch.Fetcher{}
	missing := strings.Repeat("a", 40)
	tests := []struct {
		name, url, commit string
	}{
		{"unknown commit", bare, missing},
		{"branch name", bare, "main"},
		{"abbreviated", bare, "abc1234"},
		{"empty url", "", missing},
		{"option as url", "--upload-pack=x", missing},
		{"unreachable repo", filepath.Join(t.TempDir(), "nope.git"), missing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := f.Fetch(context.Background(), tt.url, tt.commit); err == nil {
				t.Errorf("expected error, got %v", got)
			}
		})
	}
}

type countingCache struct {
	fetch.MemoryCache
	mu         sync.Mutex
	gets, puts int
}

func (c *countingCache) Get(ctx context.Context, url, commit string) (fetch.Files, bool, error) {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
	return c.MemoryCache.Get(ctx, url, commit)
}

func (c *countingCache) Put(ctx context.Context, url, commit string, files fetch.Files) error {
	c.mu.Lock()
	c.puts++
	c.mu.Unlock()
	return c.MemoryCache.Put(ctx, url, commit, files)
}

func TestFetchUsesCache(t *testing.T) {
	bare, first, _ := fixtureRepo(t)
	cache := &countingCache{}
	f := &fetch.Fetcher{Cache: cache}
	if _, err := f.Fetch(context.Background(), bare, first); err != nil {
		t.Fatal(err)
	}
	// Remove the repository: a second fetch must be served from the cache.
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	got, err := f.Fetch(context.Background(), bare, first)
	if err != nil {
		t.Fatalf("cached fetch failed: %v", err)
	}
	if string(got["foo.spec"]) != "Name: foo\nVersion: 1\n" {
		t.Errorf("got %v", got)
	}
	if cache.gets != 2 || cache.puts != 1 {
		t.Errorf("gets=%d puts=%d, want 2 and 1", cache.gets, cache.puts)
	}
}

// fakeS3 is an in-memory fetch.ObjectStore.
type fakeS3 struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (s *fakeS3) UploadFile(_ context.Context, localPath, key string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objs == nil {
		s.objs = map[string][]byte{}
	}
	s.objs[key] = data
	return nil
}

func (s *fakeS3) GetObject(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objs[key]
	if !ok {
		return nil, 0, "", fmt.Errorf("failed to get %s: %w", key, &awss3types.NoSuchKey{})
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), "", nil
}

func TestS3Cache(t *testing.T) {
	store := &fakeS3{}
	c := &fetch.S3Cache{Store: store}
	ctx := context.Background()
	const url, commit = "https://example.com/pkg.git", "0123456789012345678901234567890123456789"

	if _, ok, err := c.Get(ctx, url, commit); err != nil || ok {
		t.Fatalf("empty cache: ok=%v err=%v", ok, err)
	}
	files := fetch.Files{"a.spec": []byte("x"), "_multibuild": []byte("y")}
	if err := c.Put(ctx, url, commit, files); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, url, commit)
	if err != nil || !ok || !reflect.DeepEqual(got, files) {
		t.Fatalf("got %v ok=%v err=%v", got, ok, err)
	}
	prefix := fetch.KeyPrefix(url, commit)
	if !strings.HasPrefix(prefix, "recipes/") || !strings.HasSuffix(prefix, "/"+commit+"/") {
		t.Errorf("prefix %q", prefix)
	}
	if _, ok := store.objs[prefix+"a.spec"]; !ok {
		t.Errorf("object %sa.spec missing; have %v", prefix, keys(store.objs))
	}

	// A different url or commit is a different entry.
	if _, ok, _ := c.Get(ctx, url+"x", commit); ok {
		t.Error("different url must miss")
	}

	// An entry without its completion marker is a miss.
	delete(store.objs, prefix+".complete")
	if _, ok, err := c.Get(ctx, url, commit); ok || err != nil {
		t.Errorf("incomplete entry: ok=%v err=%v", ok, err)
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	return k
}

func TestObjectFormat(t *testing.T) {
	tests := []struct {
		commit, want string
	}{
		{strings.Repeat("a", 64), "sha256"},
		{strings.Repeat("b", 40), "sha1"},
		{"HEAD", "sha1"},
	}
	for _, tt := range tests {
		if got := fetch.ObjectFormat(tt.commit); got != tt.want {
			t.Errorf("ObjectFormat(%q) = %q, want %q", tt.commit, got, tt.want)
		}
	}
}
