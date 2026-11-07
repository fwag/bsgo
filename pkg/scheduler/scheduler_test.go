package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func runEmbeddedNATS(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{
		Port:      -1, // Random available port
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

func TestSchedulerWebhookToNATSJob(t *testing.T) {
	ctx := context.Background()
	_, natsURL := runEmbeddedNATS(t)

	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to jetstream: %v", err)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		t.Fatalf("failed to setup jetstream streams: %v", err)
	}

	// 1. Prepare Store with project and package
	store := storage.NewMemoryStore()
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name: "openSUSE:Factory",
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "ripgrep",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/BurntSushi/ripgrep.git",
			Branch: "master",
			Recipe: "ripgrep.spec",
		},
		Build: manifest.PackageBuild{
			Enabled: true,
		},
	}
	_ = store.UpsertPackage(ctx, "openSUSE:Factory", pkg)

	// 2. Prepare solv pool with solver
	solverPool, err := solv.NewWorkerPool("x86_64", 2)
	if err != nil {
		t.Fatalf("failed to create solver pool: %v", err)
	}
	defer solverPool.Close()

	// 3. Create Scheduler
	pub := scheduler.NewNatsPublisher(func(pCtx context.Context, subject string, data []byte) error {
		_, err := js.Publish(pCtx, subject, data)
		return err
	})
	sched := scheduler.NewScheduler(store, solverPool, pub, scheduler.Config{S3Bucket: "bsgo-artifacts"})

	// 4. Trigger build
	trigger := v1.NewTrigger(
		"openSUSE:Factory",
		"ripgrep",
		"https://github.com/BurntSushi/ripgrep.git",
		"a1b2c3d4e5f6",
		"refs/heads/master",
		"alice",
	)

	// In test, since ripgrep solvable isn't in pool, solv.Resolve returns NoProviderError.
	// But let's verify error handling:
	_, err = sched.ProcessTrigger(ctx, trigger)
	if err == nil {
		t.Fatalf("expected solver error for missing provider in unseeded pool")
	}

	// Now seed pool via a temporary repo dumped to memory
	tempPool, _ := solv.NewPool("x86_64")
	repo := tempPool.CreateRepo("base")
	_ = repo.AddPackage(solv.PackageSpec{
		Name:     "ripgrep",
		EVR:      "14.1.0-1",
		Arch:     "x86_64",
		Provides: []string{"ripgrep"},
	})
	solvBlob, _ := repo.DumpMemory()
	tempPool.Close()

	// Load into pool
	loadPool, _ := solv.NewPool("x86_64")
	_, _ = loadPool.LoadRepoFromMemory("seeded", solvBlob)
	defer loadPool.Close()

	// Direct publish test to NATS
	jobData, _ := json.Marshal(&v1.BuildJob{
		JobID:        "job-test-01",
		Project:      "openSUSE:Factory",
		Package:      "ripgrep",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     100,
	})
	_, err = js.Publish(ctx, natsqueue.SubjectJobsPending, jobData)
	if err != nil {
		t.Fatalf("failed to publish test job to NATS: %v", err)
	}

	// Consume message from JOBS_PENDING stream
	cons, err := js.CreateOrUpdateConsumer(ctx, natsqueue.StreamJobsPending, jetstream.ConsumerConfig{
		Durable:   "test-consumer",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}

	msg, err := cons.Next()
	if err != nil {
		t.Fatalf("failed to fetch message from stream: %v", err)
	}
	defer func() { _ = msg.Ack() }()

	var receivedJob v1.BuildJob
	if err := json.Unmarshal(msg.Data(), &receivedJob); err != nil {
		t.Fatalf("failed to unmarshal received job: %v", err)
	}
	if receivedJob.JobID != "job-test-01" {
		t.Errorf("expected job ID 'job-test-01', got %s", receivedJob.JobID)
	}
}

func TestPrioritizedTriggerQueueAntiStarvation(t *testing.T) {
	// Anti-starvation threshold: 3 non-low items before forcing low item
	q := scheduler.NewPrioritizedTriggerQueue(3)

	// Push 10 high, 2 med, 2 low triggers
	for i := 0; i < 10; i++ {
		q.Push(&v1.BuildTrigger{TriggerID: "high", Priority: "high"})
	}
	for i := 0; i < 2; i++ {
		q.Push(&v1.BuildTrigger{TriggerID: "med", Priority: "med"})
	}
	for i := 0; i < 2; i++ {
		q.Push(&v1.BuildTrigger{TriggerID: "low", Priority: "low"})
	}

	h, m, l := q.Counts()
	if h != 10 || m != 2 || l != 2 {
		t.Fatalf("expected counts (10, 2, 2), got (%d, %d, %d)", h, m, l)
	}

	// 1, 2, 3 should be high
	for i := 0; i < 3; i++ {
		tr := q.Pop()
		if tr.Priority != "high" {
			t.Fatalf("expected high priority on pop %d, got %s", i+1, tr.Priority)
		}
	}

	// Pop 4: anti-starvation threshold (3) exceeded, MUST be low!
	tr := q.Pop()
	if tr.Priority != "low" {
		t.Fatalf("expected low priority due to anti-starvation on pop 4, got %s", tr.Priority)
	}

	// Pop 5, 6, 7 should be high
	for i := 0; i < 3; i++ {
		tr := q.Pop()
		if tr.Priority != "high" {
			t.Fatalf("expected high priority on pop %d, got %s", i+5, tr.Priority)
		}
	}

	// Pop 8: threshold exceeded again, MUST be remaining low!
	tr = q.Pop()
	if tr.Priority != "low" {
		t.Fatalf("expected remaining low priority on pop 8, got %s", tr.Priority)
	}
}

type mockSolver struct {
	pkgs []solv.ResolvedPackage
}

func (m *mockSolver) Resolve(_ context.Context, _ solv.ExpansionRequest) (*solv.ExpansionResult, error) {
	return &solv.ExpansionResult{Packages: m.pkgs}, nil
}

func TestSchedulerRebuildPolicyGating(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "test-prj"},
		Targets: map[string]manifest.BuildTarget{
			"local-target": {
				Architectures: []string{"x86_64"},
				Flags: manifest.TargetFlags{
					Build:   true,
					Rebuild: "local",
				},
			},
			"transitive-target": {
				Architectures: []string{"x86_64"},
				Flags: manifest.TargetFlags{
					Build:   true,
					Rebuild: "transitive",
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "app"},
		Source:        manifest.PackageSource{Git: "https://git/app.git", Branch: "main"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "test-prj", pkg)

	solver := &mockSolver{
		pkgs: []solv.ResolvedPackage{{Name: "app", EVR: "1.0-1", Arch: "x86_64"}},
	}

	var publishedJobs [][]byte
	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, data []byte) error {
		publishedJobs = append(publishedJobs, data)
		return nil
	})
	sched := scheduler.NewScheduler(store, solver, pub, scheduler.Config{S3Bucket: "artifacts"})

	// 1. Cascade trigger without force: should skip "local-target" and only build for "transitive-target"
	cascadeTrigger := &v1.BuildTrigger{
		TriggerID: "trig-cascade",
		Project:   "test-prj",
		Package:   "app",
		Priority:  "med",
		Reason:    "CASCADE",
	}

	jobs, err := sched.ProcessTrigger(ctx, cascadeTrigger)
	if err != nil {
		t.Fatalf("unexpected error processing cascade trigger: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 job for transitive-target, got %d", len(jobs))
	}
	if jobs[0].Target != "transitive-target" {
		t.Errorf("expected target 'transitive-target', got %s", jobs[0].Target)
	}
	if jobs[0].Priority < 500 {
		t.Errorf("expected priority >= 500 for med tier, got %d", jobs[0].Priority)
	}

	// 2. Cascade trigger with force_rebuild = true: builds both!
	cascadeTrigger.ForceRebuild = true
	jobs, err = sched.ProcessTrigger(ctx, cascadeTrigger)
	if err != nil {
		t.Fatalf("unexpected error processing forced cascade trigger: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs when ForceRebuild is true, got %d", len(jobs))
	}
}

func TestSchedulerProcessCommitBatch(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "batch-prj"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Flags:         manifest.TargetFlags{Build: true},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	for _, name := range []string{"pkg-a", "pkg-b"} {
		pkg := &manifest.PackageManifest{
			SchemaVersion: "v1",
			Package:       manifest.PackageInfo{Name: name},
			Source:        manifest.PackageSource{Git: "https://git/" + name, Branch: "main"},
			Build:         manifest.PackageBuild{Enabled: true},
		}
		_ = store.UpsertPackage(ctx, "batch-prj", pkg)
	}

	solver := &mockSolver{
		pkgs: []solv.ResolvedPackage{{Name: "base", EVR: "1.0-1", Arch: "x86_64"}},
	}

	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, _ []byte) error {
		return nil
	})
	sched := scheduler.NewScheduler(store, solver, pub, scheduler.Config{S3Bucket: "artifacts"})

	triggers := []*v1.BuildTrigger{
		v1.NewTrigger("batch-prj", "pkg-a", "https://git/pkg-a", "c0ffee", "main", "alice"),
		v1.NewTrigger("batch-prj", "pkg-b", "https://git/pkg-b", "c0ffee", "main", "alice"),
	}

	jobs, err := sched.ProcessCommitBatch(ctx, triggers)
	if err != nil {
		t.Fatalf("unexpected error in ProcessCommitBatch: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs from commit batch, got %d", len(jobs))
	}
}
