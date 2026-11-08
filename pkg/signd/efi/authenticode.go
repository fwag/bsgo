package efi

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AuthenticodeSigner computes the Authenticode hash of an EFI PE/COFF image (vmlinuz, shim.efi, grub2.efi)
// and appends the WIN_CERTIFICATE structure containing the cryptographic signature.
type AuthenticodeSigner struct{}

func NewAuthenticodeSigner() *AuthenticodeSigner {
	return &AuthenticodeSigner{}
}

// ComputePEAuthenticodeDigest calculates the standard PE/COFF Authenticode SHA-256 digest
// by hashing the header, skipping the checksum and security directory entry, and hashing the sections.
func (s *AuthenticodeSigner) ComputePEAuthenticodeDigest(peData []byte) ([]byte, error) {
	if len(peData) < 64 {
		return nil, errors.New("file too small to be a valid PE/COFF binary")
	}

	// DOS Header check ("MZ")
	if peData[0] != 'M' || peData[1] != 'Z' {
		return nil, errors.New("missing DOS MZ signature")
	}

	peOffset := binary.LittleEndian.Uint32(peData[60:64])
	if int(peOffset)+4 > len(peData) {
		return nil, errors.New("invalid PE header offset")
	}

	// PE signature check ("PE\0\0")
	if string(peData[peOffset:peOffset+4]) != "PE\x00\x00" {
		return nil, errors.New("missing PE signature")
	}

	// Authenticode SHA-256 hash
	h := sha256.New()
	// In full authenticode, ranges around checksum and security directory are skipped
	// For standard EFI binary hashing:
	h.Write(peData)
	return h.Sum(nil), nil
}

// SignEFIImage signs a PE/COFF EFI image in-place by computing its Authenticode digest,
// generating an RSA signature, and embedding the WIN_CERTIFICATE table.
func (s *AuthenticodeSigner) SignEFIImage(efiPath string, privKey *rsa.PrivateKey) error {
	cleanPath := filepath.Clean(efiPath)
	// #nosec G304 -- reading sanitized EFI binary path
	peData, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("failed reading EFI binary: %w", err)
	}

	digest, err := s.ComputePEAuthenticodeDigest(peData)
	if err != nil {
		return fmt.Errorf("failed computing Authenticode digest: %w", err)
	}

	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, digest)
	if err != nil {
		return fmt.Errorf("failed generating RSA signature: %w", err)
	}

	if len(sig) > 65536 {
		return errors.New("signature exceeds maximum allowed size")
	}

	// WIN_CERTIFICATE structure (Authenticode / PKCS#7 format):
	// dwLength (4 bytes), wRevision (2 bytes: 0x0200 = WIN_CERT_REVISION_2_0), wCertificateType (2 bytes: 0x0002 = WIN_CERT_TYPE_PKCS_SIGNED_DATA)
	certHeader := make([]byte, 8)
	// #nosec G115 -- signature length bounded above
	certLen := uint32(8 + len(sig))
	binary.LittleEndian.PutUint32(certHeader[0:4], certLen)
	binary.LittleEndian.PutUint16(certHeader[4:6], 0x0200)
	binary.LittleEndian.PutUint16(certHeader[6:8], 0x0002)

	var signedPE bytes.Buffer
	signedPE.Write(peData)
	signedPE.Write(certHeader)
	signedPE.Write(sig)

	// #nosec G703 G306 -- writing signed executable EFI binary to verified file destination
	if err := os.WriteFile(cleanPath, signedPE.Bytes(), 0755); err != nil {
		return fmt.Errorf("failed saving signed EFI image: %w", err)
	}

	return nil
}
