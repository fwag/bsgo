package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/worker"
)

func runEmbeddedNATS(t *testing.T) (*server.Server, string) {
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

func TestWorkerLifecycleAndLiveLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, natsURL := runEmbeddedNATS(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	// 1. Listen for heartbeats
	heartbeatChan := make(chan v1.WorkerHeartbeat, 5)
	_, _ = nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if err := json.Unmarshal(msg.Data, &hb); err == nil {
			heartbeatChan <- hb
		}
	})

	// 2. Start Worker Daemon
	cfg := worker.WorkerConfig{
		WorkerID:     "worker-test-01",
		Architecture: "x86_64",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:    4,
			TotalMemoryMB: 8192,
			SandboxType:   "kvm",
		},
	}
	mockSandbox := &worker.MockSandbox{
		SimulateDuration: 50 * time.Millisecond,
		ProducedFiles:    []string{"hello-world-1.0.0-1.x86_64.rpm"},
	}
	daemon := worker.NewWorkerDaemon(cfg, nc, mockSandbox)
	go func() { _ = daemon.Start(ctx) }()
	defer daemon.Stop()

	// Verify initial heartbeat
	select {
	case hb := <-heartbeatChan:
		if hb.WorkerID != "worker-test-01" {
			t.Errorf("expected worker-test-01, got %s", hb.WorkerID)
		}
		if hb.Status != "IDLE" {
			t.Errorf("expected IDLE status, got %s", hb.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for worker heartbeat")
	}

	// 3. Listen for live logs
	jobID := "job-live-log-test"
	logChan := make(chan string, 10)
	_, _ = nc.Subscribe("bsgo.logs."+jobID, func(msg *nats.Msg) {
		logChan <- string(msg.Data)
	})

	// 4. Listen for completion status
	statusChan := make(chan map[string]any, 5)
	_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st map[string]any
		if err := json.Unmarshal(msg.Data, &st); err == nil {
			statusChan <- st
		}
	})

	// 5. Dispatch BuildJob to worker private inbox
	job := &v1.BuildJob{
		JobID:        jobID,
		Project:      "openSUSE:Factory",
		Package:      "hello-world",
		Architecture: "x86_64",
		Destination: v1.ArtifactDestination{
			S3Prefix: "artifacts/openSUSE:Factory/hello-world",
		},
	}
	jobData, _ := json.Marshal(job)
	_ = nc.Publish("bsgo.workers.worker-test-01.job", jobData)

	// Verify live log received
	select {
	case logLine := <-logChan:
		if !strings.Contains(logLine, "bsgo-worker: preparing sandbox") {
			t.Errorf("unexpected log content: %s", logLine)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for live log stream")
	}

	// The worker announces the start (building) before the outcome.
	if st := <-statusChanTimeout(t, statusChan); st["phase"] != "BUILDING" {
		t.Fatalf("expected the first phase BUILDING, got %v", st["phase"])
	}

	// Verify job completion status
	select {
	case st := <-statusChan:
		if st["job_id"] != jobID {
			t.Errorf("expected job_id %s, got %v", jobID, st["job_id"])
		}
		if st["phase"] != "SUCCEEDED" {
			t.Errorf("expected phase SUCCEEDED, got %v", st["phase"])
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for job status update")
	}
}

// statusChanTimeout returns a channel with the next status of ch, failing the
// test when none arrives in time.
func statusChanTimeout(t *testing.T, ch <-chan map[string]any) <-chan map[string]any {
	t.Helper()
	out := make(chan map[string]any, 1)
	select {
	case st := <-ch:
		out <- st
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the start status")
	}
	return out
}

func TestWorkerAbortSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, natsURL := runEmbeddedNATS(t)
	nc, _ := nats.Connect(natsURL)
	defer nc.Close()

	cfg := worker.WorkerConfig{
		WorkerID:     "worker-abort-01",
		Architecture: "x86_64",
	}
	mockSandbox := &worker.MockSandbox{
		SimulateDuration: 500 * time.Millisecond,
	}
	daemon := worker.NewWorkerDaemon(cfg, nc, mockSandbox)
	go func() { _ = daemon.Start(ctx) }()
	defer daemon.Stop()

	// Wait for worker subscription to be ready
	time.Sleep(100 * time.Millisecond)

	statusChan := make(chan map[string]any, 5)
	_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st map[string]any
		if err := json.Unmarshal(msg.Data, &st); err == nil {
			if st["job_id"] == "job-to-abort" {
				statusChan <- st
			}
		}
	})

	jobID := "job-to-abort"
	job := &v1.BuildJob{
		JobID:        jobID,
		Project:      "openSUSE:Factory",
		Package:      "heavy-compiler",
		Architecture: "x86_64",
	}
	jobData, _ := json.Marshal(job)
	_ = nc.Publish("bsgo.workers.worker-abort-01.job", jobData)

	// Wait for worker to register active job before sending abort signal
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		if daemon.Status() == "BUSY" || len(daemon.ActiveJobs()) > 0 {
			break
		}
	}

	ctrl := v1.JobControl{
		JobID:  jobID,
		Action: "ABORT",
		Reason: "SUPERSEDED_BY_NEW_COMMIT",
	}
	ctrlData, _ := json.Marshal(ctrl)
	_ = nc.Publish("bsgo.jobs."+jobID+".control", ctrlData)
	_ = nc.Flush()

	if st := <-statusChanTimeout(t, statusChan); st["phase"] != "BUILDING" {
		t.Fatalf("expected the first phase BUILDING, got %v", st["phase"])
	}
	select {
	case st := <-statusChan:
		if st["phase"] != "ABORTED" {
			t.Errorf("expected phase ABORTED, got %v", st["phase"])
		}
		if int(st["exit_code"].(float64)) != 137 {
			t.Errorf("expected exit code 137, got %v", st["exit_code"])
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for abort status")
	}
}

func TestProcessSandboxPathTraversalRejection(t *testing.T) {
	tempDir := t.TempDir()
	sandbox := &worker.ProcessSandbox{WorkDir: tempDir}

	maliciousJobs := []*v1.BuildJob{
		{JobID: "../../../../etc/passwd", Project: "test", Package: "pkg"},
		{JobID: "sub/dir", Project: "test", Package: "pkg"},
		{JobID: "sub\\dir", Project: "test", Package: "pkg"},
		{JobID: "..", Project: "test", Package: "pkg"},
		{JobID: "", Project: "test", Package: "pkg"},
	}

	for _, job := range maliciousJobs {
		_, err := sandbox.Execute(context.Background(), job, io.Discard)
		if err == nil {
			t.Errorf("expected error for malicious job id %q, but got nil", job.JobID)
		}
	}
}

func TestProcessSandboxSafeCleanup(t *testing.T) {
	tempDir := t.TempDir()
	sandbox := &worker.ProcessSandbox{WorkDir: tempDir}

	// Create simulated job
	job := &v1.BuildJob{
		JobID:   "job-safe-cleanup-01",
		Project: "test",
		Package: "pkg",
	}
	res, err := sandbox.Execute(context.Background(), job, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error executing valid job: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}

	// Verify Cleanup removes workspace cleanly
	if err := sandbox.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
}

func TestProcessSandboxSLSAProvenanceGeneration(t *testing.T) {
	tempDir := t.TempDir()
	sandbox := &worker.ProcessSandbox{WorkDir: tempDir}

	job := &v1.BuildJob{
		JobID:        "job-provenance-test",
		Project:      "openSUSE:Factory",
		Package:      "sample-app",
		Target:       "standard",
		Architecture: "x86_64",
		Source: v1.SourceSpec{
			GitURL:    "https://git/sample.git",
			CommitSHA: "abcdef0123456789",
		},
		ChrootRPMs: []v1.ChrootRPM{
			{Filename: "base-system-1.0.rpm", SHA256: "hash-base"},
		},
	}

	res, err := sandbox.Execute(context.Background(), job, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error executing job: %v", err)
	}

	foundProv := false
	for _, art := range res.Artifacts {
		if art.Filename == "_slsa_provenance.json" {
			foundProv = true
			if art.SHA256 == "" {
				t.Errorf("expected non-empty sha256 for provenance file")
			}
			break
		}
	}
	if !foundProv {
		t.Fatalf("expected _slsa_provenance.json in generated artifacts")
	}

	provPath := filepath.Join(tempDir, "jobs", job.JobID, "packages", "_slsa_provenance.json")
	data, err := os.ReadFile(provPath)
	if err != nil {
		t.Fatalf("failed reading _slsa_provenance.json: %v", err)
	}

	var stmt v1.InTotoStatement
	if err := json.Unmarshal(data, &stmt); err != nil {
		t.Fatalf("invalid json in _slsa_provenance.json: %v", err)
	}
	if stmt.Type != v1.InTotoStatementType {
		t.Errorf("expected statement type %s, got %s", v1.InTotoStatementType, stmt.Type)
	}
	if stmt.PredicateType != v1.SLSAProvenanceType {
		t.Errorf("expected predicate type %s, got %s", v1.SLSAProvenanceType, stmt.PredicateType)
	}
	if stmt.Predicate.BuildDefinition.ExternalParameters["package"] != "sample-app" {
		t.Errorf("expected package sample-app in build definition external parameters")
	}
}

func TestProcessSandboxDynamicRepositories(t *testing.T) {
	tempDir := t.TempDir()
	sandbox := &worker.ProcessSandbox{WorkDir: tempDir}

	// Create local mock repository with repomd.xml
	localRepo := filepath.Join(tempDir, "repos", "upstream-dep", "standard", "x86_64", "repodata")
	if err := os.MkdirAll(localRepo, 0750); err != nil {
		t.Fatalf("failed to create localRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localRepo, "repomd.xml"), []byte("<repomd/>"), 0600); err != nil {
		t.Fatalf("failed to write repomd.xml: %v", err)
	}

	job := &v1.BuildJob{
		JobID:        "job-dyn-repo-test",
		Project:      "consumer-proj",
		Package:      "app",
		Target:       "standard",
		Architecture: "x86_64",
		Repositories: []v1.RepositoryDependency{
			{URL: "https://download.opensuse.org/distribution/leap"},
		},
	}

	var buf strings.Builder
	args := sandbox.ResolveJobRepositories(context.Background(), job, &buf)

	// Verify that:
	// 1. External URL from job was attached
	// 2. Local repository from repos/ was discovered and attached
	// 3. No hardcoded repos were forced
	hasExternal := false
	hasLocal := false
	for i, arg := range args {
		if arg == "--repo" && i+1 < len(args) {
			val := args[i+1]
			if val == "https://download.opensuse.org/distribution/leap" {
				hasExternal = true
			}
			if strings.Contains(val, "upstream-dep") {
				hasLocal = true
			}
			if strings.Contains(val, "openSUSE:Crypto") {
				t.Errorf("unexpected hardcoded openSUSE:Crypto found in args: %v", args)
			}
		}
	}

	if !hasExternal {
		t.Errorf("expected external repo in args, got %v", args)
	}
	if !hasLocal {
		t.Errorf("expected local upstream-dep repo in args, got %v", args)
	}
}

func TestProcessSandboxRepoSnapshot(t *testing.T) {
	tempDir := t.TempDir()
	sandbox := &worker.ProcessSandbox{WorkDir: tempDir}

	s3RepoDir := filepath.Join(tempDir, "cache", "s3_repos", "my-repo")
	_ = os.MkdirAll(filepath.Join(s3RepoDir, "repodata"), 0750)
	_ = os.WriteFile(filepath.Join(s3RepoDir, "pkg-1.0.rpm"), []byte("rpmcontent"), 0600)
	_ = os.WriteFile(filepath.Join(s3RepoDir, "repodata", "repomd.xml"), []byte("<repomd>v1</repomd>"), 0600)

	jobID := "job-snap-123"
	jobRepo := sandbox.SnapshotRepoForJob(s3RepoDir, jobID)
	if !strings.Contains(jobRepo, jobID) {
		t.Fatalf("expected jobID in snapshot path, got %s", jobRepo)
	}

	snapRepomd := filepath.Join(jobRepo, "repodata", "repomd.xml")
	data, err := os.ReadFile(snapRepomd)
	if err != nil || string(data) != "<repomd>v1</repomd>" {
		t.Fatalf("unexpected snapshot repomd content: %s, err: %v", string(data), err)
	}

	// Mutate original repomd.xml and ensure snapshot is immune
	_ = os.WriteFile(filepath.Join(s3RepoDir, "repodata", "repomd.xml"), []byte("<repomd>v2</repomd>"), 0600)
	dataAfter, _ := os.ReadFile(snapRepomd)
	if string(dataAfter) != "<repomd>v1</repomd>" {
		t.Fatalf("snapshot repomd was altered after mutation: %s", string(dataAfter))
	}
}

func TestRenderBuildConfig(t *testing.T) {
	cfg := &manifest.ProjectConfig{
		Preinstall:   []string{"bash", "coreutils", "rpm"},
		VMinstall:    []string{"kernel-default"},
		Order:        []string{"bash", "rpm"},
		BuildEngine:  "podman",
		BuildTypes:   []string{"docker"},
		RepoTypes:    []string{"none"},
		Prefer:       []string{"mariadb-server"},
		Ignore:       []string{"broken-pkg"},
		Substitute:   map[string][]string{"default-jdk": {"java-17-openjdk-devel"}},
		Conflict:     [][]string{{"pkg-a", "pkg-b"}},
		Optflags:     map[string]string{"x86_64": "-O2 -g"},
		BuildFlags:   []string{"allowrootforbuild"},
		PublishFlags: []string{"archsync"},
		Macros:       "%vendor openSUSE\n%_target_image 1",
	}

	rendered := worker.RenderBuildConfig(cfg)

	expectedDirectives := []string{
		"Preinstall: bash coreutils rpm",
		"VMinstall: kernel-default",
		"Order: bash rpm",
		"BuildEngine: podman",
		"Type: docker",
		"Repotype: none",
		"Prefer: mariadb-server",
		"Ignore: broken-pkg",
		"Substitute: default-jdk java-17-openjdk-devel",
		"Conflict: pkg-a pkg-b",
		"Optflags: x86_64 -O2 -g",
		"buildflags: allowrootforbuild",
		"publishflags: archsync",
		"Macros:\n%vendor openSUSE\n%_target_image 1\n:macros",
	}

	for _, d := range expectedDirectives {
		if !strings.Contains(rendered, d) {
			t.Errorf("expected rendered config to contain %q, but got:\n%s", d, rendered)
		}
	}

	// Verify sanitization when Macros input already carries outer delimiters
	cfgWithDelims := &manifest.ProjectConfig{
		Macros: "Macros:\n%sle_version 0\n:macros",
	}
	renderedWithDelims := worker.RenderBuildConfig(cfgWithDelims)
	expectedClean := "Macros:\n%sle_version 0\n:macros\n"
	if renderedWithDelims != expectedClean {
		t.Errorf("expected clean rendered macros %q, got %q", expectedClean, renderedWithDelims)
	}
}

func TestWorkerSandboxBuildEnginePodman(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	sandbox := &worker.ProcessSandbox{WorkDir: tmpDir}

	job := &v1.BuildJob{
		JobID:        "job-podman-01",
		Project:      "openSUSE:Containers",
		Package:      "hello",
		Target:       "containers",
		Architecture: "x86_64",
		BuildEngine:  "podman",
		BuildType:    "docker",
	}

	var buf strings.Builder
	res, err := sandbox.Execute(ctx, job, &buf)
	if err != nil {
		t.Fatalf("unexpected error executing podman build: %v", err)
	}

	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}

	// Verify that hello.tar was produced and recorded
	foundTar := false
	for _, art := range res.Artifacts {
		if art.Filename == "hello.tar" {
			foundTar = true
			break
		}
	}
	if !foundTar {
		t.Errorf("expected hello.tar in artifacts, got %+v", res.Artifacts)
	}
}

func TestWorkerSandboxBuildEngineHelm(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	sandbox := &worker.ProcessSandbox{WorkDir: tmpDir}

	job := &v1.BuildJob{
		JobID:        "job-helm-01",
		Project:      "cloud-charts",
		Package:      "my-chart",
		Target:       "helm-repo",
		Architecture: "x86_64",
		BuildEngine:  "helm",
		BuildType:    "helm",
	}

	var buf strings.Builder
	res, err := sandbox.Execute(ctx, job, &buf)
	if err != nil {
		t.Fatalf("unexpected error executing helm build: %v", err)
	}

	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}

	// Verify that my-chart-0.1.0.tgz was produced and recorded
	foundTgz := false
	for _, art := range res.Artifacts {
		if art.Filename == "my-chart-0.1.0.tgz" {
			foundTgz = true
			break
		}
	}
	if !foundTgz {
		t.Errorf("expected my-chart-0.1.0.tgz in artifacts, got %+v", res.Artifacts)
	}
}

type controllableSandbox struct {
	mu       sync.Mutex
	running  map[string]chan struct{}
	finished map[string]bool
}

func newControllableSandbox() *controllableSandbox {
	return &controllableSandbox{
		running:  make(map[string]chan struct{}),
		finished: make(map[string]bool),
	}
}

func (c *controllableSandbox) Execute(ctx context.Context, job *v1.BuildJob, _ io.Writer) (*worker.ExecutionResult, error) {
	gate := make(chan struct{})
	c.mu.Lock()
	c.running[job.JobID] = gate
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return &worker.ExecutionResult{ExitCode: 137}, ctx.Err()
	case <-gate:
		c.mu.Lock()
		c.finished[job.JobID] = true
		c.mu.Unlock()
		return &worker.ExecutionResult{ExitCode: 0}, nil
	}
}

func (c *controllableSandbox) Release(jobID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.running[jobID]; ok {
		close(ch)
	}
}

func (c *controllableSandbox) Cleanup(_ context.Context) error {
	return nil
}

func TestWorkerDynamicResourceBinPacking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, natsURL := runEmbeddedNATS(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to embedded NATS: %v", err)
	}
	defer nc.Close()

	cfg := worker.WorkerConfig{
		WorkerID:     "worker-binpack-01",
		Architecture: "x86_64",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:        8,
			TotalMemoryMB:     16384,
			AvailableVCPUs:    8,
			AvailableMemoryMB: 16384,
			AvailableDiskGB:   100,
			SandboxType:       "kvm",
		},
	}

	mockSandbox := newControllableSandbox()
	daemon := worker.NewWorkerDaemon(cfg, nc, mockSandbox)

	go func() {
		_ = daemon.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Send Job 1 (4GB, 2 vCPUs)
	job1 := &v1.BuildJob{
		JobID:        "job-1",
		Project:      "prjA",
		Package:      "pkg1",
		Architecture: "x86_64",
		Resources:    v1.ResourceRequirements{MemoryMB: 4096, VCPUs: 2, DiskGB: 10, SandboxType: "kvm"},
	}
	data1, _ := json.Marshal(job1)
	_ = nc.Publish("bsgo.workers.worker-binpack-01.job", data1)

	// Send Job 2 (8GB, 4 vCPUs)
	job2 := &v1.BuildJob{
		JobID:        "job-2",
		Project:      "prjA",
		Package:      "pkg2",
		Architecture: "x86_64",
		Resources:    v1.ResourceRequirements{MemoryMB: 8192, VCPUs: 4, DiskGB: 10, SandboxType: "kvm"},
	}
	data2, _ := json.Marshal(job2)
	_ = nc.Publish("bsgo.workers.worker-binpack-01.job", data2)

	// Wait for both jobs to be concurrently active
	var active []string
	for i := 0; i < 20; i++ {
		active = daemon.ActiveJobs()
		if len(active) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(active) != 2 {
		t.Fatalf("expected 2 concurrently running jobs, got %d (%v)", len(active), active)
	}

	// Release Job 1 -> finishes build and recovers resources
	mockSandbox.Release("job-1")

	for i := 0; i < 20; i++ {
		active = daemon.ActiveJobs()
		if len(active) == 1 && active[0] == "job-2" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(active) != 1 || active[0] != "job-2" {
		t.Fatalf("expected 1 active job (job-2) after job-1 completion, got %v", active)
	}

	// Release Job 2 -> finishes build, zero active jobs
	mockSandbox.Release("job-2")

	for i := 0; i < 20; i++ {
		active = daemon.ActiveJobs()
		if len(active) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(active) != 0 {
		t.Fatalf("expected 0 active jobs after all jobs finished, got %v", active)
	}

	if daemon.Status() != "IDLE" {
		t.Errorf("expected worker status IDLE after completion, got %s", daemon.Status())
	}

	daemon.Stop()
}
