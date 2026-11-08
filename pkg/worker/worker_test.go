package worker_test

import (
	"context"
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

	// Wait for worker to become BUSY before sending abort signal
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		if daemon.Status() == "BUSY" {
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
