package signd

import "errors"

// HardwareTokenSigner is implemented by PKCS#11 / Thales Luna HSM providers.
type HardwareTokenSigner interface {
	SignDigest(slotID uint, keyLabel string, digest []byte) ([]byte, error)
	GetCertificate(slotID uint, keyLabel string) ([]byte, error)
}

// SignEngine manages OpenPGP project keys: generation, extension, master
// key encryption at rest, and signing of client supplied digests. It is
// stateless; callers store the encrypted key and pass it with each request,
// like obs-signd's -P option.
type SignEngine struct {
	masterKey *MasterKey
	hsmSigner HardwareTokenSigner
	params    KeyParams
}

func NewSignEngine(masterKey *MasterKey, hsm HardwareTokenSigner) *SignEngine {
	return &SignEngine{
		masterKey: masterKey,
		hsmSigner: hsm,
		params:    KeyParams{}.withDefaults(),
	}
}

// SetKeyParams overrides the key generation defaults (user ID domain, RSA
// size, lifetime).
func (e *SignEngine) SetKeyParams(p KeyParams) {
	e.params = p.withDefaults()
}

// SignDigestWithHSM delegates signing to a physical Thales Luna HSM partition via PKCS#11.
func (e *SignEngine) SignDigestWithHSM(slotID uint, keyLabel string, digest []byte) ([]byte, error) {
	if e.hsmSigner == nil {
		return nil, errors.New("no hardware token / PKCS#11 provider configured")
	}
	return e.hsmSigner.SignDigest(slotID, keyLabel, digest)
}
