package publisher_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
)

// fakeCreateRepo mimics createrepo_c: it lists exactly the Pkglist, takes the
// checksum of a package from its file in Dir or, if the file is absent, from
// the old metadata.
type fakeCreateRepo struct {
	mu    sync.Mutex
	calls []publisher.CreateRepoOptions
	// gate, when set, is called before generating.
	gate func()
	// failWithOld makes a run with old metadata fail, as a createrepo_c that
	// cannot reuse inconsistent metadata would.
	failWithOld bool
	err         error
}

func (f *fakeCreateRepo) Run(_ context.Context, opts publisher.CreateRepoOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, opts)
	f.mu.Unlock()
	if f.gate != nil {
		f.gate()
	}
	if f.err != nil {
		return f.err
	}
	if f.failWithOld && opts.OldRepoDir != "" {
		return fmt.Errorf("fake createrepo: cannot reuse old metadata")
	}
	old := map[string]string{}
	if opts.OldRepoDir != "" {
		if err := readFakePrimary(filepath.Join(opts.OldRepoDir, "repodata"), old); err != nil {
			return err
		}
	}
	var primary strings.Builder
	primary.WriteString(`<?xml version="1.0"?><metadata xmlns="http://linux.duke.edu/metadata/common">`)
	for _, name := range opts.Pkglist {
		sum, err := fakeSum(opts.Dir, name, old)
		if err != nil {
			return err
		}
		fmt.Fprintf(&primary, `<package type="rpm"><name>%s</name><checksum type="sha256" pkgid="YES">%s</checksum><location href="%s"/></package>`, name, sum, name)
	}
	primary.WriteString(`</metadata>`)
	return writeFakeRepodata(filepath.Join(opts.OutputDir, "repodata"), primary.String())
}

func fakeSum(dir, name string, old map[string]string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err == nil {
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:]), nil
	}
	if sum, ok := old[name]; ok {
		return sum, nil
	}
	return "", fmt.Errorf("fake createrepo: %s is neither in %s nor in the old metadata", name, dir)
}

func writeFakeRepodata(dir, primary string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(primary)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	h := sha256.Sum256(buf.Bytes())
	name := hex.EncodeToString(h[:]) + "-primary.xml.gz"
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
		return err
	}
	repomd := fmt.Sprintf(`<?xml version="1.0"?><repomd xmlns="http://linux.duke.edu/metadata/repo"><data type="primary"><location href="repodata/%s"/></data></repomd>`, name)
	return os.WriteFile(filepath.Join(dir, "repomd.xml"), []byte(repomd), 0o600)
}

func readFakePrimary(dir string, into map[string]string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*-primary.xml.gz"))
	if err != nil || len(matches) != 1 {
		return fmt.Errorf("old primary.xml.gz not found in %s: %v", dir, err)
	}
	return readFakePrimaryFile(matches[0], into)
}

func readFakePrimaryFile(file string, into map[string]string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(zr); err != nil {
		return err
	}
	for _, chunk := range strings.Split(raw.String(), "<package ")[1:] {
		sum := between(chunk, `pkgid="YES">`, "<")
		href := between(chunk, `<location href="`, `"`)
		into[href] = sum
	}
	return nil
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	return s[:strings.Index(s, b)]
}

// rpmSpec describes a fake binary.
type rpmSpec struct {
	name, version, release, arch string
	content                      string
}

func (r rpmSpec) filename() string {
	return fmt.Sprintf("%s-%s-%s.%s.rpm", r.name, r.version, r.release, r.arch)
}

func (r rpmSpec) body() []byte {
	if r.content != "" {
		return []byte(r.content)
	}
	return []byte("rpm:" + r.filename())
}

func (r rpmSpec) info() bininfo.BinInfo {
	body := r.body()
	h := sha256.Sum256(body)
	return bininfo.BinInfo{
		Name: r.name, Version: r.version, Release: r.release, Arch: r.arch,
		Filename: r.filename(), SHA256: hex.EncodeToString(h[:]), Size: int64(len(body)),
	}
}

// fakeIndex is an IndexReader backed by a settable entry list.
type fakeIndex struct {
	mu      sync.Mutex
	entries []buildrepo.Entry
	rev     int
}

func (f *fakeIndex) Index(_ context.Context, _ buildrepo.Prpa) (*buildrepo.Index, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := &buildrepo.Index{Entries: append([]buildrepo.Entry(nil), f.entries...)}
	if f.rev > 0 {
		idx.Hash = fmt.Sprintf("rev%d", f.rev)
	}
	return idx, nil
}

// builds simulates a finished job: it uploads the RPMs and the bininfo.json
// of unit and replaces the unit in the index. Debug and source packages go
// to bininfo.json only, as the indexer keeps them out of the build repo.
func (f *fakeIndex) builds(t *testing.T, store *publisher.MemoryStore, p buildrepo.Prpa, unit string, rpms ...rpmSpec) {
	t.Helper()
	prefix := buildrepo.ArtifactPrefix(p, unit)
	var infos []bininfo.BinInfo
	var entries []buildrepo.Entry
	for _, r := range rpms {
		store.Set(prefix+r.filename(), r.body())
		infos = append(infos, r.info())
		if buildrepo.UseForBuild(r.info()) {
			entries = append(entries, buildrepo.Entry{Unit: unit, Key: prefix + r.filename(), BinInfo: r.info()})
		}
	}
	raw, err := bininfo.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	store.Set(prefix+bininfo.FileName, raw)
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.entries[:0:0]
	for _, e := range f.entries {
		if e.Unit != unit {
			kept = append(kept, e)
		}
	}
	f.entries = append(kept, entries...)
	f.rev++
}

type fixedPolicy publisher.Policy

func (p fixedPolicy) Policy(context.Context, buildrepo.Prpa) (publisher.Policy, error) {
	return publisher.Policy(p), nil
}

var testPrpa = buildrepo.Prpa{Project: "SUSE:SLFO:1.2", Repo: "standard", Arch: "x86_64"}

const (
	stdPrefix   = "repos/SUSE:SLFO:1.2/standard/x86_64/"
	debugPrefix = "repos/SUSE:SLFO:1.2/standard/debug/x86_64/"
)

func rpmx(name, version string) rpmSpec {
	return rpmSpec{name: name, version: version, release: "1.1", arch: "x86_64"}
}

// primaryOf returns the file names the published primary.xml.gz of a prefix
// lists.
func primaryOf(t *testing.T, store *publisher.MemoryStore, prefix string) map[string]string {
	t.Helper()
	repomd, err := store.ReadObject(context.Background(), prefix+"repodata/repomd.xml")
	if err != nil {
		t.Fatalf("no repomd.xml under %s: %v", prefix, err)
	}
	href := between(string(repomd), `<location href="`, `"`)
	local := filepath.Join(t.TempDir(), "primary.xml.gz")
	if err := store.DownloadFile(context.Background(), prefix+href, local); err != nil {
		t.Fatal(err)
	}
	store.TakeDownloads()
	out := map[string]string{}
	if err := readFakePrimaryFile(local, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func names(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
