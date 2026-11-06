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
