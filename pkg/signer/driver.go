package signer

import (
	"context"
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
// Implementations can communicate with obs-signd (Thales Luna HSM), Cloud KMS, or local keyrings.
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
