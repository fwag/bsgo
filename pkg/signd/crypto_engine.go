package signd

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// HardwareTokenSigner is implemented by PKCS#11 / Thales Luna HSM providers.
type HardwareTokenSigner interface {
	SignDigest(slotID uint, keyLabel string, digest []byte) ([]byte, error)
	GetCertificate(slotID uint, keyLabel string) ([]byte, error)
}

// SignEngine manages project key lifecycle, master decryption, and hardware token signing.
type SignEngine struct {
	masterKey *MasterKey
	hsmSigner HardwareTokenSigner
}

func NewSignEngine(masterKey *MasterKey, hsm HardwareTokenSigner) *SignEngine {
	return &SignEngine{
		masterKey: masterKey,
		hsmSigner: hsm,
	}
}

// GenerateProjectKeypair generates a new RSA-2048 keypair, encrypts the private key with the master key,
// and returns a storage.ProjectSigningKey ready for database persistence.
func (e *SignEngine) GenerateProjectKeypair(projectName, flavor, keyType string) (*storage.ProjectSigningKey, error) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed generating rsa key: %w", err)
	}

	privDER := x509.MarshalPKCS1PrivateKey(privKey)
	defer ZeroMemory(privDER)

	encryptedPrivKey, err := e.masterKey.Encrypt(privDER)
	if err != nil {
		return nil, fmt.Errorf("failed encrypting private key: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling public key: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	h := sha256.Sum256(pubDER)
	fingerprint := hex.EncodeToString(h[:])

	return &storage.ProjectSigningKey{
		ProjectName:         projectName,
		KeyFlavor:           flavor,
		KeyType:             keyType,
		PublicKeyArmored:    string(pubPEM),
		EncryptedPrivateKey: encryptedPrivKey,
		Fingerprint:         fingerprint,
	}, nil
}

// SignDigestWithProjectKey decrypts the project's private key into ephemeral memory, computes
// an RSA-PKCS1v15 signature over the SHA-256 digest, and immediately wipes the private key from memory.
func (e *SignEngine) SignDigestWithProjectKey(encryptedPrivKeyHex string, digest []byte) ([]byte, error) {
	if len(digest) == 0 {
		return nil, errors.New("digest cannot be empty")
	}

	privDER, err := e.masterKey.Decrypt(encryptedPrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed decrypting project key: %w", err)
	}
	defer ZeroMemory(privDER)

	privKey, err := x509.ParsePKCS1PrivateKey(privDER)
	if err != nil {
		return nil, fmt.Errorf("failed parsing private key: %w", err)
	}

	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, digest)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	return sig, nil
}

// SignDigestWithHSM delegates signing to a physical Thales Luna HSM partition via PKCS#11.
func (e *SignEngine) SignDigestWithHSM(slotID uint, keyLabel string, digest []byte) ([]byte, error) {
	if e.hsmSigner == nil {
		return nil, errors.New("no hardware token / PKCS#11 provider configured")
	}
	return e.hsmSigner.SignDigest(slotID, keyLabel, digest)
}
