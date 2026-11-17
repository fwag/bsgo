package v1

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// DSSEPayloadType is the payloadType of in-toto attestations in DSSE
// envelopes (OBS bs_signer:signslsaprovenance uses the same).
const DSSEPayloadType = "application/vnd.in-toto+json"

// DSSEEnvelope is a Dead Simple Signing Envelope
// (https://github.com/secure-systems-lab/dsse): the payload is the JSON of an
// in-toto statement, signed over its pre-authentication encoding.
type DSSEEnvelope struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"` // base64 (standard, padded)
	Signatures  []DSSESignature `json:"signatures"`
}

// DSSESignature is one signature of an envelope. KeyID is the fingerprint of
// the signing key (optional in DSSE; bs_signer omits it).
type DSSESignature struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   string `json:"sig"` // base64 of the raw RSA PKCS#1 v1.5 signature over SHA-256(PAE)
}

// DSSEPAE is the pre-authentication encoding that DSSE signs:
// "DSSEv1" SP len(type) SP type SP len(payload) SP payload, lengths in
// decimal bytes.
func DSSEPAE(payloadType string, payload []byte) []byte {
	head := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	return append([]byte(head), payload...)
}

// DSSEDigest is SHA-256 of the pre-authentication encoding, the value the
// signing daemon signs.
func DSSEDigest(payloadType string, payload []byte) []byte {
	sum := sha256.Sum256(DSSEPAE(payloadType, payload))
	return sum[:]
}

// NewDSSEEnvelope builds the envelope of payload with one signature.
func NewDSSEEnvelope(payload, sig []byte, keyID string) *DSSEEnvelope {
	return &DSSEEnvelope{
		PayloadType: DSSEPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []DSSESignature{{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(sig)}},
	}
}

// ParseDSSEEnvelope decodes an envelope; ok is false when data is not one
// (for example an unsigned statement).
func ParseDSSEEnvelope(data []byte) (env *DSSEEnvelope, ok bool) {
	var e DSSEEnvelope
	if err := json.Unmarshal(data, &e); err != nil || e.Payload == "" {
		return nil, false
	}
	return &e, true
}

// Statement decodes the in-toto statement carried by the envelope.
func (e *DSSEEnvelope) Statement() (*InTotoStatement, error) {
	if e.PayloadType != DSSEPayloadType {
		return nil, fmt.Errorf("unexpected DSSE payload type %q", e.PayloadType)
	}
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("decoding DSSE payload: %w", err)
	}
	var st InTotoStatement
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decoding in-toto statement: %w", err)
	}
	return &st, nil
}

// Verify checks that one signature of the envelope is a valid RSA PKCS#1 v1.5
// SHA-256 signature of the payload by pub. A signature that names another
// keyID than wantKeyID (when not empty) is skipped. It returns the statement.
func (e *DSSEEnvelope) Verify(pub *rsa.PublicKey, wantKeyID string) (*InTotoStatement, error) {
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("decoding DSSE payload: %w", err)
	}
	digest := DSSEDigest(e.PayloadType, raw)
	for _, s := range e.Signatures {
		if wantKeyID != "" && !strings.EqualFold(s.KeyID, wantKeyID) {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			continue
		}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig) == nil {
			return e.Statement()
		}
	}
	return nil, errors.New("no valid signature for the given key")
}

// SetSubjectDigests sets the sha256 of the named subjects, adds subjects
// that are missing and returns whether anything changed. Subjects are kept
// sorted by name so repeated runs give identical statements.
func (s *InTotoStatement) SetSubjectDigests(sums map[string]string) bool {
	changed := false
	have := map[string]bool{}
	for i := range s.Subject {
		sub := &s.Subject[i]
		have[sub.Name] = true
		want, ok := sums[sub.Name]
		if !ok || sub.Digest["sha256"] == want {
			continue
		}
		sub.Digest = map[string]string{"sha256": want}
		changed = true
	}
	for name, sum := range sums {
		if !have[name] {
			s.Subject = append(s.Subject, SubjectArtifact{Name: name, Digest: map[string]string{"sha256": sum}})
			changed = true
		}
	}
	if changed {
		sort.Slice(s.Subject, func(i, j int) bool { return s.Subject[i].Name < s.Subject[j].Name })
	}
	return changed
}
