package signd

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// MasterKey manages symmetric encryption/decryption of project private keys in memory.
// Modeled after hardware security appliance master keys, ensuring project private keys
// stored in PostgreSQL can only be decrypted by the isolated bare-metal signer daemon.
type MasterKey struct {
	key []byte
}

func NewMasterKey(passphrase string) (*MasterKey, error) {
	if passphrase == "" {
		return nil, errors.New("master key passphrase cannot be empty")
	}

	h := sha256.Sum256([]byte(passphrase))
	keyCopy := make([]byte, len(h))
	copy(keyCopy, h[:])

	return &MasterKey{key: keyCopy}, nil
}

// Destroy zeroes out the master key in memory to prevent memory scraping.
func (m *MasterKey) Destroy() {
	if m.key != nil {
		for i := range m.key {
			m.key[i] = 0
		}
		m.key = nil
	}
}

// Encrypt encrypts project private key plaintext using AES-256-GCM and returns a hex string.
func (m *MasterKey) Encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(m.key)
	if err != nil {
		return "", fmt.Errorf("failed creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return hex.EncodeToString(ciphertext), nil
}

// Decrypt decrypts the hex-encoded ciphertext and returns plaintext in an ephemeral buffer.
func (m *MasterKey) Decrypt(ciphertextHex string) ([]byte, error) {
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext hex: %w", err)
	}

	block, err := aes.NewCipher(m.key)
	if err != nil {
		return nil, fmt.Errorf("failed creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (corrupt data or invalid master key): %w", err)
	}

	return plaintext, nil
}

// ZeroMemory securely wipes a sensitive buffer from RAM.
func ZeroMemory(buf []byte) {
	if len(buf) == 0 {
		return
	}
	zeros := make([]byte, len(buf))
	subtle.ConstantTimeCopy(1, buf, zeros)
}
