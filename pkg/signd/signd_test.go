package signd_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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

func TestServerCIDRFiltering(t *testing.T) {
	mk, _ := signd.NewMasterKey("cidr-pass")
	defer mk.Destroy()
	engine := newTestEngine(mk)

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
	keyModel, err := engine.GenerateKey("demo", "default", 0)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	if _, err = client.PublicKey(context.Background(), keyModel.EncryptedPrivateKey); err == nil {
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
	if _, err := clientAllowed.PublicKey(context.Background(), keyModel.EncryptedPrivateKey); err != nil {
		t.Fatalf("expected allowed client to succeed: %v", err)
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
	engine := newTestEngine(mk)

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

	keyModel, err := engine.GenerateKey("tls-project", "default", 0)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	// Non-TLS client should fail handshake
	plainClient := signd.NewClient(addr)
	if _, err := plainClient.PublicKey(context.Background(), keyModel.EncryptedPrivateKey); err == nil {
		t.Errorf("expected plain client to fail against TLS server")
	}

	// TLS client with CA should succeed
	clientTLS := &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}
	tlsClient := signd.NewTLSClient(addr, clientTLS)
	if _, err := tlsClient.PublicKey(context.Background(), keyModel.EncryptedPrivateKey); err != nil {
		t.Fatalf("expected TLS client to succeed: %v", err)
	}
}
