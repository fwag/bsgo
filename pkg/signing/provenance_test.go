package signing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// engineDaemon runs the real signd engine in process as the signer's daemon.
type engineDaemon struct{ e *signd.SignEngine }

func (d engineDaemon) GenerateKey(_ context.Context, project, flavor string, days int) (*signd.KeyMaterial, error) {
	return d.e.GenerateKey(project, flavor, days)
}

func (d engineDaemon) ExtendKey(_ context.Context, enc string, days int) (*signd.KeyMaterial, error) {
	return d.e.ExtendKey(enc, days, time.Now())
}

func (d engineDaemon) SignatureTrailer(_ context.Context, enc string, p signd.SignParams) ([]byte, error) {
	return d.e.SignatureTrailer(enc, p, time.Now())
}

func (d engineDaemon) SignDigest(_ context.Context, enc string, p signd.SignParams, digest []byte, armored bool) ([]byte, error) {
	return d.e.SignDigest(enc, p, digest, armored, time.Now())
}

func (d engineDaemon) SignRaw(_ context.Context, enc, hashAlgo string, digest []byte) ([]byte, error) {
	return d.e.SignRaw(enc, hashAlgo, digest, time.Now())
}

func newSigner(t *testing.T, cfg signer.Config) *signer.Signer {
	t.Helper()
	mk, err := signd.NewMasterKey("provenance-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mk.Destroy)
	e := signd.NewSignEngine(mk, nil)
	e.SetKeyParams(signd.KeyParams{RSABits: 2048})
	store := storage.NewMemoryStore()
	err = store.CreateProject(context.Background(), &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "p", Title: "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return signer.New(engineDaemon{e}, store, cfg)
}

const provKey = "artifacts/p/std/x86_64/foo/" + ProvenanceFile

// provFixture is a stage with one signed RPM on disk and the worker's
// unsigned provenance in the object store.
type provFixture struct {
	stage *Stage
	objs  memObjects
	dir   string
	key   KeyRef
	rpms  []*localRPM
	arts  []indexer.Artifact
	pub   string
}

func newProvFixture(t *testing.T, sg *signer.Signer) *provFixture {
	t.Helper()
	ctx := context.Background()
	k, err := sg.ResolveKey(ctx, "p", signer.DefaultFlavor)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rpmPath := filepath.Join(dir, "foo.rpm")
	if err := os.WriteFile(rpmPath, []byte("signed-rpm-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The worker reported the digest of the unsigned file.
	statement, err := json.MarshalIndent(&v1.InTotoStatement{
		Type: v1.InTotoStatementType, PredicateType: v1.SLSAProvenanceType,
		Subject: []v1.SubjectArtifact{{Name: "foo.rpm", Digest: map[string]string{"sha256": hexSum([]byte("unsigned"))}}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	objs := memObjects{provKey: statement}
	return &provFixture{
		stage: &Stage{Keys: sg, Raw: sg, Objects: objs, TempRoot: t.TempDir()},
		objs:  objs, dir: dir,
		key:  KeyRef{Project: k.ProjectName, Flavor: k.KeyFlavor, Fingerprint: k.Fingerprint},
		rpms: []*localRPM{{art: indexer.Artifact{Filename: "foo.rpm"}, path: rpmPath}},
		arts: []indexer.Artifact{
			{Filename: "foo.rpm", S3Key: "artifacts/p/std/x86_64/foo/foo.rpm"},
			{Filename: ProvenanceFile, S3Key: provKey, SHA256: hexSum(statement)},
		},
		pub: k.PublicKeyArmored,
	}
}

func (f *provFixture) run(t *testing.T) []indexer.Artifact {
	t.Helper()
	arts, err := f.stage.signProvenance(context.Background(), f.arts, f.key, f.rpms)
	if err != nil {
		t.Fatal(err)
	}
	return arts
}

func TestSignProvenanceVerifiesWithProjectKey(t *testing.T) {
	f := newProvFixture(t, newSigner(t, signer.Config{}))
	arts := f.run(t)

	envelope := f.objs[provKey]
	st, err := VerifyProvenance(envelope, f.pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if st.Type != v1.InTotoStatementType || st.PredicateType != v1.SLSAProvenanceType {
		t.Errorf("statement types %q %q", st.Type, st.PredicateType)
	}
	want := hexSum([]byte("signed-rpm-bytes"))
	if len(st.Subject) != 1 || st.Subject[0].Name != "foo.rpm" || st.Subject[0].Digest["sha256"] != want {
		t.Errorf("subject digests were not moved to the signed RPM: %+v", st.Subject)
	}
	var env v1.DSSEEnvelope
	if err := json.Unmarshal(envelope, &env); err != nil {
		t.Fatal(err)
	}
	if env.PayloadType != "application/vnd.in-toto+json" || len(env.Signatures) != 1 || env.Signatures[0].KeyID != f.key.Fingerprint {
		t.Errorf("envelope layout %+v", env)
	}
	for _, a := range arts {
		if a.Filename == ProvenanceFile && (a.SHA256 != hexSum(envelope) || a.Size != int64(len(envelope))) {
			t.Errorf("artifact entry not updated: %+v", a)
		}
	}
}

func TestSignProvenanceIdempotent(t *testing.T) {
	f := newProvFixture(t, newSigner(t, signer.Config{}))
	f.run(t)
	first := bytes.Clone(f.objs[provKey])
	// A retry downloads the envelope of the previous attempt.
	f.run(t)
	f.run(t)
	if !bytes.Equal(first, f.objs[provKey]) {
		t.Fatal("a repeated run changed the envelope")
	}
}

func TestVerifyProvenanceRejects(t *testing.T) {
	sg := newSigner(t, signer.Config{})
	f := newProvFixture(t, sg)
	f.run(t)
	good := f.objs[provKey]

	other := newProvFixture(t, newSigner(t, signer.Config{}))
	var env v1.DSSEEnvelope
	if err := json.Unmarshal(good, &env); err != nil {
		t.Fatal(err)
	}
	tampered := env
	tampered.Payload = v1.NewDSSEEnvelope([]byte(`{"_type":"x"}`), nil, "").Payload
	tamperedJSON, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		pub  string
	}{
		{"other project key", good, other.pub},
		{"tampered payload", tamperedJSON, f.pub},
		{"unsigned statement", []byte(`{"_type":"https://in-toto.io/Statement/v1"}`), f.pub},
		{"not json", []byte("x"), f.pub},
		{"bad public key", good, "not a key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := VerifyProvenance(tt.data, tt.pub); err == nil {
				t.Fatal("expected a verification error")
			}
		})
	}
}

func TestSignProvenanceSkipsAndFailures(t *testing.T) {
	sg := newSigner(t, signer.Config{})
	tests := []struct {
		name    string
		mutate  func(f *provFixture)
		wantErr bool
		wantNew bool // the object was rewritten
	}{
		{"no provenance artifact", func(f *provFixture) { f.arts = f.arts[:1] }, false, false},
		{"no raw signer", func(f *provFixture) { f.stage.Raw = nil }, true, false},
		{"missing object", func(f *provFixture) { delete(f.objs, provKey) }, true, false},
		{"garbage provenance", func(f *provFixture) { f.objs[provKey] = []byte("nope") }, true, false},
		{"wrong statement type", func(f *provFixture) { f.objs[provKey] = []byte(`{"_type":"x"}`) }, true, false},
		{"signs", func(*provFixture) {}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProvFixture(t, sg)
			orig := bytes.Clone(f.objs[provKey])
			tt.mutate(f)
			before := bytes.Clone(f.objs[provKey])
			_, err := f.stage.signProvenance(context.Background(), f.arts, f.key, f.rpms)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if changed := !bytes.Equal(before, f.objs[provKey]); changed != tt.wantNew {
				t.Errorf("object rewritten = %v, want %v (orig %d bytes)", changed, tt.wantNew, len(orig))
			}
		})
	}
}

// A project without a key and without automatic key creation is not signed:
// the stage fails like the RPM stage and leaves the provenance untouched; the
// policy (signer.Enabled) keeps such jobs out of the stage in the first place.
func TestProvenanceNoKey(t *testing.T) {
	sg := newSigner(t, signer.Config{NoAutoCreate: true})
	ctx := context.Background()
	if on, err := sg.Enabled(ctx, "p", signer.DefaultFlavor); err != nil || on {
		t.Fatalf("Enabled = %v, %v; want false", on, err)
	}
	objs := memObjects{provKey: []byte(`{"_type":"https://in-toto.io/Statement/v1"}`)}
	before := bytes.Clone(objs[provKey])
	s := &Stage{Keys: sg, Raw: sg, Objects: objs, Tool: &fakeTool{}, TempRoot: t.TempDir()}
	_, err := s.Sign(ctx, Job{
		JobID: "j", Project: "p", Package: "foo", Target: "std", Architecture: "x86_64",
		Artifacts: []indexer.Artifact{{Filename: ProvenanceFile, S3Key: provKey}},
	})
	if !errors.Is(err, signer.ErrNoKey) {
		t.Fatalf("Sign error = %v, want ErrNoKey", err)
	}
	if !bytes.Equal(before, objs[provKey]) {
		t.Error("provenance of an unsigned project was modified")
	}
}
