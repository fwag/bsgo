package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/openSUSE/bsgo/pkg/manifest"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func runEmbeddedNATS(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
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

func TestWatchShowsJobsCreatedPriorToInvocation(t *testing.T) {
	_, natsURL := runEmbeddedNATS(t)
	t.Setenv("NATS_URL", natsURL)

	store := storage.NewMemoryStore()
	ctx := context.Background()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "myproject"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Architectures: []string{"x86_64"}},
		},
	}
	_ = store.CreateProject(ctx, prj)
	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "mypackage"},
		Source:        manifest.PackageSource{Git: "https://example.com/repo.git", Commit: "abcdef123"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "myproject", pkg)

	// 1. Run package trigger command BEFORE watch is started
	cmd := NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"package", "trigger", "myproject/mypackage", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("failed to execute package trigger: %v", err)
	}

	var trigRes map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &trigRes); err != nil {
		t.Fatalf("failed to parse trigger output: %v\nOutput: %s", err, outBuf.String())
	}
	jobID, _ := trigRes["job_id"].(string)
	if _, err := uuid.Parse(jobID); err != nil {
		t.Fatalf("expected job_id to be a valid RFC 4122 UUID, got: %v (err: %v)", jobID, err)
	}

	// 2. Now start watch dashboard (after job has already been created)
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to jetstream: %v", err)
	}
	defer nc.Close()

	m := newLiveDashboardModel(natsURL, nc, js)
	if len(m.jobs) == 0 {
		t.Fatalf("expected job %s to be loaded and shown in watch dashboard, but got 0 jobs", jobID)
	}
	if m.jobs[0].JobID != jobID {
		t.Errorf("expected job ID %s, got %s", jobID, m.jobs[0].JobID)
	}
	if m.jobs[0].Package != "mypackage" {
		t.Errorf("expected package mypackage, got %s", m.jobs[0].Package)
	}
	if m.jobs[0].Project != "myproject" {
		t.Errorf("expected project myproject, got %s", m.jobs[0].Project)
	}
	if m.jobs[0].Target != "PENDING" {
		t.Errorf("expected phase PENDING, got %s", m.jobs[0].Target)
	}
}

func TestWatchShowsJobsWithUpdatedStatus(t *testing.T) {
	_, natsURL := runEmbeddedNATS(t)
	t.Setenv("NATS_URL", natsURL)

	store := storage.NewMemoryStore()
	ctx := context.Background()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "core"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Architectures: []string{"x86_64"}},
		},
	}
	_ = store.CreateProject(ctx, prj)
	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "libfoo"},
		Source:        manifest.PackageSource{Git: "https://example.com/libfoo.git", Commit: "11223344"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "core", pkg)

	// Trigger package
	cmd := NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"package", "trigger", "core/libfoo", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("failed to trigger: %v", err)
	}

	var trigRes map[string]any
	_ = json.Unmarshal(outBuf.Bytes(), &trigRes)
	jobID := trigRes["job_id"].(string)

	// Simulate worker picking up job and completing it
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to jetstream: %v", err)
	}
	defer nc.Close()

	buildingMsg, _ := json.Marshal(map[string]any{
		"job_id":       jobID,
		"project":      "core",
		"package":      "libfoo",
		"phase":        "BUILDING",
		"architecture": "x86_64",
	})
	_, _ = js.Publish(ctx, natsqueue.SubjectJobStatus, buildingMsg)

	succeededMsg, _ := json.Marshal(map[string]any{
		"job_id":       jobID,
		"project":      "core",
		"package":      "libfoo",
		"phase":        "SUCCEEDED",
		"architecture": "x86_64",
		"exit_code":    0,
	})
	_, _ = js.Publish(ctx, natsqueue.SubjectJobStatus, succeededMsg)
	_ = nc.Flush()

	// Now start watch dashboard
	m := newLiveDashboardModel(natsURL, nc, js)
	if len(m.jobs) == 0 {
		t.Fatalf("expected job %s to be present, got 0", jobID)
	}
	if m.jobs[0].Target != "SUCCEEDED" {
		t.Errorf("expected phase SUCCEEDED, got %s", m.jobs[0].Target)
	}
}

func TestWatchShowsAdminTriggeredJobs(t *testing.T) {
	_, natsURL := runEmbeddedNATS(t)
	t.Setenv("NATS_URL", natsURL)

	store := storage.NewMemoryStore()
	ctx := context.Background()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "devel:tools"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Architectures: []string{"x86_64"}},
		},
	}
	_ = store.CreateProject(ctx, prj)
	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "ripgrep"},
		Source:        manifest.PackageSource{Git: "https://example.com/ripgrep.git", Commit: "aabbccdd"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "devel:tools", pkg)

	// Admin trigger
	cmd := NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"admin", "trigger", "devel:tools", "ripgrep", "--priority", "high", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("failed to admin trigger: %v", err)
	}

	var res []map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil || len(res) == 0 {
		t.Fatalf("failed to parse admin trigger output: %v\nOutput: %s", err, outBuf.String())
	}
	jobID := res[0]["job_id"].(string)
	if _, err := uuid.Parse(jobID); err != nil {
		t.Fatalf("expected admin trigger job_id to be a valid RFC 4122 UUID, got: %v (err: %v)", jobID, err)
	}

	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to jetstream: %v", err)
	}
	defer nc.Close()

	m := newLiveDashboardModel(natsURL, nc, js)
	if len(m.jobs) == 0 {
		t.Fatalf("expected admin-triggered job %s to appear in watch, got 0", jobID)
	}
	if m.jobs[0].JobID != jobID {
		t.Errorf("expected job ID %s, got %s", jobID, m.jobs[0].JobID)
	}
	if m.jobs[0].Package != "ripgrep" {
		t.Errorf("expected package ripgrep, got %s", m.jobs[0].Package)
	}
}
