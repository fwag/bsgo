package signd

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"time"
)

// SignRaw returns a raw RSA PKCS#1 v1.5 signature over digest, with the key
// of the stored project key. It is the signature of DSSE envelopes (in-toto
// attestations), which carry the bare signature value instead of an OpenPGP
// packet. The digest is H(message) computed by the client; hashName is
// sha256, sha384 or sha512. Like SignDigest it refuses expired keys.
func (e *SignEngine) SignRaw(enc, hashName string, digest []byte, now time.Time) ([]byte, error) {
	h, err := ParseHash(hashName)
	if err != nil {
		return nil, err
	}
	if len(digest) != h.Size() {
		return nil, fmt.Errorf("digest is %d bytes, %s needs %d", len(digest), hashName, h.Size())
	}
	ent, err := e.loadEntity(enc)
	if err != nil {
		return nil, err
	}
	if exp := keyExpiry(ent); !exp.IsZero() && now.After(exp) {
		return nil, fmt.Errorf("signing key expired at %s, extend it first", exp.UTC().Format(time.RFC3339))
	}
	priv, ok := ent.PrivateKey.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("raw signatures need an RSA key")
	}
	sig, err := rsa.SignPKCS1v15(nil, priv, h, digest)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	return sig, nil
}
