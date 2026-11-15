//go:build oracle

package oracle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// inProcessSignd runs the signd engine without the network layer.
type inProcessSignd struct{ e *signd.SignEngine }

func (d inProcessSignd) GenerateKey(_ context.Context, project, flavor string, days int) (*signd.KeyMaterial, error) {
	return d.e.GenerateKey(project, flavor, days)
}

func (d inProcessSignd) ExtendKey(_ context.Context, enc string, days int) (*signd.KeyMaterial, error) {
	return d.e.ExtendKey(enc, days, time.Now())
}

func (d inProcessSignd) SignatureTrailer(_ context.Context, enc string, p signd.SignParams) ([]byte, error) {
	return d.e.SignatureTrailer(enc, p, time.Now())
}

func (d inProcessSignd) SignDigest(_ context.Context, enc string, p signd.SignParams, digest []byte, armored bool) ([]byte, error) {
	return d.e.SignDigest(enc, p, digest, armored, time.Now())
}

func gpg(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("gpg", append([]string{"--batch", "--homedir", home}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestGPGVerifiesRepomdSignature checks that gpg, the verifier used by
// zypper and dnf, accepts the detached signature of repomd.xml made through
// the signd digest protocol with a default (RSA-4096, 800 days) project key.
func TestGPGVerifiesRepomdSignature(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	ctx := context.Background()
	mk, err := signd.NewMasterKey("oracle")
	if err != nil {
		t.Fatal(err)
	}
	defer mk.Destroy()
	engine := signd.NewSignEngine(mk, nil)
	store := storage.NewMemoryStore()
	err = store.CreateProject(ctx, &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "home:bot", Title: "bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := signer.New(inProcessSignd{engine}, store, signer.Config{})

	repomd := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo"><revision>1</revision></repomd>
`)
	pub, err := s.PublicKeyArmored(ctx, "home:bot", "")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := s.SignDetachedArmored(ctx, repomd, "home:bot", "")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	keyFile := write("repomd.xml.key", pub)
	sigFile := write("repomd.xml.asc", sig)
	repomdFile := write("repomd.xml", repomd)

	if out, err := gpg(t, home, "--import", keyFile); err != nil {
		t.Fatalf("gpg import: %v\n%s", err, out)
	}
	list, err := gpg(t, home, "--list-keys", "--with-colons")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, list)
	}
	for _, want := range []string{`home\x3abot OBS Project <home\x3abot@bsgo>`, ":4096:1:", "scSC"} {
		if !strings.Contains(list, want) {
			t.Errorf("gpg listing lacks %q:\n%s", want, list)
		}
	}
	if out, err := gpg(t, home, "--verify", sigFile, repomdFile); err != nil {
		t.Fatalf("gpg --verify rejected the signature: %v\n%s", err, out)
	}

	tampered := write("tampered.xml", append([]byte("x"), repomd...))
	if out, err := gpg(t, home, "--verify", sigFile, tampered); err == nil {
		t.Fatalf("gpg accepted a tampered repomd.xml:\n%s", out)
	}
}
