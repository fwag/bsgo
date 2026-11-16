//go:build oracle

package oracle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// signedRepo creates real createrepo_c metadata, signs it with the publisher's
// RepoSigner and lays it out like the published repository: root/repodata/
// with repomd.xml, repomd.xml.asc and repomd.xml.key.
func signedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("createrepo_c"); err != nil {
		t.Skip("createrepo_c not installed")
	}
	ctx := context.Background()
	mk, err := signd.NewMasterKey("oracle-repomd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mk.Destroy)
	engine := signd.NewSignEngine(mk, nil)
	engine.SetKeyParams(signd.KeyParams{RSABits: 2048})
	store := storage.NewMemoryStore()
	err = store.CreateProject(ctx, &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "home:bot", Title: "bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rs := signer.NewRepoSigner(signer.New(inProcessSignd{engine}, store, signer.Config{}), nil)

	root := t.TempDir()
	err = publisher.NewRealCreateRepoExecutor().Run(ctx, publisher.CreateRepoOptions{
		Dir: t.TempDir(), OutputDir: root, Pkglist: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	repodata := filepath.Join(root, "repodata")
	sig, key, err := rs.SignRepositoryMetadata(ctx, repodata, publisher.SignOptions{Project: "home:bot"})
	if err != nil {
		t.Fatal(err)
	}
	for src, name := range map[string]string{sig: "repomd.xml.asc", key: "repomd.xml.key"} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repodata, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGPGVerifiesPublishedRepodata runs gpg --verify on the signature of real
// repodata with the exported repomd.xml.key in a temporary GNUPGHOME.
func TestGPGVerifiesPublishedRepodata(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	repodata := filepath.Join(signedRepo(t), "repodata")
	home := filepath.Join(t.TempDir(), "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := gpg(t, home, "--import", filepath.Join(repodata, "repomd.xml.key")); err != nil {
		t.Fatalf("gpg import: %v\n%s", err, out)
	}
	if out, err := gpg(t, home, "--verify", filepath.Join(repodata, "repomd.xml.asc"), filepath.Join(repodata, "repomd.xml")); err != nil {
		t.Fatalf("gpg --verify: %v\n%s", err, out)
	}
}

func zypper(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("zypper", append([]string{"--root", root, "--non-interactive", "--gpg-auto-import-keys"}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestZypperRefreshesSignedRepo adds the repository to a zypper root, imports
// the key and refreshes; refreshing must fail once repomd.xml is tampered
// with. It skips when zypper is missing or cannot run unprivileged.
func TestZypperRefreshesSignedRepo(t *testing.T) {
	if _, err := exec.LookPath("zypper"); err != nil {
		t.Skip("zypper not installed")
	}
	root := signedRepo(t)
	zroot := t.TempDir()
	if out, err := zypper(t, zroot, "ar", "--gpgcheck-strict", "file://"+root, "bsgo-test"); err != nil {
		t.Skipf("zypper cannot run unprivileged here: %v\n%s", err, out)
	}
	if out, err := zypper(t, zroot, "ref", "--force"); err != nil {
		t.Fatalf("zypper ref of the signed repository: %v\n%s", err, out)
	}
	repomd := filepath.Join(root, "repodata", "repomd.xml")
	data, err := os.ReadFile(repomd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repomd, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := zypper(t, zroot, "ref", "--force"); err == nil {
		t.Fatalf("zypper ref accepted a tampered repomd.xml:\n%s", out)
	}
}
