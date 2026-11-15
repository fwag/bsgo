package dod_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dod"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func primaryXML(names ...string) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">`)
	for _, n := range names {
		fmt.Fprintf(&sb, `<package type="rpm"><name>%s</name><arch>x86_64</arch><version epoch="0" ver="1.0" rel="1"/>`+
			`<checksum type="sha256" pkgid="YES">%s</checksum><format><rpm:provides><rpm:entry name="%s" flags="EQ" epoch="0" ver="1.0" rel="1"/></rpm:provides></format></package>`,
			n, sum256([]byte(n)), n)
	}
	sb.WriteString(`</metadata>`)
	return []byte(sb.String())
}

// fakeRepo is a mutable upstream rpm-md repository.
type fakeRepo struct {
	mu          sync.Mutex
	primary     []byte
	checksum    string // declared sha256 of primary; empty means the real one
	checkType   string
	noPrimary   bool
	repomdGets  int
	primaryGets int
}

func (f *fakeRepo) set(primary []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.primary = primary
}

func (f *fakeRepo) repomd() string {
	sum := f.checksum
	if sum == "" {
		sum = sum256(f.primary)
	}
	typ := f.checkType
	if typ == "" {
		typ = "sha256"
	}
	data := ""
	if !f.noPrimary {
		data = fmt.Sprintf(`<data type="primary"><checksum type="%s">%s</checksum><location href="repodata/%s-primary.xml"/></data>`, typ, sum, sum)
	}
	return `<?xml version="1.0"?><repomd xmlns="http://linux.duke.edu/metadata/repo">` + data +
		`<data type="filelists"><location href="repodata/other.xml"/></data></repomd>`
}

func (f *fakeRepo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/repodata/repomd.xml":
		f.repomdGets++
		_, _ = w.Write([]byte(f.repomd()))
	case strings.HasSuffix(r.URL.Path, "-primary.xml"):
		f.primaryGets++
		_, _ = w.Write(f.primary)
	default:
		http.NotFound(w, r)
	}
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

func resolvable(t *testing.T, blob solv.RepoBlob, pkg string) bool {
	t.Helper()
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{blob})
	if err != nil {
		t.Fatalf("NewPathPool: %v", err)
	}
	defer pp.Close()
	_, err = pp.Expand(nil, pkg)
	return err == nil
}

func TestRepoCacheIngestOncePerRepomd(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	repo.set(primaryXML("alpha", "beta"))
	ts := httptest.NewServer(repo)
	defer ts.Close()

	s3 := newMemoryS3Store()
	store := storage.NewMemoryStore()
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	cfg := dod.RepoCacheConfig{TTL: time.Minute, Clock: clock.now}
	cache := dod.NewRepoCache(nil, s3, store, cfg)

	blob, err := cache.Get(ctx, ts.URL)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	wantSum := sum256(primaryXML("alpha", "beta"))
	wantKey := fmt.Sprintf("dod/%s/%s.solv", sum256([]byte(ts.URL+"/")), wantSum)
	if blob.Hash != wantSum {
		t.Errorf("blob hash = %s, want primary checksum %s", blob.Hash, wantSum)
	}
	if _, ok := s3.objects[wantKey]; !ok {
		t.Errorf("solv blob not stored at %s (have %v)", wantKey, keysOf(s3.objects))
	}
	ptr, err := store.GetDoDRepo(ctx, ts.URL+"/")
	if err != nil || ptr.SolvKey != wantKey || ptr.RepomdChecksum == "" {
		t.Errorf("pointer row = %+v (%v)", ptr, err)
	}
	if !resolvable(t, blob, "alpha") || resolvable(t, blob, "gamma") {
		t.Error("blob does not carry the repository packages")
	}

	steps := []struct {
		name                    string
		advance                 time.Duration
		cache                   func() *dod.RepoCache
		wantRepomd, wantPrimary int
	}{
		{"within TTL makes no request", 10 * time.Second, func() *dod.RepoCache { return cache }, 1, 1},
		{"after TTL only repomd is checked", 2 * time.Minute, func() *dod.RepoCache { return cache }, 2, 1},
		{"restart reuses the stored blob", 0, func() *dod.RepoCache { return dod.NewRepoCache(nil, s3, store, cfg) }, 3, 1},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			clock.t = clock.t.Add(st.advance)
			got, err := st.cache().Get(ctx, ts.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got.Hash != wantSum || !resolvable(t, got, "beta") {
				t.Errorf("unexpected blob %s", got.Hash)
			}
			if repo.repomdGets != st.wantRepomd || repo.primaryGets != st.wantPrimary {
				t.Errorf("repomd/primary requests = %d/%d, want %d/%d", repo.repomdGets, repo.primaryGets, st.wantRepomd, st.wantPrimary)
			}
		})
	}

	repo.set(primaryXML("alpha", "beta", "gamma"))
	clock.t = clock.t.Add(2 * time.Minute)
	updated, err := cache.Get(ctx, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hash == wantSum || !resolvable(t, updated, "gamma") || repo.primaryGets != 2 {
		t.Errorf("changed repomd was not re-ingested: hash=%s primaryGets=%d", updated.Hash, repo.primaryGets)
	}
}

func keysOf(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestRepoCacheRejectsBadMetadata(t *testing.T) {
	tests := []struct {
		name    string
		repo    *fakeRepo
		wantErr string
	}{
		{"checksum mismatch", &fakeRepo{checksum: strings.Repeat("0", 64)}, "sha256 mismatch"},
		{"weak digest type", &fakeRepo{checkType: "sha1"}, "no valid sha256 checksum"},
		{"no primary section", &fakeRepo{noPrimary: true}, "no primary metadata"},
		{"unparsable primary", &fakeRepo{}, "converting primary"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unparsable primary" {
				tc.repo.set([]byte("<not-rpmmd"))
			} else {
				tc.repo.set(primaryXML("alpha"))
			}
			ts := httptest.NewServer(tc.repo)
			defer ts.Close()
			store := storage.NewMemoryStore()
			s3 := newMemoryS3Store()
			cache := dod.NewRepoCache(nil, s3, store, dod.RepoCacheConfig{})
			_, err := cache.Get(context.Background(), ts.URL)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if len(s3.objects) != 0 {
				t.Error("nothing may be stored for rejected metadata")
			}
			if _, err := store.GetDoDRepo(context.Background(), ts.URL+"/"); err != storage.ErrNotFound {
				t.Error("no pointer may be recorded for rejected metadata")
			}
		})
	}
}

func TestRepoCacheUpstreamDown(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	cache := dod.NewRepoCache(nil, newMemoryS3Store(), storage.NewMemoryStore(), dod.RepoCacheConfig{})
	if _, err := cache.Get(context.Background(), ts.URL); err == nil {
		t.Fatal("expected an error when repomd.xml cannot be fetched")
	}
}
