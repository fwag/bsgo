package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func runEmbeddedNATSForKVM(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{
		Port: -1,
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("failed to create embedded nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatalf("embedded nats server timed out starting")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s, s.ClientURL()
}

func TestKVMDiskAndSourceIsolation(t *testing.T) {
	workDir := t.TempDir()
	diskDir := filepath.Join(t.TempDir(), "dedicated-disks")

	sandbox := &ProcessSandbox{
		WorkDir: workDir,
		DiskDir: diskDir,
	}

	job := &v1.BuildJob{
		JobID:   "job-isolation-test-01",
		Project: "openSUSE:Factory",
		Package: "hello",
		Resources: v1.ResourceRequirements{
			MemoryMB: 2048,
			DiskGB:   15,
		},
	}
	jobDir := filepath.Join(workDir, "jobs", job.JobID)
	if err := os.MkdirAll(jobDir, 0750); err != nil {
		t.Fatalf("failed to create jobDir: %v", err)
	}

	// 1. Verify sources are placed strictly in jobDir/sources, not jobDir root
	specFile, _, err := sandbox.setupKVMSources(context.Background(), job, jobDir, io.Discard)
	if err != nil {
		t.Fatalf("setupKVMSources failed: %v", err)
	}
	expectedSourcesDir := filepath.Join(jobDir, "sources")
	if !strings.HasPrefix(specFile, expectedSourcesDir) {
		t.Errorf("expected specFile in %s, got %s", expectedSourcesDir, specFile)
	}
	if !checkFileExists(specFile) {
		t.Errorf("specFile %s does not exist", specFile)
	}

	// 2. Verify VM disk is placed strictly in dedicated diskDir, not jobDir
	vmDisk, vmSwap, err := sandbox.setupKVMDisks(job)
	if err != nil {
		t.Fatalf("setupKVMDisks failed: %v", err)
	}
	if !strings.HasPrefix(vmDisk, diskDir) {
		t.Errorf("expected vmDisk in %s, got %s", diskDir, vmDisk)
	}
	if !strings.HasPrefix(vmSwap, diskDir) {
		t.Errorf("expected vmSwap in %s, got %s", diskDir, vmSwap)
	}
	if strings.HasPrefix(vmDisk, jobDir) {
		t.Errorf("vmDisk must NOT be inside jobDir to avoid self-copy recursion")
	}

	// 3. Verify args include explicit --vm-disk and --vm-swap pointing to dedicated disks
	buildRoot := filepath.Join(jobDir, "build-root")
	args := sandbox.buildKVMArgs(context.Background(), job, buildRoot, vmDisk, vmSwap, filepath.Join(workDir, "cache"), specFile, "", io.Discard)

	hasDiskArg := false
	hasSwapArg := false
	hasUserArg := false
	hasVMArgsArg := false
	for i, a := range args {
		if a == "--vm-disk" && i+1 < len(args) && args[i+1] == vmDisk {
			hasDiskArg = true
		}
		if a == "--vm-swap" && i+1 < len(args) && args[i+1] == vmSwap {
			hasSwapArg = true
		}
		if a == "--vm-user" && i+1 < len(args) && args[i+1] == "qemu" {
			hasUserArg = true
		}
		if a == "--vm-args" && i+1 < len(args) && args[i+1] == "-sandbox on -net none" {
			hasVMArgsArg = true
		}
	}
	if !hasDiskArg {
		t.Errorf("expected --vm-disk %s in args: %v", vmDisk, args)
	}
	if !hasSwapArg {
		t.Errorf("expected --vm-swap %s in args: %v", vmSwap, args)
	}
	if !hasUserArg {
		t.Errorf("expected --vm-user qemu in args: %v", args)
	}
	if !hasVMArgsArg {
		t.Errorf("expected --vm-args '-sandbox on -net none' in args: %v", args)
	}

	// 4. Verify cleanup removes disks and size files
	_ = os.WriteFile(vmDisk, []byte("dummy-disk"), 0600)
	_ = os.WriteFile(vmSwap, []byte("dummy-swap"), 0600)
	_ = os.WriteFile(vmDisk+".size", []byte("15360"), 0600)
	_ = os.WriteFile(vmSwap+".size", []byte("1024"), 0600)
	_ = os.MkdirAll(buildRoot, 0750)

	cleanupKVMRun(jobDir, buildRoot, vmDisk, vmSwap)

	if checkFileExists(vmDisk) || checkFileExists(vmSwap) || checkFileExists(vmDisk+".size") || checkFileExists(vmSwap+".size") {
		t.Errorf("cleanupKVMRun failed to remove VM disk files")
	}
	if checkFileExists(buildRoot) {
		t.Errorf("cleanupKVMRun failed to remove buildRoot")
	}
}

func TestCleanupOrphans(t *testing.T) {
	workDir := t.TempDir()
	diskDir := filepath.Join(workDir, "disks")
	_ = os.MkdirAll(diskDir, 0750)

	orphanImg := filepath.Join(diskDir, "stale-job.img")
	orphanSwap := filepath.Join(diskDir, "stale-job.swap")
	orphanSize := filepath.Join(diskDir, "stale-job.img.size")
	keeperFile := filepath.Join(diskDir, "keep-me.txt")

	_ = os.WriteFile(orphanImg, []byte("img"), 0600)
	_ = os.WriteFile(orphanSwap, []byte("swap"), 0600)
	_ = os.WriteFile(orphanSize, []byte("size"), 0600)
	_ = os.WriteFile(keeperFile, []byte("data"), 0600)

	legacyJobDir := filepath.Join(workDir, "jobs", "legacy-job-01")
	_ = os.MkdirAll(legacyJobDir, 0750)
	legacyImg := filepath.Join(legacyJobDir, "build-root.img")
	legacySwap := filepath.Join(legacyJobDir, "build-root.swap")
	_ = os.WriteFile(legacyImg, []byte("legacy-img"), 0600)
	_ = os.WriteFile(legacySwap, []byte("legacy-swap"), 0600)

	sandbox := &ProcessSandbox{WorkDir: workDir, DiskDir: diskDir}
	if err := sandbox.CleanupOrphans(context.Background()); err != nil {
		t.Fatalf("CleanupOrphans returned error: %v", err)
	}

	if checkFileExists(orphanImg) || checkFileExists(orphanSwap) || checkFileExists(orphanSize) {
		t.Errorf("CleanupOrphans failed to delete orphaned disk files in diskDir")
	}
	if !checkFileExists(keeperFile) {
		t.Errorf("CleanupOrphans accidentally deleted non-disk file")
	}
	if checkFileExists(legacyImg) || checkFileExists(legacySwap) {
		t.Errorf("CleanupOrphans failed to delete legacy build-root.img/swap in jobsDir")
	}
}

func TestWorkerDaemonGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, natsURL := runEmbeddedNATSForKVM(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	cfg := WorkerConfig{
		WorkerID:     "worker-graceful-01",
		Architecture: "x86_64",
	}

	jobStarted := make(chan struct{})
	jobDone := make(chan struct{})

	mockSandbox := &blockingSandbox{
		onExecute: func(execCtx context.Context) {
			close(jobStarted)
			<-execCtx.Done()
			close(jobDone)
		},
	}

	daemon := NewWorkerDaemon(cfg, nc, mockSandbox)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- daemon.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	job := &v1.BuildJob{
		JobID:        "job-graceful-test",
		Project:      "openSUSE:Factory",
		Package:      "slow-build",
		Architecture: "x86_64",
	}
	jobData, _ := json.Marshal(job)
	_ = nc.Publish("bsgo.workers.worker-graceful-01.job", jobData)

	select {
	case <-jobStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("job did not start within timeout")
	}

	daemon.Stop()

	select {
	case <-jobDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("job was not cleanly aborted by daemon.Stop")
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Errorf("daemon.Start returned error on stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("daemon.Start did not return cleanly after daemon.Stop")
	}
}

type blockingSandbox struct {
	onExecute func(ctx context.Context)
}

func (b *blockingSandbox) Execute(ctx context.Context, _ *v1.BuildJob, _ io.Writer) (*ExecutionResult, error) {
	if b.onExecute != nil {
		b.onExecute(ctx)
	}
	return &ExecutionResult{ExitCode: 0}, nil
}

func (b *blockingSandbox) Cleanup(_ context.Context) error {
	return nil
}

func TestKVMStateDirSeparation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, natsURL := runEmbeddedNATSForKVM(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	stateDir := t.TempDir()
	workDir := t.TempDir()

	cfg := WorkerConfig{
		WorkerID:     "worker-state-test",
		Architecture: "x86_64",
		StateDir:     stateDir,
		WorkDir:      workDir,
	}

	sandbox := &blockingSandbox{}
	daemon := NewWorkerDaemon(cfg, nc, sandbox)
	go func() { _ = daemon.Start(ctx) }()

	// Wait briefly for heartbeat to emit status.json
	time.Sleep(100 * time.Millisecond)

	statusFile := filepath.Join(stateDir, "status.json")
	if !checkFileExists(statusFile) {
		t.Errorf("expected status.json in stateDir %s", stateDir)
	}

	// Verify status.json is NOT in workDir
	if checkFileExists(filepath.Join(workDir, "status.json")) {
		t.Errorf("status.json must not be written to workDir")
	}

	daemon.Stop()
}

func TestKVMWorkerConfig(t *testing.T) {
	cfgFile, err := LoadConfigFile("../../config/worker-kvm.yaml")
	if err != nil {
		t.Fatalf("failed loading worker-kvm.yaml: %v", err)
	}

	if cfgFile.Worker.WorkDir != "/var/tmp/bsgo-worker-kvm" {
		t.Errorf("expected work_dir /var/tmp/bsgo-worker-kvm, got %s", cfgFile.Worker.WorkDir)
	}
	if cfgFile.Worker.StateDir != "/var/tmp/bsgo-worker-kvm/state" {
		t.Errorf("expected state_dir /var/tmp/bsgo-worker-kvm/state, got %s", cfgFile.Worker.StateDir)
	}
	if cfgFile.Sandbox.KVM.User != "qemu" {
		t.Errorf("expected KVM user 'qemu', got %s", cfgFile.Sandbox.KVM.User)
	}
	if cfgFile.Sandbox.KVM.VMArgs != "-sandbox on -net none" {
		t.Errorf("expected KVM vm_args '-sandbox on -net none', got %s", cfgFile.Sandbox.KVM.VMArgs)
	}
	if cfgFile.Sandbox.KVM.AIO != "io_uring" {
		t.Errorf("expected KVM aio 'io_uring', got %s", cfgFile.Sandbox.KVM.AIO)
	}
	if cfgFile.Sandbox.KVM.CacheMode != "unsafe" {
		t.Errorf("expected KVM cache_mode 'unsafe', got %s", cfgFile.Sandbox.KVM.CacheMode)
	}
}

func TestDeterministicDiskWiping(t *testing.T) {
	tmpDir := t.TempDir()
	diskFile := filepath.Join(tmpDir, "test-build-root.img")

	// Write sensitive non-zero bytes to the test file
	content := []byte("VERY_SECRET_KEY_AND_BUILD_ARTIFACT_DATA_THAT_MUST_BE_WIPED")
	if err := os.WriteFile(diskFile, content, 0600); err != nil {
		t.Fatalf("failed creating test disk file: %v", err)
	}

	if !checkFileExists(diskFile) {
		t.Fatalf("diskFile was not created")
	}

	wipeAndRemoveFile(diskFile)

	if checkFileExists(diskFile) {
		t.Errorf("diskFile was not wiped and unlinked")
	}
}

func TestVerifyChrootRPMsValidation(t *testing.T) {
	tmpDir := t.TempDir()
	rpmFile := filepath.Join(tmpDir, "curl-8.1.0-1.x86_64.rpm")
	content := []byte("rpm-package-payload-test-content")
	if err := os.WriteFile(rpmFile, content, 0644); err != nil {
		t.Fatalf("failed creating rpm file: %v", err)
	}

	sum := sha256.Sum256(content)
	correctHash := hex.EncodeToString(sum[:])

	// 1. Matching authentic hash -> success
	goodList := []v1.ChrootRPM{
		{Filename: "curl-8.1.0-1.x86_64.rpm", SHA256: correctHash},
	}
	if err := VerifyChrootRPMs(tmpDir, goodList); err != nil {
		t.Errorf("expected verification success for authentic hash, got: %v", err)
	}

	// 2. Legacy placeholder digest is no longer special: it is a mismatch
	placeholderList := []v1.ChrootRPM{
		{Filename: "curl-8.1.0-1.x86_64.rpm", SHA256: "mock-sha256-digest"},
	}
	if err := VerifyChrootRPMs(tmpDir, placeholderList); err == nil {
		t.Errorf("expected placeholder digest to be rejected as a mismatch")
	}

	// 3. Tampered / mismatched hash -> error
	badList := []v1.ChrootRPM{
		{Filename: "curl-8.1.0-1.x86_64.rpm", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
	}
	if err := VerifyChrootRPMs(tmpDir, badList); err == nil {
		t.Errorf("expected verification to fail for mismatched checksum")
	}
}
