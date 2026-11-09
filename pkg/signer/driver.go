package signer

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// SignOptions provides context for signing operations including project and hardware token scoping.
type SignOptions struct {
	Project    string
	Package    string
	SignFlavor string
	SignType   string // "gpg", "cms", "appx"
	HashAlgo   string // "sha256", "sha512"
	KeyFile    string // optional private key file override
}

// SignerDriver defines the pluggable interface for signing packages, metadata, and attestations.
// Implementations can communicate with bsgo-signd (Thales Luna HSM), Cloud KMS, or local keyrings.
type SignerDriver interface {
	// PublicKey retrieves the public key for the project/keyring.
	PublicKey(ctx context.Context, opts SignOptions) ([]byte, error)

	// SignDetached computes a detached signature over data.
	SignDetached(ctx context.Context, data []byte, opts SignOptions) ([]byte, error)

	// SignClearsign generates a cleartext-signed document.
	SignClearsign(ctx context.Context, data []byte, opts SignOptions) ([]byte, error)

	// SignRPM injects a cryptographic signature into the RPM header.
	SignRPM(ctx context.Context, rpmPath string, opts SignOptions) error
}

// ProjectKeyStore retrieves stored project keys from database or memory.
type ProjectKeyStore interface {
	GetProjectKey(ctx context.Context, projectName, flavor, keyType string) (*storage.ProjectSigningKey, error)
	SaveProjectKey(ctx context.Context, key *storage.ProjectSigningKey) error
}

// SignDigestClient represents a client capable of signing digest over network or IPC.
type SignDigestClient interface {
	SignDigest(ctx context.Context, encryptedPrivKeyHex string, digest []byte) ([]byte, error)
	GenerateKey(ctx context.Context, project, flavor, keyType string) (*storage.ProjectSigningKey, error)
}

// VerifyRSADetached parses an RSA public key and verifies a detached signature over SHA-256 digest of data.
func VerifyRSADetached(pubKeyPEM, data, sigPEM []byte) error {
	block, _ := pem.Decode(pubKeyPEM)
	if block == nil {
		return fmt.Errorf("invalid public key pem")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed parsing public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("not an rsa public key")
	}

	sigBlock, _ := pem.Decode(sigPEM)
	if sigBlock == nil {
		return fmt.Errorf("invalid signature pem")
	}

	h := sha256.Sum256(data)
	return rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, h[:], sigBlock.Bytes)
}
