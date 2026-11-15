package signd

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
	"math"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// deterministic disables go-crypto's random salt notation. The salt is added
// while signing and would change the hashed trailer between the TRAILER and
// SIGN calls; RSA PKCS#1 v1.5 signatures do not need it. Signatures made
// with this config are also reproducible, which keeps public keys stable
// when a key is re-serialized.
var deterministic = false

const (
	secondsPerDay = 24 * 60 * 60
	maxKeyDays    = 36500
)

// KeyMaterial is everything a client persists about a project key. The
// private key never leaves signd unencrypted: EncryptedPrivateKey is the
// OpenPGP private entity encrypted with the master key.
type KeyMaterial struct {
	PublicKeyArmored    string
	EncryptedPrivateKey string
	Fingerprint         string
	Algo                string
	ExpiresAt           time.Time
}

// SignParams selects the OpenPGP signature parameters that become part of
// the hashed trailer.
type SignParams struct {
	Hash    string    // sha256, sha384 or sha512
	SigType byte      // 0 binary document, 1 canonical text document
	Created time.Time // signature creation time, second resolution
}

// KeyParams configures key generation.
type KeyParams struct {
	// ObsName is the domain of the user ID email, default "bsgo".
	ObsName string
	// RSABits is the key size, default 4096.
	RSABits int
	// DefaultDays is the key lifetime when a request gives none, default 800.
	DefaultDays int
}

func (p KeyParams) withDefaults() KeyParams {
	if p.ObsName == "" {
		p.ObsName = "bsgo"
	}
	if p.RSABits == 0 {
		p.RSABits = 4096
	}
	if p.DefaultDays == 0 {
		p.DefaultDays = 800
	}
	return p
}

var hashByName = map[string]crypto.Hash{
	"sha256": crypto.SHA256,
	"sha384": crypto.SHA384,
	"sha512": crypto.SHA512,
}

// ParseHash maps a protocol hash name to its crypto.Hash.
func ParseHash(name string) (crypto.Hash, error) {
	h, ok := hashByName[strings.ToLower(name)]
	if !ok {
		return 0, fmt.Errorf("unsupported hash algorithm %q", name)
	}
	return h, nil
}

// userID returns the OBS style user ID parts for a project and flavor.
func userID(project, flavor, obsName string) (name, comment, email string) {
	name = project + " OBS Project"
	if flavor != "" && flavor != "default" {
		comment = flavor
	}
	return name, comment, project + "@" + obsName
}

// generateEntity creates a v4 RSA primary key that can sign and certify.
func generateEntity(project, flavor string, days int, p KeyParams) (*openpgp.Entity, error) {
	lifetime, err := lifetimeSecs(0, days)
	if err != nil {
		return nil, err
	}
	cfg := &packet.Config{
		Algorithm:       packet.PubKeyAlgoRSA,
		RSABits:         p.RSABits,
		KeyLifetimeSecs: lifetime,
		DefaultHash:     crypto.SHA256,

		NonDeterministicSignaturesViaNotation: &deterministic,
	}
	name, comment, email := userID(project, flavor, p.ObsName)
	e, err := openpgp.NewEntity(name, comment, email, cfg)
	if err != nil {
		return nil, fmt.Errorf("generating openpgp key: %w", err)
	}
	// OBS keys are a single signing primary key; drop the encryption subkey.
	e.Subkeys = nil
	return e, nil
}

// lifetimeSecs returns the OpenPGP key lifetime (seconds after key creation)
// of a key that is age old and shall live days more.
func lifetimeSecs(age time.Duration, days int) (uint32, error) {
	if err := validateDays(days); err != nil {
		return 0, err
	}
	secs := int64(age/time.Second) + int64(days)*secondsPerDay
	if secs < 0 || secs > math.MaxUint32 {
		return 0, fmt.Errorf("key lifetime of %d seconds out of range", secs)
	}
	return uint32(secs), nil
}

func validateDays(days int) error {
	if days < 1 || days > maxKeyDays {
		return fmt.Errorf("key lifetime %d days out of range 1-%d", days, maxKeyDays)
	}
	return nil
}

func serializePrivate(e *openpgp.Entity) ([]byte, error) {
	var buf bytes.Buffer
	if err := e.SerializePrivate(&buf, &packet.Config{NonDeterministicSignaturesViaNotation: &deterministic}); err != nil {
		return nil, fmt.Errorf("serializing private key: %w", err)
	}
	return buf.Bytes(), nil
}

func armoredPublic(e *openpgp.Entity) (string, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", err
	}
	if err := e.Serialize(w); err != nil {
		return "", fmt.Errorf("serializing public key: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func keyExpiry(e *openpgp.Entity) time.Time {
	sig, _ := e.PrimarySelfSignature()
	if sig == nil || sig.KeyLifetimeSecs == nil || *sig.KeyLifetimeSecs == 0 {
		return time.Time{}
	}
	return e.PrimaryKey.CreationTime.Add(time.Duration(*sig.KeyLifetimeSecs) * time.Second)
}

func entityBits(ent *openpgp.Entity) int {
	bits, err := ent.PrimaryKey.BitLength()
	if err != nil {
		return 0
	}
	return int(bits)
}

func (e *SignEngine) material(ent *openpgp.Entity) (*KeyMaterial, error) {
	priv, err := serializePrivate(ent)
	if err != nil {
		return nil, err
	}
	defer ZeroMemory(priv)
	enc, err := e.masterKey.Encrypt(priv)
	if err != nil {
		return nil, fmt.Errorf("encrypting private key: %w", err)
	}
	pub, err := armoredPublic(ent)
	if err != nil {
		return nil, err
	}
	return &KeyMaterial{
		PublicKeyArmored:    pub,
		EncryptedPrivateKey: enc,
		Fingerprint:         fmt.Sprintf("%X", ent.PrimaryKey.Fingerprint),
		Algo:                fmt.Sprintf("rsa%d", entityBits(ent)),
		ExpiresAt:           keyExpiry(ent),
	}, nil
}

// GenerateKey creates an OpenPGP key for a project and flavor. days <= 0
// selects the configured default lifetime.
func (e *SignEngine) GenerateKey(project, flavor string, days int) (*KeyMaterial, error) {
	if project == "" {
		return nil, errors.New("project name required")
	}
	if days <= 0 {
		days = e.params.DefaultDays
	}
	if err := validateDays(days); err != nil {
		return nil, err
	}
	ent, err := generateEntity(project, flavor, days, e.params)
	if err != nil {
		return nil, err
	}
	return e.material(ent)
}

// loadEntity decrypts a stored key. The caller must not retain it.
func (e *SignEngine) loadEntity(enc string) (*openpgp.Entity, error) {
	plain, err := e.masterKey.Decrypt(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypting project key: %w", err)
	}
	defer ZeroMemory(plain)
	ent, err := openpgp.ReadEntity(packet.NewReader(bytes.NewReader(plain)))
	if err != nil {
		return nil, fmt.Errorf("parsing project key: %w", err)
	}
	if ent.PrivateKey == nil || ent.PrivateKey.Encrypted {
		return nil, errors.New("stored key has no usable private key")
	}
	return ent, nil
}

// PublicKey returns the public key material of a stored key.
func (e *SignEngine) PublicKey(enc string) (*KeyMaterial, error) {
	ent, err := e.loadEntity(enc)
	if err != nil {
		return nil, err
	}
	m, err := e.material(ent)
	if err != nil {
		return nil, err
	}
	m.EncryptedPrivateKey = enc
	return m, nil
}

// ExtendKey sets the key to expire days from now and returns the updated
// key material, like "sign -x <days>" of obs-signd.
func (e *SignEngine) ExtendKey(enc string, days int, now time.Time) (*KeyMaterial, error) {
	if err := validateDays(days); err != nil {
		return nil, err
	}
	ent, err := e.loadEntity(enc)
	if err != nil {
		return nil, err
	}
	id := ent.PrimaryIdentity()
	if id == nil || id.SelfSignature == nil {
		return nil, errors.New("key has no self-signed user id")
	}
	lifetime, err := lifetimeSecs(now.Sub(ent.PrimaryKey.CreationTime), days)
	if err != nil {
		return nil, err
	}
	id.SelfSignature.KeyLifetimeSecs = &lifetime
	id.SelfSignature.CreationTime = now
	// SerializePrivate re-signs the self-signature with the new fields.
	return e.material(ent)
}

// digestHash is a hash.Hash that records what is written to it and returns
// a fixed digest from Sum. go-crypto's Signature.Sign writes the hashed
// trailer into it and signs the digest, which lets signd sign a digest the
// client computed without ever seeing the document.
type digestHash struct {
	written bytes.Buffer
	digest  []byte
	size    int
	block   int
}

func (d *digestHash) Write(p []byte) (int, error) { return d.written.Write(p) }
func (d *digestHash) Sum(b []byte) []byte         { return append(b, d.digest...) }
func (d *digestHash) Reset()                      { d.written.Reset() }
func (d *digestHash) Size() int                   { return d.size }
func (d *digestHash) BlockSize() int              { return d.block }

var _ hash.Hash = (*digestHash)(nil)

func newDigestHash(h crypto.Hash, digest []byte) *digestHash {
	block := sha512.BlockSize
	if h == crypto.SHA256 {
		block = sha256.BlockSize
	}
	return &digestHash{digest: digest, size: h.Size(), block: block}
}

func checkParams(p SignParams) (crypto.Hash, error) {
	h, err := ParseHash(p.Hash)
	if err != nil {
		return 0, err
	}
	if p.SigType != byte(packet.SigTypeBinary) && p.SigType != byte(packet.SigTypeText) {
		return 0, fmt.Errorf("unsupported signature type %d (only 0 binary and 1 text documents)", p.SigType)
	}
	return h, nil
}

// sign runs go-crypto's signing over a digestHash and returns the resulting
// signature and the bytes it hashed after the document (the trailer).
func (e *SignEngine) sign(enc string, p SignParams, digest []byte, now time.Time) (*packet.Signature, []byte, error) {
	h, err := checkParams(p)
	if err != nil {
		return nil, nil, err
	}
	if len(digest) != h.Size() {
		return nil, nil, fmt.Errorf("digest is %d bytes, %s needs %d", len(digest), p.Hash, h.Size())
	}
	ent, err := e.loadEntity(enc)
	if err != nil {
		return nil, nil, err
	}
	if exp := keyExpiry(ent); !exp.IsZero() && now.After(exp) {
		return nil, nil, fmt.Errorf("signing key expired at %s, extend it first", exp.UTC().Format(time.RFC3339))
	}
	sig := &packet.Signature{
		Version:      ent.PrivateKey.Version,
		SigType:      packet.SignatureType(p.SigType),
		PubKeyAlgo:   ent.PrivateKey.PubKeyAlgo,
		Hash:         h,
		CreationTime: p.Created,
		IssuerKeyId:  &ent.PrivateKey.KeyId,
	}
	dh := newDigestHash(h, digest)
	cfg := &packet.Config{Rand: rand.Reader, DefaultHash: h, NonDeterministicSignaturesViaNotation: &deterministic}
	if err := sig.Sign(dh, ent.PrivateKey, cfg); err != nil {
		return nil, nil, fmt.Errorf("signing: %w", err)
	}
	return sig, dh.written.Bytes(), nil
}

// SignatureTrailer returns the bytes that the client must hash after the
// signed document, before finalizing the digest it sends to SignDigest.
func (e *SignEngine) SignatureTrailer(enc string, p SignParams, now time.Time) ([]byte, error) {
	h, err := checkParams(p)
	if err != nil {
		return nil, err
	}
	_, trailer, err := e.sign(enc, p, make([]byte, h.Size()), now)
	return trailer, err
}

// SignDigest returns an OpenPGP v4 signature packet over a digest that the
// client computed as H(document || trailer). With armored set the result is
// an ASCII armored detached signature.
func (e *SignEngine) SignDigest(enc string, p SignParams, digest []byte, armored bool, now time.Time) ([]byte, error) {
	sig, _, err := e.sign(enc, p, digest, now)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if !armored {
		if err := sig.Serialize(&buf); err != nil {
			return nil, fmt.Errorf("serializing signature: %w", err)
		}
		return buf.Bytes(), nil
	}
	w, err := armor.Encode(&buf, openpgp.SignatureType, nil)
	if err != nil {
		return nil, err
	}
	if err := sig.Serialize(w); err != nil {
		return nil, fmt.Errorf("serializing signature: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
