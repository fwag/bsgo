package efi_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/signd/efi"
)

func createFakePEImage(t *testing.T) []byte {
	t.Helper()
	buf := make([]byte, 256)
	// DOS Header: "MZ" at offset 0, PE offset (0x80) at offset 60
	buf[0] = 'M'
	buf[1] = 'Z'
	binary.LittleEndian.PutUint32(buf[60:64], 0x80)

	// PE Header: "PE\0\0" at offset 0x80
	buf[0x80] = 'P'
	buf[0x81] = 'E'
	buf[0x82] = 0x00
	buf[0x83] = 0x00

	// Simulated code payload
	for i := 0x84; i < len(buf); i++ {
		buf[i] = byte(i)
	}
	return buf
}

func TestAuthenticodeSigning(t *testing.T) {
	signer := efi.NewAuthenticodeSigner()

	peData := createFakePEImage(t)
	digest, err := signer.ComputePEAuthenticodeDigest(peData)
	if err != nil {
		t.Fatalf("failed computing authenticode digest: %v", err)
	}
	if len(digest) != 32 {
		t.Fatalf("expected 32-byte SHA-256 digest, got %d", len(digest))
	}

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed generating rsa key: %v", err)
	}

	tempDir := t.TempDir()
	pePath := filepath.Join(tempDir, "vmlinuz.efi")
	if err := os.WriteFile(pePath, peData, 0755); err != nil {
		t.Fatalf("failed writing fake pe image: %v", err)
	}

	if err := signer.SignEFIImage(pePath, privKey); err != nil {
		t.Fatalf("failed signing EFI image: %v", err)
	}

	signedData, err := os.ReadFile(pePath)
	if err != nil {
		t.Fatalf("failed reading signed pe: %v", err)
	}

	if len(signedData) <= len(peData) {
		t.Errorf("signed PE image must be larger than original PE image")
	}

	// Verify WIN_CERTIFICATE header is present
	certHeader := signedData[len(peData) : len(peData)+8]
	certLen := binary.LittleEndian.Uint32(certHeader[0:4])
	rev := binary.LittleEndian.Uint16(certHeader[4:6])
	certType := binary.LittleEndian.Uint16(certHeader[6:8])

	if rev != 0x0200 {
		t.Errorf("expected WIN_CERT_REVISION_2_0 (0x0200), got 0x%04x", rev)
	}
	if certType != 0x0002 {
		t.Errorf("expected WIN_CERT_TYPE_PKCS_SIGNED_DATA (0x0002), got 0x%04x", certType)
	}
	if certLen != uint32(len(signedData)-len(peData)) {
		t.Errorf("certLen mismatch: header says %d, actual cert size is %d", certLen, len(signedData)-len(peData))
	}
}
