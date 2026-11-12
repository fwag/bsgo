package solv_test

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/openSUSE/bsgo/pkg/solv"
)

func TestLoadRepoFromRPMMDWithSHA256(t *testing.T) {
	primaryXML := `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="1">
<package type="rpm">
  <name>libcrypto-util</name>
  <arch>x86_64</arch>
  <version epoch="0" ver="3.0.0" rel="1.1"/>
  <checksum type="sha256" pkgid="YES">e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855</checksum>
  <summary>Crypto library utilities</summary>
  <description>Shared cryptographic utility package.</description>
  <format>
    <rpm:provides>
      <rpm:entry name="libcrypto-util" flags="EQ" epoch="0" ver="3.0.0" rel="1.1"/>
      <rpm:entry name="libcrypto.so.3()(64bit)"/>
    </rpm:provides>
    <rpm:requires>
    </rpm:requires>
  </format>
</package>
</metadata>`

	// Gzip compress the primary XML
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(primaryXML)); err != nil {
		t.Fatalf("failed compressing primary XML: %v", err)
	}
	_ = gw.Close()

	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed creating pool: %v", err)
	}
	defer pool.Close()

	repo, err := pool.LoadRepoFromRPMMD("crypto-repo", buf.Bytes())
	if err != nil {
		t.Fatalf("LoadRepoFromRPMMD failed: %v", err)
	}
	if repo == nil {
		t.Fatalf("expected non-nil repo returned")
	}

	req := solv.ExpansionRequest{
		BuildRequires: []string{"libcrypto.so.3()(64bit)"},
		Architecture:  "x86_64",
	}

	res, err := pool.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(res.Packages) != 1 {
		t.Fatalf("expected 1 package resolved, got %d", len(res.Packages))
	}

	pkg := res.Packages[0]
	if pkg.Name != "libcrypto-util" {
		t.Errorf("expected package name libcrypto-util, got %s", pkg.Name)
	}
	if pkg.EVR != "3.0.0-1.1" {
		t.Errorf("expected version 3.0.0-1.1, got %s", pkg.EVR)
	}

	expectedSHA := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if pkg.SHA256 != expectedSHA {
		t.Errorf("expected authentic SHA-256 %s, got %s", expectedSHA, pkg.SHA256)
	}
}
