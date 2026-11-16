package signing_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/signing"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const tinySpec = `Name: bsgo-tiny
Version: 1.0
Release: 1
Summary: Signing test package
License: MIT
BuildArch: noarch

%description
Test package.

%install
mkdir -p %{buildroot}/usr/share/bsgo-tiny
echo hello > %{buildroot}/usr/share/bsgo-tiny/hello

%files
/usr/share/bsgo-tiny/hello
`

func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"rpmbuild", "rpmsign", "rpm", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func command(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// buildTinyRPM builds an unsigned noarch RPM and returns its path.
func buildTinyRPM(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	spec := filepath.Join(top, "tiny.spec")
	if err := os.WriteFile(spec, []byte(tinySpec), 0o600); err != nil {
		t.Fatal(err)
	}
	command(t, top, "rpmbuild", "-bb", "--define", "_topdir "+top, "--define", "_build_id_links none", spec)
	rpms, err := filepath.Glob(filepath.Join(top, "RPMS", "*", "*.rpm"))
	if err != nil || len(rpms) != 1 {
		t.Fatalf("expected one RPM, got %v (%v)", rpms, err)
	}
	return rpms[0]
}

func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bsgo-sign-helper")
	command(t, "", "go", "build", "-o", bin, "github.com/openSUSE/bsgo/cmd/bsgo-sign-helper")
	return bin
}

// startSignd runs an in-process bsgo-signd on a loopback port.
func startSignd(t *testing.T) (*signd.Client, string) {
	t.Helper()
	mk, err := signd.NewMasterKey("integration-test-master-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mk.Destroy)
	engine := signd.NewSignEngine(mk, nil)
	engine.SetKeyParams(signd.KeyParams{RSABits: 2048})
	srv := signd.NewServer(engine)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return signd.NewClient(ln.Addr().String()), ln.Addr().String()
}

// dirObjects is an object store on a local directory.
type dirObjects struct {
	mu   sync.Mutex
	root string
}

func (d *dirObjects) path(key string) string { return filepath.Join(d.root, filepath.FromSlash(key)) }

func (d *dirObjects) UploadFile(_ context.Context, local, key string) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(d.path(key)), 0o750); err != nil {
		return err
	}
	return os.WriteFile(d.path(key), data, 0o600)
}

func (d *dirObjects) DownloadFile(_ context.Context, key, local string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, err := os.ReadFile(d.path(key))
	if err != nil {
		return err
	}
	return os.WriteFile(local, data, 0o600)
}

func sha(t *testing.T, path string) (string, int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data))
}

type fixture struct {
	stage     *signing.Stage
	objects   *dirObjects
	store     *storage.MemoryStore
	job       signing.Job
	rpmPath   string
	published [][]byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	needTools(t)
	f := &fixture{objects: &dirObjects{root: t.TempDir()}, store: storage.NewMemoryStore()}
	for _, p := range []string{"home:bot", "home:other"} {
		err := f.store.CreateProject(context.Background(), &manifest.ProjectManifest{
			SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: p, Title: p},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	client, addr := startSignd(t)
	keys := signer.New(client, f.store, signer.Config{})
	f.stage = &signing.Stage{
		Keys: keys, Objects: f.objects, States: f.store, TempRoot: t.TempDir(),
		Tool: signing.RPMTool{
			Helper: buildHelper(t),
			Signd:  signing.SigndEndpoint{Addr: addr},
		},
		Publish:  func(_ context.Context, data []byte) error { f.published = append(f.published, data); return nil },
		Attempts: 1,
	}
	f.rpmPath = buildTinyRPM(t)
	name := filepath.Base(f.rpmPath)
	sum, size := sha(t, f.rpmPath)
	key := "artifacts/home:bot/std/x86_64/tiny/"
	if err := f.objects.UploadFile(context.Background(), f.rpmPath, key+name); err != nil {
		t.Fatal(err)
	}
	infos, err := bininfo.ExtractAll(context.Background(), []string{f.rpmPath})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bininfo.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	biLocal := filepath.Join(t.TempDir(), bininfo.FileName)
	if err := os.WriteFile(biLocal, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.objects.UploadFile(context.Background(), biLocal, key+bininfo.FileName); err != nil {
		t.Fatal(err)
	}
	biSum, biSize := sha(t, biLocal)
	f.job = signing.Job{
		JobID: "job-1", Project: "home:bot", Package: "tiny", Target: "std", Architecture: "x86_64",
		Artifacts: []indexer.Artifact{
			{Filename: name, S3Key: key + name, SHA256: sum, Size: size},
			{Filename: bininfo.FileName, S3Key: key + bininfo.FileName, SHA256: biSum, Size: biSize},
		},
	}
	return f
}

func TestStageSignsRPM(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rpmKey := f.job.Artifacts[0].S3Key
	unsigned := f.job.Artifacts[0].SHA256

	arts, err := f.stage.Sign(ctx, f.job)
	if err != nil {
		t.Fatal(err)
	}
	if arts[0].SHA256 == unsigned {
		t.Fatal("RPM sha256 did not change: not signed")
	}
	key, err := f.store.GetProjectKey(ctx, "home:bot", signer.DefaultFlavor, signer.KeyType)
	if err != nil {
		t.Fatal(err)
	}
	signed := t.TempDir() + "/signed.rpm"
	if err := f.objects.DownloadFile(ctx, rpmKey, signed); err != nil {
		t.Fatal(err)
	}
	if got, _ := sha(t, signed); got != arts[0].SHA256 {
		t.Fatalf("uploaded RPM sha256 %s, answer says %s", got, arts[0].SHA256)
	}

	tool := f.stage.Tool
	if err := tool.Verify(ctx, key.PublicKeyArmored, []string{signed}); err != nil {
		t.Fatalf("signed RPM must verify with the project key: %v", err)
	}
	if err := tool.Verify(ctx, key.PublicKeyArmored, []string{f.rpmPath}); err == nil {
		t.Fatal("unsigned RPM must not verify")
	}
	assertOtherKeyFails(t, f, signed)

	raw, err := os.ReadFile(f.objects.path(arts[1].S3Key))
	if err != nil {
		t.Fatal(err)
	}
	infos, err := bininfo.Unmarshal(raw)
	if err != nil || len(infos) != 1 || infos[0].SHA256 != arts[0].SHA256 {
		t.Fatalf("bininfo does not describe the signed RPM: %v %+v", err, infos)
	}
}

// assertOtherKeyFails checks that rpm -K with an unrelated key rejects the RPM.
func assertOtherKeyFails(t *testing.T, f *fixture, rpm string) {
	t.Helper()
	other, err := f.stage.Keys.ResolveKey(context.Background(), "home:other", signer.DefaultFlavor)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stage.Tool.Verify(context.Background(), other.PublicKeyArmored, []string{rpm}); err == nil {
		t.Fatal("RPM must not verify with another project's key")
	}
}

func TestStageIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.stage.Sign(ctx, f.job)
	if err != nil {
		t.Fatal(err)
	}
	// The same unsigned request again, with the signed objects in S3: the
	// RPM is recognised as signed and nothing breaks.
	second, err := f.stage.Sign(ctx, f.job)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].SHA256 != second[0].SHA256 || first[1].SHA256 != second[1].SHA256 {
		t.Fatalf("second run changed the result: %+v vs %+v", first, second)
	}
}

func TestStageHandleSkipsFinishedJobs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	msg, err := signing.Encode(f.job)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stage.Handle(ctx, msg); err != nil || len(f.published) != 0 {
		t.Fatalf("no state row: want skip, got %v / %d answers", err, len(f.published))
	}
	row := &storage.PackageBuildState{
		Project: "home:bot", Repo: "std", Arch: "x86_64", Package: "tiny", JobID: "job-1", State: storage.StateSigning,
	}
	if err := f.store.UpsertBuildState(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := f.stage.Handle(ctx, msg); err != nil || len(f.published) != 1 {
		t.Fatalf("signing state: want one answer, got %v / %d", err, len(f.published))
	}
	answer, err := signing.Decode(f.published[0])
	if err != nil || answer.Error != "" {
		t.Fatalf("answer %+v: %v", answer, err)
	}
	row.State = storage.StateSucceeded
	if err := f.store.UpsertBuildState(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := f.stage.Handle(ctx, msg); err != nil || len(f.published) != 1 {
		t.Fatalf("done job: want skip, got %v / %d answers", err, len(f.published))
	}
}

func TestStageRejectsCorruptDownload(t *testing.T) {
	f := newFixture(t)
	f.job.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if _, err := f.stage.Sign(context.Background(), f.job); err == nil {
		t.Fatal("an RPM that differs from the worker's sha256 and is unsigned must be rejected")
	}
}
