package signd_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()

	go func() {
		_ = server.Serve(ln)
	}()
	defer server.Close()

	client := signd.NewClient(addr)

	keyModel, _ := engine.GenerateProjectKeypair("demo-project", "default", "gpg")
	digest := sha256.Sum256([]byte("test-data-packet"))

	sig, err := client.SignDigest(context.Background(), keyModel.EncryptedPrivateKey, digest[:])
	if err != nil {
		t.Fatalf("client failed to sign digest: %v", err)
	}
	if len(sig) == 0 {
		t.Fatalf("expected non-empty signature")
	}
}

func TestServerCIDRFiltering(t *testing.T) {
	mk, _ := signd.NewMasterKey("cidr-pass")
	defer mk.Destroy()
	engine := signd.NewSignEngine(mk, nil)

	// Server restricting access to non-localhost CIDR
	serverBlocked := signd.NewServer(engine)
	if err := serverBlocked.SetAllowedCIDRs([]string{"10.200.0.0/16"}); err != nil {
		t.Fatalf("failed setting CIDRs: %v", err)
	}

	lnBlocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addrBlocked := lnBlocked.Addr().String()

	go func() {
		_ = serverBlocked.Serve(lnBlocked)
	}()
	defer serverBlocked.Close()

	client := signd.NewClient(addrBlocked)
	keyModel, _ := engine.GenerateProjectKeypair("demo", "default", "gpg")
	digest := sha256.Sum256([]byte("test"))

	_, err = client.SignDigest(context.Background(), keyModel.EncryptedPrivateKey, digest[:])
	if err == nil {
		t.Errorf("expected connection to be rejected due to CIDR filtering")
	}

	// Server allowing localhost
	serverAllowed := signd.NewServer(engine)
	if err := serverAllowed.SetAllowedCIDRs([]string{"127.0.0.1/32"}); err != nil {
		t.Fatalf("failed setting CIDRs: %v", err)
	}
	lnAllowed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addrAllowed := lnAllowed.Addr().String()

	go func() {
		_ = serverAllowed.Serve(lnAllowed)
	}()
	defer serverAllowed.Close()

	clientAllowed := signd.NewClient(addrAllowed)
	sig, err := clientAllowed.SignDigest(context.Background(), keyModel.EncryptedPrivateKey, digest[:])
	if err != nil {
		t.Fatalf("expected allowed client to succeed: %v", err)
	}
	if len(sig) == 0 {
		t.Fatalf("expected signature")
	}
}

func generateTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed generating test rsa key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test bsgo-signd"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed creating test cert: %v", err)
	}

	cert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}

	pool := x509.NewCertPool()
	parsedCert, _ := x509.ParseCertificate(derBytes)
	pool.AddCert(parsedCert)

	return cert, pool
}

func TestServerClientTLS(t *testing.T) {
	mk, _ := signd.NewMasterKey("tls-pass")
	defer mk.Destroy()
	engine := signd.NewSignEngine(mk, nil)

	serverCert, caPool := generateTestCert(t)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
	}

	server := signd.NewServer(engine)
	server.SetTLSConfig(serverTLS)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()

	go func() {
		_ = server.Serve(ln)
	}()
	defer server.Close()

	keyModel, _ := engine.GenerateProjectKeypair("tls-project", "default", "gpg")
	digest := sha256.Sum256([]byte("tls-test-packet"))

	// Non-TLS client should fail handshake
	plainClient := signd.NewClient(addr)
	if _, err := plainClient.SignDigest(context.Background(), keyModel.EncryptedPrivateKey, digest[:]); err == nil {
		t.Errorf("expected plain client to fail against TLS server")
	}

	// TLS client with CA should succeed
	clientTLS := &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}
	tlsClient := signd.NewTLSClient(addr, clientTLS)
	sig, err := tlsClient.SignDigest(context.Background(), keyModel.EncryptedPrivateKey, digest[:])
	if err != nil {
		t.Fatalf("expected TLS client to succeed: %v", err)
	}
	if len(sig) == 0 {
		t.Fatalf("expected non-empty signature from TLS client")
	}
}
