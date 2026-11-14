package signer_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestMemorySignDriverDetachedAndVerification(t *testing.T) {
	ctx := context.Background()
	driver := signer.NewMemorySignDriver()

	opts := signer.SignOptions{
		Project: "openSUSE:Factory",
	}

	pubKey, err := driver.PublicKey(ctx, opts)
	if err != nil {
		t.Fatalf("failed getting public key: %v", err)
	}
	if len(pubKey) == 0 {
		t.Fatalf("expected non-empty public key")
	}

	payload := []byte("hello, openSUSE build service signed content")
	sig, err := driver.SignDetached(ctx, payload, opts)
	if err != nil {
		t.Fatalf("failed signing detached: %v", err)
	}

	// Verify with valid payload
	if err := driver.VerifyDetached(pubKey, payload, sig); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}

	// Verify tampering fails
	tamperedPayload := []byte("tampered content")
	if err := driver.VerifyDetached(pubKey, tamperedPayload, sig); err == nil {
		t.Fatalf("expected verification to fail for tampered content")
	}
}

func TestRemoteSignerDriver(t *testing.T) {
	ctx := context.Background()
	mk, _ := signd.NewMasterKey("remote-driver-passphrase")
	defer mk.Destroy()

	engine := signd.NewSignEngine(mk, nil)
	server := signd.NewServer(engine)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()

	go func() {
		_ = server.Serve(ln)
	}()
	defer server.Close()

	// Provision project in storage without key -> RemoteSignerDriver must auto-provision via signd!
	memStore := storage.NewMemoryStore()
	_ = memStore.CreateProject(ctx, &manifest.ProjectManifest{
		Project: manifest.ProjectInfo{Name: "openSUSE:Factory"},
	})

	client := signd.NewClient(addr)
	driver := signer.NewRemoteSignerDriver(client, memStore)
	opts := signer.SignOptions{Project: "openSUSE:Factory"}

	pubKey, err := driver.PublicKey(ctx, opts)
	if err != nil {
		t.Fatalf("failed fetching public key (auto-provision): %v", err)
	}
	if len(pubKey) == 0 {
		t.Fatalf("expected non-empty public key")
	}

	savedKey, err := memStore.GetProjectKey(ctx, "openSUSE:Factory", "default", "gpg")
	if err != nil || savedKey == nil {
		t.Fatalf("expected key to be automatically saved to storage: %v", err)
	}

	payload := []byte("data to sign via remote signd daemon")
	sig, err := driver.SignDetached(ctx, payload, opts)
	if err != nil {
		t.Fatalf("failed signing detached remotely: %v", err)
	}

	if err := driver.VerifyDetached(pubKey, payload, sig); err != nil {
		t.Fatalf("remote signature verification failed: %v", err)
	}

	if err := driver.VerifyDetached(pubKey, []byte("tampered data"), sig); err == nil {
		t.Fatalf("expected verification to fail on tampered data")
	}

	clearsign, err := driver.SignClearsign(ctx, payload, opts)
	if err != nil || len(clearsign) == 0 {
		t.Fatalf("clearsign failed: %v", err)
	}

	tempRPM := filepath.Join(t.TempDir(), "test.rpm")
	_ = os.WriteFile(tempRPM, []byte("rpmpayload"), 0600)
	if err := driver.SignRPM(ctx, tempRPM, opts); err != nil {
		t.Fatalf("SignRPM failed: %v", err)
	}
}

func TestSignerServiceArtifactsAndMetadata(t *testing.T) {
	ctx := context.Background()
	driver := signer.NewMemorySignDriver()
	service := signer.NewSignerService(driver)

	tempDir := t.TempDir()
	opts := signer.SignOptions{
		Project: "devel:languages:go",
		Package: "bsgo",
	}

	// 1. Create simulated RPM and SLSA provenance files
	rpmFile := filepath.Join(tempDir, "bsgo-1.0.0-1.x86_64.rpm")
	_ = os.WriteFile(rpmFile, []byte("fake-rpm-content"), 0600)

	provFile := filepath.Join(tempDir, "_slsa_provenance.json")
	_ = os.WriteFile(provFile, []byte(`{"_type":"https://in-toto.io/Statement/v1"}`), 0600)

	signedFiles, err := service.SignPackageArtifacts(ctx, tempDir, opts)
	if err != nil {
		t.Fatalf("failed signing package artifacts: %v", err)
	}

	if len(signedFiles) != 2 {
		t.Fatalf("expected 2 signed files (rpm and prov.asc), got %d", len(signedFiles))
	}

	provSigFile := filepath.Join(tempDir, "_slsa_provenance.json.asc")
	if _, err := os.Stat(provSigFile); err != nil {
		t.Fatalf("expected _slsa_provenance.json.asc to exist: %v", err)
	}

	// 2. Test repository metadata signing
	repodataDir := filepath.Join(tempDir, "repodata")
	_ = os.MkdirAll(repodataDir, 0750)
	repomdFile := filepath.Join(repodataDir, "repomd.xml")
	_ = os.WriteFile(repomdFile, []byte("<repomd></repomd>"), 0600)

	sigPath, keyPath, err := service.SignRepositoryMetadata(ctx, repodataDir, opts)
	if err != nil {
		t.Fatalf("failed signing repomd.xml: %v", err)
	}

	if _, err := os.Stat(sigPath); err != nil {
		t.Fatalf("expected repomd.xml.asc: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("expected repomd.xml.key: %v", err)
	}
}

type mockPublisherSigner struct {
	driver signer.SignerDriver
}

func (m *mockPublisherSigner) SignRepositoryMetadata(ctx context.Context, repodataDir string, opts publisher.SignOptions) (string, string, error) {
	svc := signer.NewSignerService(m.driver)
	return svc.SignRepositoryMetadata(ctx, repodataDir, signer.SignOptions{
		Project: opts.Project,
	})
}

func TestPublisherWithSignerIntegration(t *testing.T) {
	ctx := context.Background()
	driver := signer.NewMemorySignDriver()

	tempDir := t.TempDir()
	pub := publisher.NewPublisher(&mockCrExecutor{}, &mockS3Client{}, publisher.NewLocalLockManager(), tempDir)
	pub.SetMetadataSigner(&mockPublisherSigner{driver: driver})

	// Create test repo layout
	repoDir := filepath.Join(tempDir, "openSUSE:Factory", "standard", "x86_64")
	repodataDir := filepath.Join(repoDir, "repodata")
	_ = os.MkdirAll(repodataDir, 0750)
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte("<repomd/>"), 0600)

	req := publisher.PublishRequest{
		RequestID:    "pub-sign-test",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
		NewRPMPaths:  []string{},
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error processing publish with signer: %v", err)
	}

	foundAsc := false
	foundKey := false
	for _, key := range res.UploadedXMLKeys {
		if filepath.Base(key) == "repomd.xml.asc" {
			foundAsc = true
		}
		if filepath.Base(key) == "repomd.xml.key" {
			foundKey = true
		}
	}

	if !foundAsc || !foundKey {
		t.Errorf("expected repomd.xml.asc and repomd.xml.key in uploaded keys, got %v", res.UploadedXMLKeys)
	}
}

type mockCrExecutor struct{}

func (m *mockCrExecutor) Run(_ context.Context, _ publisher.CreateRepoOptions) error {
	return nil
}

type mockS3Client struct {
	uploaded []string
}

func (m *mockS3Client) DownloadMetadata(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockS3Client) UploadFile(_ context.Context, _, s3Key string) error {
	m.uploaded = append(m.uploaded, s3Key)
	return nil
}

func (m *mockS3Client) DownloadFile(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockS3Client) ListObjects(_ context.Context, _ string) ([]publisher.S3ObjectInfo, error) {
	return nil, nil
}

func (m *mockS3Client) DeleteObject(_ context.Context, _ string) error {
	return nil
}
