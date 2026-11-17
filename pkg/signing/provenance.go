package signing

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
)

// ProvenanceFile is the name of the SLSA provenance next to the binaries. The
// worker uploads it as an unsigned in-toto statement; the stage replaces it by
// a DSSE envelope that carries the same statement (OBS: bs_signer
// signslsaprovenance rewrites _slsa_provenance.json in place the same way).
const ProvenanceFile = "_slsa_provenance.json"

// RawSigner signs a digest with the project key and returns the raw RSA
// PKCS#1 v1.5 signature and the fingerprint of the key; *signer.Signer
// satisfies it. The private key stays in bsgo-signd.
type RawSigner interface {
	SignRaw(ctx context.Context, project, flavor, hashAlgo string, digest []byte) (sig []byte, fingerprint string, err error)
}

// errNoProvenanceSigner reports a stage that has a provenance to sign but no
// signer configured.
var errNoProvenanceSigner = errors.New("job has a SLSA provenance but the signing stage has no provenance signer")

// provenanceArtifact returns the job's provenance artifact, if the worker
// reported one.
func provenanceArtifact(arts []indexer.Artifact) (indexer.Artifact, bool) {
	for _, a := range arts {
		if a.Filename == ProvenanceFile {
			return a, true
		}
	}
	return indexer.Artifact{}, false
}

// SignEnvelope wraps statement in a DSSE envelope signed with the project
// key: the signature is raw RSA PKCS#1 v1.5 over SHA-256 of the DSSE
// pre-authentication encoding, made by bsgo-signd (RAWSIGN). The keyid is the
// OpenPGP fingerprint of the project key.
func SignEnvelope(ctx context.Context, raw RawSigner, project, flavor string, statement []byte) ([]byte, error) {
	sig, fingerprint, err := raw.SignRaw(ctx, project, flavor, "sha256", v1.DSSEDigest(v1.DSSEPayloadType, statement))
	if err != nil {
		return nil, fmt.Errorf("signing provenance of %s: %w", project, err)
	}
	return json.Marshal(v1.NewDSSEEnvelope(statement, sig, fingerprint))
}

// readStatement decodes a provenance file, which is a bare statement (the
// worker's upload) or an envelope (a previous attempt of this stage).
func readStatement(data []byte) (*v1.InTotoStatement, error) {
	if env, ok := v1.ParseDSSEEnvelope(data); ok {
		return env.Statement()
	}
	var st v1.InTotoStatement
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("decoding provenance: %w", err)
	}
	if st.Type != v1.InTotoStatementType {
		return nil, fmt.Errorf("provenance has statement type %q, want %q", st.Type, v1.InTotoStatementType)
	}
	return &st, nil
}

// signProvenance signs the job's provenance and uploads the envelope over
// the unsigned file. The subjects are updated to the sha256 of the signed
// RPMs (their digests changed when they were signed, OBS fixes them up the
// same way), so the provenance is signed last. The output is a function of
// the statement and the key only, which makes a retry harmless. It returns
// the artifact list with the provenance entry updated; a job without
// provenance is returned unchanged.
func (s *Stage) signProvenance(ctx context.Context, arts []indexer.Artifact, key KeyRef, rpms []*localRPM) ([]indexer.Artifact, error) {
	prov, ok := provenanceArtifact(arts)
	if !ok {
		return arts, nil
	}
	if s.Raw == nil {
		return nil, errNoProvenanceSigner
	}
	dir, err := os.MkdirTemp(s.TempRoot, "bsgo-provenance-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	data, err := s.Objects.ReadObject(ctx, prov.S3Key)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", prov.S3Key, err)
	}
	st, err := readStatement(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", prov.S3Key, err)
	}
	sums := make(map[string]string, len(rpms))
	for _, l := range rpms {
		sum, err := fileSHA256(l.path)
		if err != nil {
			return nil, err
		}
		sums[l.art.Filename] = sum
	}
	st.SetSubjectDigests(sums)
	payload, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	envelope, err := SignEnvelope(ctx, s.Raw, key.Project, key.Flavor, payload)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "envelope.json")
	if err := os.WriteFile(out, envelope, 0o600); err != nil {
		return nil, err
	}
	up, err := s.upload(ctx, out, prov.S3Key, ProvenanceFile)
	if err != nil {
		return nil, err
	}
	return mergeArtifacts(arts, map[string]indexer.Artifact{ProvenanceFile: up}), nil
}

// RSAPublicKey extracts the RSA primary key and the fingerprint (upper case
// hex, the DSSE keyid) from an armored OpenPGP public key.
func RSAPublicKey(armored string) (*rsa.PublicKey, string, error) {
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		return nil, "", fmt.Errorf("reading public key: %w", err)
	}
	if len(ring) != 1 {
		return nil, "", fmt.Errorf("public key block holds %d keys, want 1", len(ring))
	}
	pub, ok := ring[0].PrimaryKey.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, "", errors.New("primary key is not RSA")
	}
	return pub, fmt.Sprintf("%X", ring[0].PrimaryKey.Fingerprint), nil
}

// VerifyProvenance checks a signed provenance file against the armored
// OpenPGP public key of the project and returns its statement: the envelope
// must carry a signature of that key over the recomputed DSSE PAE.
func VerifyProvenance(data []byte, armoredPub string) (*v1.InTotoStatement, error) {
	pub, fingerprint, err := RSAPublicKey(armoredPub)
	if err != nil {
		return nil, err
	}
	env, ok := v1.ParseDSSEEnvelope(data)
	if !ok {
		return nil, errors.New("provenance is not a DSSE envelope")
	}
	return env.Verify(pub, fingerprint)
}
