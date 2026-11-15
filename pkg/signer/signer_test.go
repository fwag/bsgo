package signer_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// localDaemon runs the signd engine in process.
type localDaemon struct {
	e          *signd.SignEngine
	generated  int
	extensions int
}

func (d *localDaemon) GenerateKey(_ context.Context, project, flavor string, days int) (*signd.KeyMaterial, error) {
	d.generated++
	return d.e.GenerateKey(project, flavor, days)
}

func (d *localDaemon) ExtendKey(_ context.Context, enc string, days int) (*signd.KeyMaterial, error) {
	d.extensions++
	return d.e.ExtendKey(enc, days, time.Now())
}

func (d *localDaemon) SignatureTrailer(_ context.Context, enc string, p signd.SignParams) ([]byte, error) {
	return d.e.SignatureTrailer(enc, p, time.Now())
}

func (d *localDaemon) SignDigest(_ context.Context, enc string, p signd.SignParams, digest []byte, armored bool) ([]byte, error) {
	return d.e.SignDigest(enc, p, digest, armored, time.Now())
}

type fixture struct {
	daemon *localDaemon
	store  *storage.MemoryStore
	signer *signer.Signer
}

func newFixture(t *testing.T, cfg signer.Config, projects ...string) *fixture {
	t.Helper()
	mk, err := signd.NewMasterKey("signer-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mk.Destroy)
	e := signd.NewSignEngine(mk, nil)
	e.SetKeyParams(signd.KeyParams{RSABits: 2048})
	store := storage.NewMemoryStore()
	for _, p := range projects {
		err := store.CreateProject(context.Background(), &manifest.ProjectManifest{
			SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: p, Title: p},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	d := &localDaemon{e: e}
	return &fixture{daemon: d, store: store, signer: signer.New(d, store, cfg)}
}

func TestProjectChain(t *testing.T) {
	tests := []struct {
		project string
		want    []string
	}{
		{"a:b:c", []string{"a:b:c", "a:b", "a"}},
		{"home:bot", []string{"home:bot", "home"}},
		{"openSUSE", []string{"openSUSE"}},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			if got := signer.ProjectChain(tt.project); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveKeyInheritance(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, signer.Config{}, "a", "a:b", "a:b:c", "x")

	own, err := f.signer.ResolveKey(ctx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if own.ProjectName != "a" || f.daemon.generated != 1 {
		t.Fatalf("expected one key created for a, got %+v (generated %d)", own, f.daemon.generated)
	}

	tests := []struct {
		project   string
		flavor    string
		wantOwner string
		wantNew   int
	}{
		{"a:b:c", "", "a", 0},
		{"a:b", "default", "a", 0},
		{"x", "", "x", 1},                 // no key anywhere: created for x itself
		{"a:b", "2025-01-22", "a:b", 1},   // other flavor is a separate key
		{"a:b:c", "2025-01-22", "a:b", 0}, // and inherited the same way
		{"a:b:c:d", "", "a", 0},           // unknown child project still resolves up
	}
	for _, tt := range tests {
		t.Run(tt.project+"/"+tt.flavor, func(t *testing.T) {
			before := f.daemon.generated
			key, err := f.signer.ResolveKey(ctx, tt.project, tt.flavor)
			if err != nil {
				t.Fatal(err)
			}
			if key.ProjectName != tt.wantOwner {
				t.Errorf("owner %s, want %s", key.ProjectName, tt.wantOwner)
			}
			if got := f.daemon.generated - before; got != tt.wantNew {
				t.Errorf("created %d keys, want %d", got, tt.wantNew)
			}
		})
	}
}

func TestResolveKeyNoAutoCreate(t *testing.T) {
	f := newFixture(t, signer.Config{NoAutoCreate: true}, "a")
	if _, err := f.signer.ResolveKey(context.Background(), "a", ""); !errors.Is(err, signer.ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
	if f.daemon.generated != 0 {
		t.Error("key generated despite NoAutoCreate")
	}
}

func TestLegacyKeyTreatedAsAbsent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, signer.Config{}, "a")
	legacy := &storage.ProjectSigningKey{
		ProjectName: "a", KeyFlavor: "default", KeyType: "gpg",
		PublicKeyArmored: "-----BEGIN PUBLIC KEY-----", EncryptedPrivateKey: "aa", Fingerprint: "sha",
	}
	if err := f.store.SaveProjectKey(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	key, err := f.signer.ResolveKey(ctx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if key.Algo == "" || !strings.Contains(key.PublicKeyArmored, "PGP PUBLIC KEY BLOCK") {
		t.Errorf("legacy key not replaced: %+v", key)
	}
	stored, _ := f.store.GetProjectKey(ctx, "a", "default", "gpg")
	if stored.Algo == "" {
		t.Error("replacement was not persisted")
	}
}

func TestAutoExtendOnUse(t *testing.T) {
	ctx := context.Background()
	// A key with 10 days left is extended by a signer that wants 30 days of margin.
	f := newFixture(t, signer.Config{}, "p")
	m, err := f.daemon.e.GenerateKey("p", "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	k := &storage.ProjectSigningKey{
		ProjectName: "p", KeyFlavor: "default", KeyType: "gpg", PublicKeyArmored: m.PublicKeyArmored,
		EncryptedPrivateKey: m.EncryptedPrivateKey, Fingerprint: m.Fingerprint, Algo: m.Algo, ExpiresAt: m.ExpiresAt,
	}
	if err := f.store.SaveProjectKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	got, err := f.signer.ResolveKey(ctx, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.daemon.extensions != 1 || got.Fingerprint != m.Fingerprint || !got.ExpiresAt.After(time.Now().AddDate(2, 0, 0)) {
		t.Errorf("extensions %d expires %s", f.daemon.extensions, got.ExpiresAt)
	}
	stored, _ := f.store.GetProjectKey(ctx, "p", "default", "gpg")
	if !stored.ExpiresAt.Equal(got.ExpiresAt) {
		t.Error("extension not persisted")
	}
}

func TestManualExtend(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, signer.Config{}, "a", "a:b")
	if _, err := f.signer.ResolveKey(ctx, "a", ""); err != nil {
		t.Fatal(err)
	}
	key, err := f.signer.ExtendKey(ctx, "a:b", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if key.ProjectName != "a" || time.Until(key.ExpiresAt).Hours()/24 < 999 {
		t.Errorf("unexpected extension result %+v", key)
	}
	if _, err := newFixture(t, signer.Config{}, "z").signer.ExtendKey(ctx, "z", "", 1); !errors.Is(err, signer.ErrNoKey) {
		t.Errorf("want ErrNoKey, got %v", err)
	}
}

func TestSignDetachedArmoredAndPublicKey(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, signer.Config{}, "a", "a:b")
	data := []byte(`<?xml version="1.0"?><repomd/>`)

	pub, err := f.signer.PublicKeyArmored(ctx, "a:b", "")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := f.signer.SignDetachedArmored(ctx, data, "a:b", "")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pub))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(data), bytes.NewReader(sig), nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(ring, strings.NewReader("tampered"), bytes.NewReader(sig), nil); err == nil {
		t.Error("tampered data verified")
	}
	if f.daemon.generated != 1 {
		t.Errorf("generated %d keys, want 1 shared by the namespace", f.daemon.generated)
	}
}

func TestSignStreamBinaryAndHashes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, signer.Config{}, "p")
	pub, err := f.signer.PublicKeyArmored(ctx, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pub))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("rpm header bytes")
	for _, hash := range []string{"sha256", "sha512"} {
		for _, armored := range []bool{false, true} {
			sig, err := f.signer.SignStream(ctx, "p", "", bytes.NewReader(data),
				signer.DigestRequest{Hash: hash, Armored: armored, Created: time.Now().Add(-time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			check := openpgp.CheckDetachedSignature
			if armored {
				check = openpgp.CheckArmoredDetachedSignature
			}
			if _, err := check(ring, bytes.NewReader(data), bytes.NewReader(sig), nil); err != nil {
				t.Errorf("%s armored=%v: %v", hash, armored, err)
			}
		}
	}
	if _, err := f.signer.SignStream(ctx, "p", "", bytes.NewReader(data), signer.DigestRequest{Hash: "md5"}); err == nil {
		t.Error("md5 accepted")
	}
}
