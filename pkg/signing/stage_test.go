package signing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type memObjects map[string][]byte

func (m memObjects) DownloadFile(_ context.Context, key, local string) error {
	data, ok := m[key]
	if !ok {
		return errors.New("no such object " + key)
	}
	return os.WriteFile(local, data, 0o600)
}

func (m memObjects) UploadFile(_ context.Context, local, key string) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	m[key] = data
	return nil
}

type fakeKeys struct{ err error }

func (f fakeKeys) ResolveKey(_ context.Context, project, flavor string) (*storage.ProjectSigningKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &storage.ProjectSigningKey{
		ProjectName: project, KeyFlavor: flavor, Fingerprint: "FP", EncryptedPrivateKey: "enc", PublicKeyArmored: testPublicKey(),
	}, nil
}

var testPublicKey = sync.OnceValue(func() string {
	e, err := openpgp.NewEntity("t", "", "t@example.org", &packet.Config{RSABits: 1024})
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		panic(err)
	}
	if err := e.Serialize(w); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.String()
})

// fakeTool "signs" by appending a marker and verifies by looking for it.
type fakeTool struct {
	signErr   error
	signed    []string
	verifyErr error
}

const marker = "|SIGNED"

func (f *fakeTool) Sign(_ context.Context, path string, _ KeyRef) error {
	if f.signErr != nil {
		return f.signErr
	}
	f.signed = append(f.signed, path)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := fh.WriteString(marker); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

func (f *fakeTool) Verify(_ context.Context, _ string, paths []string) error {
	if f.verifyErr != nil {
		return f.verifyErr
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(string(data), marker) {
			return errors.New(p + " is not signed")
		}
	}
	return nil
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fakeStage(t *testing.T, tool *fakeTool, objs memObjects) *Stage {
	t.Helper()
	return &Stage{
		Keys: fakeKeys{}, Objects: objs, Tool: tool, TempRoot: t.TempDir(),
		Attempts: 3, Backoff: time.Millisecond, Sleep: func(context.Context, time.Duration) {},
	}
}

func job(objs memObjects) Job {
	objs["a/p/foo.rpm"] = []byte("rpm-bytes")
	return Job{
		JobID: "j", Project: "p", Package: "foo", Target: "std", Architecture: "x86_64",
		Artifacts: []indexer.Artifact{
			{Filename: "foo.rpm", S3Key: "a/p/foo.rpm", SHA256: hexSum([]byte("rpm-bytes"))},
			{Filename: "build.log", S3Key: "a/p/build.log", SHA256: "ignored"},
		},
	}
}

func TestProcessReplacesArtifacts(t *testing.T) {
	// The fake tool cannot extract bininfo from a fake RPM, so exercise the
	// steps up to the tool boundary through Sign's building blocks.
	objs := memObjects{}
	j := job(objs)
	s := fakeStage(t, &fakeTool{}, objs)
	dir := t.TempDir()
	rpms, err := s.fetch(context.Background(), dir, j)
	if err != nil || len(rpms) != 1 || rpms[0].signed {
		t.Fatalf("fetch: %v %+v", err, rpms)
	}
	if err := s.signAll(context.Background(), "pub", KeyRef{}, rpms); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rpms[0].path)
	if err != nil || !strings.HasSuffix(string(data), marker) {
		t.Fatalf("RPM not signed: %q %v", data, err)
	}
}

func TestFetchRecognisesSignedAndCorrupt(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantSigned bool
		verifyErr  error
		wantErr    bool
	}{
		{"unsigned download matches the worker", "rpm-bytes", false, nil, false},
		{"other bytes that verify were signed earlier", "rpm-bytes" + marker, true, nil, false},
		{"other bytes that do not verify are corrupt", "tampered", true, errors.New("bad"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := memObjects{}
			j := job(objs)
			objs["a/p/foo.rpm"] = []byte(tc.content)
			tool := &fakeTool{verifyErr: tc.verifyErr}
			s := fakeStage(t, tool, objs)
			rpms, err := s.fetch(context.Background(), t.TempDir(), j)
			if err != nil {
				t.Fatal(err)
			}
			if rpms[0].signed != tc.wantSigned {
				t.Fatalf("signed = %v, want %v", rpms[0].signed, tc.wantSigned)
			}
			err = s.signAll(context.Background(), "pub", KeyRef{}, rpms)
			if (err != nil) != tc.wantErr {
				t.Fatalf("signAll error %v, want error %v", err, tc.wantErr)
			}
			if tc.wantSigned && len(tool.signed) != 0 {
				t.Errorf("an already signed RPM was signed again: %v", tool.signed)
			}
		})
	}
}

func TestProcessRetriesThenFails(t *testing.T) {
	tests := []struct {
		name     string
		keys     Keys
		tool     *fakeTool
		wantErr  string
		wantWait int
	}{
		{"tool failure is retried then reported", fakeKeys{}, &fakeTool{signErr: errors.New("signd down")}, "signd down", 2},
		{"key failure is reported", fakeKeys{err: errors.New("no key")}, &fakeTool{}, "no key", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := memObjects{}
			s := fakeStage(t, tc.tool, objs)
			s.Keys = tc.keys
			var waits []time.Duration
			s.Sleep = func(_ context.Context, d time.Duration) { waits = append(waits, d) }
			got := s.Process(context.Background(), job(objs))
			if !strings.Contains(got.Error, tc.wantErr) {
				t.Fatalf("error %q does not mention %q", got.Error, tc.wantErr)
			}
			if len(waits) != tc.wantWait || waits[0] != time.Millisecond || waits[1] != 2*time.Millisecond {
				t.Errorf("backoff waits %v, want %d doubling waits", waits, tc.wantWait)
			}
		})
	}
}

func TestProcessStopsOnCancel(t *testing.T) {
	objs := memObjects{}
	s := fakeStage(t, &fakeTool{signErr: errors.New("x")}, objs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slept := 0
	s.Sleep = func(context.Context, time.Duration) { slept++ }
	if got := s.Process(ctx, job(objs)); got.Error == "" || slept != 0 {
		t.Fatalf("cancelled job: error %q, slept %d times", got.Error, slept)
	}
}

func TestSafeName(t *testing.T) {
	for name, ok := range map[string]bool{
		"foo-1.0-1.noarch.rpm": true, "": false, "..": false, ".": false, "../x.rpm": false, "a/b.rpm": false,
	} {
		if err := safeName(name); (err == nil) != ok {
			t.Errorf("safeName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
	objs := memObjects{}
	j := job(objs)
	j.Artifacts[0].Filename = "../evil.rpm"
	if _, err := fakeStage(t, &fakeTool{}, objs).fetch(context.Background(), t.TempDir(), j); err == nil {
		t.Error("a path traversing artifact name must be rejected")
	}
}

func TestMergeAndBininfoKey(t *testing.T) {
	old := []indexer.Artifact{{Filename: "a.rpm", S3Key: "d/a.rpm", SHA256: "1"}, {Filename: "log", S3Key: "d/log"}}
	got := mergeArtifacts(old, map[string]indexer.Artifact{
		"a.rpm": {Filename: "a.rpm", S3Key: "d/a.rpm", SHA256: "2"}, "bininfo.json": {Filename: "bininfo.json", S3Key: "d/bininfo.json"},
	})
	if len(got) != 3 || got[0].SHA256 != "2" || got[1].Filename != "log" || got[2].Filename != "bininfo.json" {
		t.Errorf("merge result %+v", got)
	}
	if k := bininfoKey(Job{Artifacts: old}); k != "d/bininfo.json" {
		t.Errorf("derived bininfo key %q, want d/bininfo.json", k)
	}
	withInfo := append(old, indexer.Artifact{Filename: "bininfo.json", S3Key: "x/bininfo.json"})
	if k := bininfoKey(Job{Artifacts: withInfo}); k != "x/bininfo.json" {
		t.Errorf("reported bininfo key %q, want x/bininfo.json", k)
	}
}

func TestDecode(t *testing.T) {
	good := `{"job_id":"j","project":"p","package":"x","target":"t","architecture":"a"}`
	for msg, ok := range map[string]bool{good: true, "{": false, `{"job_id":"j"}`: false} {
		if _, err := Decode([]byte(msg)); (err == nil) != ok {
			t.Errorf("Decode(%q) error %v, want ok=%v", msg, err, ok)
		}
	}
	if HasRPMs([]indexer.Artifact{{Filename: "x.iso"}}) || !HasRPMs([]indexer.Artifact{{Filename: "x.rpm"}}) {
		t.Error("HasRPMs misjudges artifact lists")
	}
}

func TestParseHelperArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    helperArgs
		wantErr bool
	}{
		{"rpm default command line",
			[]string{"gpg", "--no-verbose", "--no-armor", "--no-secmem-warning", "--digest-algo=sha256", "-u", "FP", "-sbo", "/t/sig", "--", "/t/plain"},
			helperArgs{output: "/t/sig", plaintext: "/t/plain", hash: "sha256"}, false},
		{"stdin plaintext", []string{"-sbo", "/t/sig", "--", "-"}, helperArgs{output: "/t/sig", plaintext: "-", hash: "sha256"}, false},
		{"no plaintext reads stdin", []string{"-sbo", "/t/sig"}, helperArgs{output: "/t/sig", hash: "sha256"}, false},
		{"separate digest algo", []string{"--digest-algo", "sha512", "-sbo", "/t/sig"}, helperArgs{output: "/t/sig", hash: "sha512"}, false},
		{"missing output", []string{"--", "/t/plain"}, helperArgs{}, true},
		{"unknown option", []string{"--armor", "-sbo", "/t/sig"}, helperArgs{}, true},
		{"output without value", []string{"-sbo"}, helperArgs{}, true},
		{"two plaintexts", []string{"-sbo", "/t/sig", "--", "a", "b"}, helperArgs{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHelperArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRunHelperRejectsBadEnvironment(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	args := []string{"-sbo", t.TempDir() + "/sig", "--", "-"}
	full := map[string]string{
		EnvSigndAddr: "127.0.0.1:1", EnvProject: "p", EnvEncKey: "enc", EnvCreated: "1700000000",
	}
	without := func(k string) map[string]string {
		m := map[string]string{}
		for kk, v := range full {
			if kk != k {
				m[kk] = v
			}
		}
		return m
	}
	for _, k := range []string{EnvSigndAddr, EnvProject, EnvEncKey, EnvCreated} {
		if err := RunHelper(context.Background(), args, env(without(k)), strings.NewReader("x")); err == nil {
			t.Errorf("missing %s must be an error", k)
		}
	}
	bad := without(EnvCreated)
	bad[EnvCreated] = "yesterday"
	if err := RunHelper(context.Background(), args, env(bad), strings.NewReader("x")); err == nil {
		t.Error("a non numeric creation time must be an error")
	}
}
