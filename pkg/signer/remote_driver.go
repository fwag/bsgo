package signer

import (
	"context"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// RemoteSignerDriver connects to bsgo-signd to perform remote cryptographic operations.
type RemoteSignerDriver struct {
	client   SignDigestClient
	keyStore ProjectKeyStore
}

func NewRemoteSignerDriver(client SignDigestClient, keyStore ProjectKeyStore) *RemoteSignerDriver {
	return &RemoteSignerDriver{
		client:   client,
		keyStore: keyStore,
	}
}

// PublicKey retrieves the public key for the project from the key store.
func (r *RemoteSignerDriver) PublicKey(ctx context.Context, opts SignOptions) ([]byte, error) {
	flavor := opts.SignFlavor
	if flavor == "" {
		flavor = "default"
	}
	keyType := opts.SignType
	if keyType == "" {
		keyType = "gpg"
	}

	key, err := r.keyStore.GetProjectKey(ctx, opts.Project, flavor, keyType)
	if err != nil {
		return nil, fmt.Errorf("failed fetching public key for %s: %w", opts.Project, err)
	}
	return []byte(key.PublicKeyArmored), nil
}

// SignDetached computes a detached signature over data using bsgo-signd.
func (r *RemoteSignerDriver) SignDetached(ctx context.Context, data []byte, opts SignOptions) ([]byte, error) {
	flavor := opts.SignFlavor
	if flavor == "" {
		flavor = "default"
	}
	keyType := opts.SignType
	if keyType == "" {
		keyType = "gpg"
	}

	key, err := r.keyStore.GetProjectKey(ctx, opts.Project, flavor, keyType)
	if err != nil {
		return nil, fmt.Errorf("failed fetching signing key for %s: %w", opts.Project, err)
	}

	h := sha256.Sum256(data)
	sig, err := r.client.SignDigest(ctx, key.EncryptedPrivateKey, h[:])
	if err != nil {
		return nil, fmt.Errorf("remote signing failed for %s: %w", opts.Project, err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "SIGNATURE",
		Bytes: sig,
	}), nil
}

// SignClearsign generates a cleartext-signed document.
func (r *RemoteSignerDriver) SignClearsign(ctx context.Context, data []byte, opts SignOptions) ([]byte, error) {
	sig, err := r.SignDetached(ctx, data, opts)
	if err != nil {
		return nil, err
	}
	return append(data, append([]byte("\n-----BEGIN SIGNATURE-----\n"), sig...)...), nil
}

// SignRPM injects a cryptographic signature marker into the RPM.
func (r *RemoteSignerDriver) SignRPM(ctx context.Context, rpmPath string, opts SignOptions) error {
	cleanPath := filepath.Clean(rpmPath)
	// #nosec G304 -- reading sanitized rpm path for signature injection
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return err
	}
	sig, err := r.SignDetached(ctx, data, opts)
	if err != nil {
		return err
	}
	// #nosec G304 -- appending signature to sanitized rpm path
	f, err := os.OpenFile(cleanPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append([]byte("\n[BSGO_SIGNED_RPM]"), sig...))
	return err
}

// VerifyDetached verifies a detached signature using the project's public key.
func (r *RemoteSignerDriver) VerifyDetached(pubKeyPEM, data, sigPEM []byte) error {
	return VerifyRSADetached(pubKeyPEM, data, sigPEM)
}
