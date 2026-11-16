// Package signer is the client side of bsgo-signd. It maps projects to their
// OpenPGP keys (with namespace inheritance like bs_signer:getsignkey), keeps
// keys fresh (autoextend) and offers the signing calls used by the publisher
// (detached armored signatures, public key export) and by the RPM signing
// helper (digest signing).
//
// Private keys never leave bsgo-signd unencrypted. The encrypted key blobs
// live in project_signing_keys and are handed back to signd with each call.
package signer

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const (
	// KeyType is the project_signing_keys key_type of OpenPGP keys.
	KeyType = "gpg"
	// DefaultFlavor is the sign flavor of projects without a signflavor.
	DefaultFlavor = "default"

	// SigTypeBinary is the OpenPGP signature type for binary documents.
	SigTypeBinary byte = 0
)

// ErrNoKey reports that neither the project nor a parent namespace has a
// usable signing key and automatic creation is disabled.
var ErrNoKey = errors.New("no signing key for project")

// Daemon is the part of the signd protocol the signer needs.
type Daemon interface {
	GenerateKey(ctx context.Context, project, flavor string, days int) (*signd.KeyMaterial, error)
	ExtendKey(ctx context.Context, encryptedKey string, days int) (*signd.KeyMaterial, error)
	SignatureTrailer(ctx context.Context, encryptedKey string, p signd.SignParams) ([]byte, error)
	SignDigest(ctx context.Context, encryptedKey string, p signd.SignParams, digest []byte, armored bool) ([]byte, error)
}

// KeyStore persists project keys. storage.Store implements it.
type KeyStore interface {
	GetProjectKey(ctx context.Context, projectName, flavor, keyType string) (*storage.ProjectSigningKey, error)
	SaveProjectKey(ctx context.Context, key *storage.ProjectSigningKey) error
}

// Config tunes key lifetime handling.
type Config struct {
	// KeyDays is the lifetime of generated and extended keys, default 800.
	KeyDays int
	// ExtendBefore extends a key on use when it expires sooner than this,
	// default 30 days. A negative value disables autoextend.
	ExtendBefore time.Duration
	// NoAutoCreate makes ResolveKey return ErrNoKey instead of creating a
	// key for a project without one.
	NoAutoCreate bool
	// Now is the clock, default time.Now.
	Now func() time.Time
}

// Signer resolves keys and signs through bsgo-signd.
type Signer struct {
	daemon Daemon
	store  KeyStore
	cfg    Config
	mu     sync.Mutex // serializes key creation and extension per process
}

// New returns a Signer.
func New(daemon Daemon, store KeyStore, cfg Config) *Signer {
	if cfg.KeyDays == 0 {
		cfg.KeyDays = 800
	}
	if cfg.ExtendBefore == 0 {
		cfg.ExtendBefore = 30 * 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Signer{daemon: daemon, store: store, cfg: cfg}
}

// ProjectChain returns the project and its parent namespaces, most specific
// first: "a:b:c" gives a:b:c, a:b, a.
func ProjectChain(project string) []string {
	var chain []string
	for project != "" {
		chain = append(chain, project)
		i := strings.LastIndex(project, ":")
		if i < 0 {
			break
		}
		project = project[:i]
	}
	return chain
}

func flavorOrDefault(flavor string) string {
	if flavor == "" {
		return DefaultFlavor
	}
	return flavor
}

// findKey returns the first usable key along the namespace chain. Legacy
// keys without an algorithm are skipped.
func (s *Signer) findKey(ctx context.Context, project, flavor string) (*storage.ProjectSigningKey, error) {
	for _, name := range ProjectChain(project) {
		key, err := s.store.GetProjectKey(ctx, name, flavor, KeyType)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("loading signing key of %s: %w", name, err)
		}
		if key.Algo == "" {
			continue
		}
		return key, nil
	}
	return nil, ErrNoKey
}

func toStorage(project, flavor string, m *signd.KeyMaterial) *storage.ProjectSigningKey {
	return &storage.ProjectSigningKey{
		ProjectName:         project,
		KeyFlavor:           flavor,
		KeyType:             KeyType,
		PublicKeyArmored:    m.PublicKeyArmored,
		EncryptedPrivateKey: m.EncryptedPrivateKey,
		Fingerprint:         m.Fingerprint,
		Algo:                m.Algo,
		ExpiresAt:           m.ExpiresAt,
	}
}

func (s *Signer) needsExtension(key *storage.ProjectSigningKey) bool {
	if s.cfg.ExtendBefore < 0 || key.ExpiresAt.IsZero() {
		return false
	}
	return key.ExpiresAt.Sub(s.cfg.Now()) < s.cfg.ExtendBefore
}

// ResolveKey returns the key that signs the project: its own or the one of
// the nearest parent namespace. A key close to expiry is extended in place.
// A project without any key gets one, unless Config.NoAutoCreate is set.
func (s *Signer) ResolveKey(ctx context.Context, project, flavor string) (*storage.ProjectSigningKey, error) {
	flavor = flavorOrDefault(flavor)
	s.mu.Lock()
	defer s.mu.Unlock()

	key, err := s.findKey(ctx, project, flavor)
	if errors.Is(err, ErrNoKey) && !s.cfg.NoAutoCreate {
		return s.createKey(ctx, project, flavor)
	}
	if err != nil {
		return nil, err
	}
	if s.needsExtension(key) {
		return s.extendKey(ctx, key, s.cfg.KeyDays)
	}
	return key, nil
}

func (s *Signer) createKey(ctx context.Context, project, flavor string) (*storage.ProjectSigningKey, error) {
	m, err := s.daemon.GenerateKey(ctx, project, flavor, s.cfg.KeyDays)
	if err != nil {
		return nil, fmt.Errorf("generating signing key for %s: %w", project, err)
	}
	key := toStorage(project, flavor, m)
	if err := s.store.SaveProjectKey(ctx, key); err != nil {
		return nil, fmt.Errorf("saving signing key of %s: %w", project, err)
	}
	return key, nil
}

func (s *Signer) extendKey(ctx context.Context, key *storage.ProjectSigningKey, days int) (*storage.ProjectSigningKey, error) {
	m, err := s.daemon.ExtendKey(ctx, key.EncryptedPrivateKey, days)
	if err != nil {
		return nil, fmt.Errorf("extending signing key of %s: %w", key.ProjectName, err)
	}
	ext := toStorage(key.ProjectName, key.KeyFlavor, m)
	if err := s.store.SaveProjectKey(ctx, ext); err != nil {
		return nil, fmt.Errorf("saving extended signing key of %s: %w", key.ProjectName, err)
	}
	return ext, nil
}

// ExtendKey sets the expiry of the key that signs project to days from now
// (Config.KeyDays when days <= 0) and returns the stored key.
func (s *Signer) ExtendKey(ctx context.Context, project, flavor string, days int) (*storage.ProjectSigningKey, error) {
	flavor = flavorOrDefault(flavor)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := s.findKey(ctx, project, flavor)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = s.cfg.KeyDays
	}
	return s.extendKey(ctx, key, days)
}

// PublicKeyArmored returns the armored OpenPGP public key that verifies
// signatures of the project (repomd.xml.key content).
func (s *Signer) PublicKeyArmored(ctx context.Context, project, flavor string) ([]byte, error) {
	key, err := s.ResolveKey(ctx, project, flavor)
	if err != nil {
		return nil, err
	}
	return []byte(key.PublicKeyArmored), nil
}

// DigestRequest describes one OpenPGP signature over a digest the caller
// computed as H(document || trailer), where the trailer comes from
// Trailer for the same Hash, SigType and Created.
type DigestRequest struct {
	Hash    string // sha256, sha384 or sha512
	SigType byte
	Created time.Time
	Armored bool
}

func (r DigestRequest) params() signd.SignParams {
	return signd.SignParams{Hash: r.Hash, SigType: r.SigType, Created: r.Created.Truncate(time.Second)}
}

// Trailer returns the bytes to hash after the document for the project key.
func (s *Signer) Trailer(ctx context.Context, project, flavor string, r DigestRequest) ([]byte, error) {
	key, err := s.ResolveKey(ctx, project, flavor)
	if err != nil {
		return nil, err
	}
	return s.daemon.SignatureTrailer(ctx, key.EncryptedPrivateKey, r.params())
}

// SignDigest is the low-level call used by the RPM signing helper: it signs
// a digest computed by the caller as H(document || trailer) and returns the
// OpenPGP signature packet (armored on request). Trailer and SignDigest
// resolve the key separately; an autoextension in between does not change
// the fingerprint, so the trailer stays valid.
func (s *Signer) SignDigest(ctx context.Context, project, flavor string, r DigestRequest, digest []byte) ([]byte, error) {
	key, err := s.ResolveKey(ctx, project, flavor)
	if err != nil {
		return nil, err
	}
	return s.daemon.SignDigest(ctx, key.EncryptedPrivateKey, r.params(), digest, r.Armored)
}

// SignStream hashes the document read from r together with the signature
// trailer and returns the OpenPGP signature. The document never reaches
// signd, only its digest.
func (s *Signer) SignStream(ctx context.Context, project, flavor string, r io.Reader, req DigestRequest) ([]byte, error) {
	h, err := signd.ParseHash(req.Hash)
	if err != nil {
		return nil, err
	}
	if req.Created.IsZero() {
		req.Created = s.cfg.Now()
	}
	key, err := s.ResolveKey(ctx, project, flavor)
	if err != nil {
		return nil, err
	}
	trailer, err := s.daemon.SignatureTrailer(ctx, key.EncryptedPrivateKey, req.params())
	if err != nil {
		return nil, err
	}
	digest, err := hashWithTrailer(h, r, trailer)
	if err != nil {
		return nil, err
	}
	return s.daemon.SignDigest(ctx, key.EncryptedPrivateKey, req.params(), digest, req.Armored)
}

func hashWithTrailer(h crypto.Hash, r io.Reader, trailer []byte) ([]byte, error) {
	hs := h.New()
	if _, err := io.Copy(hs, r); err != nil {
		return nil, fmt.Errorf("hashing document: %w", err)
	}
	if _, err := hs.Write(trailer); err != nil {
		return nil, err
	}
	return hs.Sum(nil), nil
}

// SignDetachedArmored returns an ASCII armored OpenPGP detached signature
// (binary document, SHA-256) of data, as published in repomd.xml.asc.
func (s *Signer) SignDetachedArmored(ctx context.Context, data []byte, project, flavor string) ([]byte, error) {
	return s.SignStream(ctx, project, flavor, bytes.NewReader(data), DigestRequest{
		Hash: "sha256", SigType: SigTypeBinary, Armored: true,
	})
}

// Enabled reports whether the RPMs of project are signed: the project or a
// parent namespace has a key, or keys are created on demand.
func (s *Signer) Enabled(ctx context.Context, project, flavor string) (bool, error) {
	if !s.cfg.NoAutoCreate {
		return true, nil
	}
	_, err := s.findKey(ctx, project, flavorOrDefault(flavor))
	if errors.Is(err, ErrNoKey) {
		return false, nil
	}
	return err == nil, err
}
