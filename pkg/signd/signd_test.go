package signd_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"

	"github.com/openSUSE/bsgo/pkg/signd"
)

func TestMasterKeyLifecycle(t *testing.T) {
	mk, err := signd.NewMasterKey("super-secret-cluster-master-passphrase")
	if err != nil {
		t.Fatalf("failed creating master key: %v", err)
	}
	defer mk.Destroy()

	secret := []byte("private-key-material-here")
	encryptedHex, err := mk.Encrypt(secret)
	if err != nil {
		t.Fatalf("failed encrypting: %v", err)
	}

	decrypted, err := mk.Decrypt(encryptedHex)
	if err != nil {
		t.Fatalf("failed decrypting: %v", err)
	}

	if string(decrypted) != string(secret) {
		t.Errorf("decrypted secret mismatch: got %s, want %s", string(decrypted), string(secret))
	}

	// Test tampering failure
	_, err = mk.Decrypt("deadbeef00112233445566778899aabbccddeeff")
	if err == nil {
		t.Errorf("expected decryption to fail on corrupt ciphertext")
	}
}

func TestSignEngineProjectKeys(t *testing.T) {
	mk, _ := signd.NewMasterKey("cluster-passphrase")
	defer mk.Destroy()

	engine := signd.NewSignEngine(mk, nil)

	// 1. Generate project keypair
	keyModel, err := engine.GenerateProjectKeypair("openSUSE:Factory", "default", "gpg")
	if err != nil {
		t.Fatalf("failed generating project keypair: %v", err)
	}

	if keyModel.ProjectName != "openSUSE:Factory" {
		t.Errorf("expected openSUSE:Factory, got %s", keyModel.ProjectName)
	}
	if keyModel.PublicKeyArmored == "" || keyModel.EncryptedPrivateKey == "" {
		t.Fatalf("keys should not be empty")
	}

	// 2. Sign a digest
	digest := sha256.Sum256([]byte("payload-to-sign"))
	sig, err := engine.SignDigestWithProjectKey(keyModel.EncryptedPrivateKey, digest[:])
	if err != nil {
		t.Fatalf("failed signing digest: %v", err)
	}

	// 3. Verify signature using public key from database model
	block, _ := pem.Decode([]byte(keyModel.PublicKeyArmored))
	if block == nil {
		t.Fatalf("failed decoding public key pem")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("failed parsing public key: %v", err)
	}
	rsaPub := pub.(*rsa.PublicKey)

	if err := rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
}

func TestServerClientProtocol(t *testing.T) {
	mk, _ := signd.NewMasterKey("test-pass")
	defer mk.Destroy()

	engine := signd.NewSignEngine(mk, nil)
	server := signd.NewServer(engine)

	// Bind to random available local port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	go func() {
		_ = server.Start(addr)
	}()
	defer server.Close()

	client := signd.NewClient(addr)

	// Generate a project key
	keyModel, _ := engine.GenerateProjectKeypair("demo-project", "default", "gpg")
	digest := sha256.Sum256([]byte("test-data-packet"))

	// Request signature over TCP
	sig, err := client.SignDigest(context.Background(), keyModel.EncryptedPrivateKey, digest[:])
	if err != nil {
		t.Fatalf("client failed to sign digest: %v", err)
	}
	if len(sig) == 0 {
		t.Fatalf("expected non-empty signature")
	}
}
