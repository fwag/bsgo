package v1

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"testing"
)

func TestDSSEPAE(t *testing.T) {
	tests := []struct {
		name, typ, payload, want string
	}{
		{"spec example", "http://example.com/HelloWorld", "hello world", "DSSEv1 29 http://example.com/HelloWorld 11 hello world"},
		{"empty payload", "t", "", "DSSEv1 1 t 0 "},
		{"lengths count bytes", "t", "é", "DSSEv1 1 t 2 é"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(DSSEPAE(tt.typ, []byte(tt.payload))); got != tt.want {
				t.Fatalf("PAE = %q, want %q", got, tt.want)
			}
		})
	}
}

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signPayload(t *testing.T, k *rsa.PrivateKey, payload []byte, keyID string) *DSSEEnvelope {
	t.Helper()
	sig, err := rsa.SignPKCS1v15(nil, k, crypto.SHA256, DSSEDigest(DSSEPayloadType, payload))
	if err != nil {
		t.Fatal(err)
	}
	return NewDSSEEnvelope(payload, sig, keyID)
}

func TestDSSEEnvelopeVerify(t *testing.T) {
	key, other := testRSAKey(t), testRSAKey(t)
	st := &InTotoStatement{Type: InTotoStatementType, PredicateType: SLSAProvenanceType,
		Subject: []SubjectArtifact{{Name: "a.rpm", Digest: map[string]string{"sha256": "aa"}}}}
	payload, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	good := signPayload(t, key, payload, "ABCD")

	tests := []struct {
		name    string
		env     func() *DSSEEnvelope
		pub     *rsa.PublicKey
		keyID   string
		wantErr bool
	}{
		{"valid", func() *DSSEEnvelope { return good }, &key.PublicKey, "", false},
		{"keyid case-insensitive", func() *DSSEEnvelope { return good }, &key.PublicKey, "abcd", false},
		{"wrong keyid", func() *DSSEEnvelope { return good }, &key.PublicKey, "FFFF", true},
		{"wrong key", func() *DSSEEnvelope { return good }, &other.PublicKey, "", true},
		{"tampered payload", func() *DSSEEnvelope {
			e := *good
			e.Payload = NewDSSEEnvelope([]byte(`{"x":1}`), nil, "").Payload
			return &e
		}, &key.PublicKey, "", true},
		{"no signatures", func() *DSSEEnvelope { e := *good; e.Signatures = nil; return &e }, &key.PublicKey, "", true},
		{"bad base64 sig", func() *DSSEEnvelope {
			e := *good
			e.Signatures = []DSSESignature{{KeyID: "ABCD", Sig: "!!"}}
			return &e
		}, &key.PublicKey, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.env().Verify(tt.pub, tt.keyID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got.Subject, st.Subject) {
				t.Fatalf("statement = %+v", got)
			}
		})
	}
}

func TestParseDSSEEnvelope(t *testing.T) {
	env := signPayload(t, testRSAKey(t), []byte("{}"), "K")
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ParseDSSEEnvelope(raw); !ok || got.Payload != env.Payload {
		t.Fatalf("envelope not recognised: %v %+v", ok, got)
	}
	for _, in := range []string{`{"_type":"x","subject":[]}`, `nope`, ``} {
		if _, ok := ParseDSSEEnvelope([]byte(in)); ok {
			t.Errorf("%q parsed as envelope", in)
		}
	}
	bad := &DSSEEnvelope{PayloadType: "text/plain", Payload: env.Payload}
	if _, err := bad.Statement(); err == nil {
		t.Error("wrong payload type accepted")
	}
}

func TestSetSubjectDigests(t *testing.T) {
	mk := func(pairs ...string) *InTotoStatement {
		s := &InTotoStatement{}
		for i := 0; i < len(pairs); i += 2 {
			s.Subject = append(s.Subject, SubjectArtifact{Name: pairs[i], Digest: map[string]string{"sha256": pairs[i+1]}})
		}
		return s
	}
	tests := []struct {
		name        string
		in          *InTotoStatement
		sums        map[string]string
		wantChanged bool
		want        *InTotoStatement
	}{
		{"same digest", mk("b.rpm", "1"), map[string]string{"b.rpm": "1"}, false, mk("b.rpm", "1")},
		{"replaced", mk("b.rpm", "1"), map[string]string{"b.rpm": "2"}, true, mk("b.rpm", "2")},
		{"added and sorted", mk("b.rpm", "1"), map[string]string{"a.rpm": "3"}, true, mk("a.rpm", "3", "b.rpm", "1")},
		{"untouched subject kept", mk("log", "9"), map[string]string{"a.rpm": "3"}, true, mk("a.rpm", "3", "log", "9")},
		{"nothing to do", mk("b.rpm", "1"), nil, false, mk("b.rpm", "1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.SetSubjectDigests(tt.sums); got != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", got, tt.wantChanged)
			}
			if !reflect.DeepEqual(tt.in.Subject, tt.want.Subject) {
				t.Fatalf("subjects = %+v, want %+v", tt.in.Subject, tt.want.Subject)
			}
		})
	}
}

func TestDSSEDigestIsSHA256OfPAE(t *testing.T) {
	want := sha256.Sum256(DSSEPAE(DSSEPayloadType, []byte("p")))
	if got := DSSEDigest(DSSEPayloadType, []byte("p")); !reflect.DeepEqual(got, want[:]) {
		t.Fatal("digest differs")
	}
}
