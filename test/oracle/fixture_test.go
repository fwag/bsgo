//go:build oracle

// Package oracle compares the Go dependency expander with the OBS reference
// implementation (/usr/lib/build/expanddeps, pure Perl). Perl is allowed here
// and only here; it never runs in production code paths.
package oracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/solv"
)

const (
	expanddeps       = "/usr/lib/build/expanddeps"
	createrepomddeps = "/usr/lib/build/createrepomddeps"
	zyppRepoDir      = "/var/cache/zypp/raw/download.opensuse.org-oss"
)

// fixture is the shared realistic repository: primary.xml loaded into a warm
// Go pool, and the depfile Perl reads.
type fixture struct {
	label   string
	pool    *solv.PathPool
	depfile string
	primary []byte
	cache   string
	hash    string
}

var (
	fixOnce sync.Once
	fix     *fixture
	fixErr  error
)

func cacheRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), ".cache")
}

func loadFixture(t testing.TB) *fixture {
	t.Helper()
	for _, bin := range []string{expanddeps, createrepomddeps} {
		if _, err := os.Stat(bin); err != nil {
			t.Skipf("OBS build scripts not installed: %v", err)
		}
	}
	fixOnce.Do(func() { fix, fixErr = buildFixture() })
	if fixErr != nil {
		t.Skipf("no repository available for the oracle: %v", fixErr)
	}
	return fix
}

type repomd struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

func buildFixture() (*fixture, error) {
	dir, label, err := resolveRepoDir()
	if err != nil {
		return nil, err
	}
	md, err := os.ReadFile(filepath.Join(dir, "repodata", "repomd.xml"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(md)
	hash := hex.EncodeToString(sum[:])
	cache := filepath.Join(cacheRoot(), hash[:16])
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return nil, err
	}
	primary, err := readPrimary(dir, md)
	if err != nil {
		return nil, err
	}
	depfile := filepath.Join(cache, "deps.txt")
	if _, err := os.Stat(depfile); err != nil {
		out, err := exec.Command(createrepomddeps, dir).Output()
		if err != nil {
			return nil, fmt.Errorf("createrepomddeps: %w", err)
		}
		if err := os.WriteFile(depfile, out, 0o600); err != nil {
			return nil, err
		}
	}
	blob, err := solv.RPMMDToSolv("oracle", primary)
	if err != nil {
		return nil, err
	}
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "oracle", Data: blob, Hash: hash}})
	if err != nil {
		return nil, err
	}
	return &fixture{label: label, pool: pp, depfile: depfile, primary: primary, cache: cache, hash: hash}, nil
}

// resolveRepoDir picks the repository: $BSGO_ORACLE_REPO (directory or URL),
// else the local zypp cache.
func resolveRepoDir() (dir, label string, err error) {
	if env := os.Getenv("BSGO_ORACLE_REPO"); env != "" {
		return repoFromSpec(env)
	}
	if _, e := os.Stat(filepath.Join(zyppRepoDir, "repodata", "repomd.xml")); e == nil {
		return zyppRepoDir, "zypp:" + zyppRepoDir, nil
	}
	return "", "", fmt.Errorf("set BSGO_ORACLE_REPO to an rpm-md directory or URL")
}

func repoFromSpec(spec string) (string, string, error) {
	if !strings.HasPrefix(spec, "http://") && !strings.HasPrefix(spec, "https://") {
		return spec, spec, nil
	}
	sum := sha256.Sum256([]byte(spec))
	dir := filepath.Join(cacheRoot(), "dl-"+hex.EncodeToString(sum[:])[:12])
	if err := os.MkdirAll(filepath.Join(dir, "repodata"), 0o750); err != nil {
		return "", "", err
	}
	base := strings.TrimSuffix(spec, "/") + "/"
	if err := httpGet(base+"repodata/repomd.xml", filepath.Join(dir, "repodata", "repomd.xml")); err != nil {
		return "", "", err
	}
	md, err := os.ReadFile(filepath.Join(dir, "repodata", "repomd.xml"))
	if err != nil {
		return "", "", err
	}
	href, err := primaryHref(md)
	if err != nil {
		return "", "", err
	}
	dst := filepath.Join(dir, "repodata", filepath.Base(href))
	if _, err := os.Stat(dst); err != nil {
		if err := httpGet(base+href, dst); err != nil {
			return "", "", err
		}
	}
	return dir, spec, nil
}

// httpGet downloads url with a single GET request.
func httpGet(url, dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func primaryHref(md []byte) (string, error) {
	var r repomd
	if err := xml.Unmarshal(md, &r); err != nil {
		return "", err
	}
	for _, d := range r.Data {
		if d.Type == "primary" {
			return d.Location.Href, nil
		}
	}
	return "", fmt.Errorf("repomd.xml has no primary entry")
}

// readPrimary returns primary.xml data; zstd is decoded with the zstd tool.
func readPrimary(dir string, md []byte) ([]byte, error) {
	href, err := primaryHref(md)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "repodata", filepath.Base(href))
	if strings.HasSuffix(path, ".zst") {
		return exec.Command("zstd", "-dc", path).Output()
	}
	return os.ReadFile(path)
}
