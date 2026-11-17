package signd_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/signd"
)

func TestSignRawVerifies(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t)
	m, err := e.GenerateKey("demo", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := keyring(t, m.PublicKeyArmored)[0].PrimaryKey.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatal("primary key is not RSA")
	}
	msg := sha256.Sum256([]byte("pae"))
	other := sha256.Sum256([]byte("other"))
	client := signd.NewClient(startServer(t, e))

	tests := []struct {
		name string
		sign func() ([]byte, error)
	}{
		{"engine", func() ([]byte, error) { return e.SignRaw(m.EncryptedPrivateKey, "sha256", msg[:], time.Now()) }},
		{"protocol", func() ([]byte, error) { return client.SignRaw(ctx, m.EncryptedPrivateKey, "sha256", msg[:]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig, err := tt.sign()
			if err != nil {
				t.Fatal(err)
			}
			if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, msg[:], sig); err != nil {
				t.Fatalf("verify: %v", err)
			}
			if rsa.VerifyPKCS1v15(pub, crypto.SHA256, other[:], sig) == nil {
				t.Fatal("signature verifies another digest")
			}
		})
	}
}

func TestSignRawRejects(t *testing.T) {
	e := testEngine(t)
	m, err := e.GenerateKey("demo", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	good := make([]byte, 32)
	tests := []struct {
		name   string
		enc    string
		hash   string
		digest []byte
		now    time.Time
	}{
		{"unknown hash", m.EncryptedPrivateKey, "md5", good, time.Now()},
		{"sha1 refused", m.EncryptedPrivateKey, "sha1", make([]byte, 20), time.Now()},
		{"short digest", m.EncryptedPrivateKey, "sha256", good[:16], time.Now()},
		{"bad key blob", "00", "sha256", good, time.Now()},
		{"expired key", m.EncryptedPrivateKey, "sha256", good, m.ExpiresAt.Add(time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.SignRaw(tt.enc, tt.hash, tt.digest, tt.now); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
