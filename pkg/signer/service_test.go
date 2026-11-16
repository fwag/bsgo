package signer_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func writeRepomd(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repodata")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repomd.xml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func verifyRepomd(t *testing.T, repodata, sigPath, keyPath string) {
	t.Helper()
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(read(keyPath)))
	if err != nil {
		t.Fatal(err)
	}
	repomd := read(filepath.Join(repodata, "repomd.xml"))
	if _, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(repomd), bytes.NewReader(read(sigPath)), nil); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	tampered := append([]byte("x"), repomd...)
	if _, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(tampered), bytes.NewReader(read(sigPath)), nil); err == nil {
		t.Fatal("signature verifies a tampered repomd.xml")
	}
}

func TestRepoSignerSignsAndInheritsKeys(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, signer.Config{}, "home", "home:bot")
	rs := signer.NewRepoSigner(f.signer, nil)
	tests := []struct {
		name, project string
		wantNewKeys   int
	}{
		{"creates key on first use", "home", 1},
		{"child inherits the parent key", "home:bot", 0},
		{"unknown child inherits too", "home:bot:sub", 0},
	}
	var parentKey []byte
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := f.daemon.generated
			repodata := writeRepomd(t, "<repomd>"+tt.project+"</repomd>")
			sig, key, err := rs.SignRepositoryMetadata(ctx, repodata, publisher.SignOptions{Project: tt.project})
			if err != nil {
				t.Fatal(err)
			}
			if got := f.daemon.generated - before; got != tt.wantNewKeys {
				t.Errorf("created %d keys, want %d", got, tt.wantNewKeys)
			}
			if filepath.Dir(sig) == repodata || filepath.Base(sig) != "repomd.xml.asc" || filepath.Base(key) != "repomd.xml.key" {
				t.Errorf("unexpected paths %s %s", sig, key)
			}
			verifyRepomd(t, repodata, sig, key)
			k, _ := os.ReadFile(key)
			if parentKey == nil {
				parentKey = k
			} else if !bytes.Equal(parentKey, k) {
				t.Error("child did not use the parent's key")
			}
		})
	}
}

func TestRepoSignerWithoutKeyPublishesUnsignedAndLogsOnce(t *testing.T) {
	f := newFixture(t, signer.Config{NoAutoCreate: true}, "p", "q")
	var logs []string
	rs := signer.NewRepoSigner(f.signer, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	for _, project := range []string{"p", "p", "q"} {
		sig, key, err := rs.SignRepositoryMetadata(context.Background(), writeRepomd(t, "x"), publisher.SignOptions{Project: project})
		if err != nil || sig != "" || key != "" {
			t.Fatalf("%s: got %q %q %v, want unsigned", project, sig, key, err)
		}
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "p ") || !strings.Contains(logs[1], "q ") {
		t.Errorf("want one log per project, got %q", logs)
	}
	if f.daemon.generated != 0 {
		t.Error("key created despite NoAutoCreate")
	}
}

func TestRepoSignerMissingRepomd(t *testing.T) {
	f := newFixture(t, signer.Config{}, "p")
	rs := signer.NewRepoSigner(f.signer, nil)
	if _, _, err := rs.SignRepositoryMetadata(context.Background(), t.TempDir(), publisher.SignOptions{Project: "p"}); err == nil {
		t.Fatal("expected an error without repomd.xml")
	}
}

// TestRepoSignerThroughSigndOverTCP signs real createrepo_c metadata through a
// bsgo-signd server on a loopback listener.
func TestRepoSignerThroughSigndOverTCP(t *testing.T) {
	if _, err := exec.LookPath("createrepo_c"); err != nil {
		t.Skip("createrepo_c not installed")
	}
	mk, err := signd.NewMasterKey("service-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mk.Destroy)
	engine := signd.NewSignEngine(mk, nil)
	engine.SetKeyParams(signd.KeyParams{RSABits: 2048})
	srv := signd.NewServer(engine)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	store := storage.NewMemoryStore()
	err = store.CreateProject(context.Background(), &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "home:bot", Title: "bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rs := signer.NewRepoSigner(signer.New(signd.NewClient(ln.Addr().String()), store, signer.Config{}), nil)

	out := t.TempDir()
	err = publisher.NewRealCreateRepoExecutor().Run(context.Background(), publisher.CreateRepoOptions{
		Dir: t.TempDir(), OutputDir: out, Pkglist: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	repodata := filepath.Join(out, "repodata")
	sig, key, err := rs.SignRepositoryMetadata(context.Background(), repodata, publisher.SignOptions{Project: "home:bot"})
	if err != nil {
		t.Fatal(err)
	}
	verifyRepomd(t, repodata, sig, key)
}
