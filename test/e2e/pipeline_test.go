package e2e_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestcontainers(t *testing.T, ctx context.Context) (natsURL, pgURL, s3URL string) {
	t.Helper()

	// 1. NATS JetStream Container
	natsReq := testcontainers.ContainerRequest{
		Image:        "nats:2.10-alpine",
		Cmd:          []string{"-js"},
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor:   wait.ForLog("Server is ready"),
	}
	natsC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: natsReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start NATS container: %v", err)
	}
	t.Cleanup(func() { _ = natsC.Terminate(ctx) })

	natsIP, _ := natsC.Host(ctx)
	natsPort, _ := natsC.MappedPort(ctx, "4222")
	natsURL = "nats://" + natsIP + ":" + natsPort.Port()

	// 2. PostgreSQL Container
	pgReq := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		Env:          map[string]string{"POSTGRES_USER": "bsgo", "POSTGRES_PASSWORD": "password", "POSTGRES_DB": "bsgodb"},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor:   wait.ForLog("database system is ready to accept connections"),
	}
	pgC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: pgReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start Postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	pgIP, _ := pgC.Host(ctx)
	pgPort, _ := pgC.MappedPort(ctx, "5432")
	pgURL = "postgres://bsgo:password@" + pgIP + ":" + pgPort.Port() + "/bsgodb?sslmode=disable"

	// 3. MinIO (Garage S3 equivalent) Container
	s3Req := testcontainers.ContainerRequest{
		Image:        "minio/minio:RELEASE.2024-01-28T22-35-53Z",
		Cmd:          []string{"server", "/data"},
		Env:          map[string]string{"MINIO_ROOT_USER": "admin", "MINIO_ROOT_PASSWORD": "password"},
		ExposedPorts: []string{"9000/tcp"},
		WaitingFor:   wait.ForLog("MinIO Object Storage Server"),
	}
	s3C, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: s3Req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start S3 container: %v", err)
	}
	t.Cleanup(func() { _ = s3C.Terminate(ctx) })

	s3IP, _ := s3C.Host(ctx)
	s3Port, _ := s3C.MappedPort(ctx, "9000")
	s3URL = "http://" + s3IP + ":" + s3Port.Port()

	return natsURL, pgURL, s3URL
}

func TestE2EFullDistributionPipeline(t *testing.T) {
	ctx := context.Background()

	// 1. Spin up Testcontainers
	natsURL, _, _ := setupTestcontainers(t, ctx)

	// 2. Connect to NATS and setup streams
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to nats: %v", err)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		t.Fatalf("failed to setup streams: %v", err)
	}

	// 3. Simulate Webhook Trigger -> Scheduler
	trigger := v1.BuildTrigger{
		TriggerID: "e2e-trig-01",
		Project:   "openSUSE:Factory",
		Package:   "ripgrep",
	}
	triggerData, _ := json.Marshal(trigger)
	_, err = js.Publish(ctx, "bsgo.triggers.openSUSE:Factory.ripgrep", triggerData)
	if err != nil {
		t.Fatalf("failed to publish trigger: %v", err)
	}

	// 4. Simulate Scheduler -> Dispatcher
	job := v1.BuildJob{
		JobID:        "job-e2e-01",
		Project:      "openSUSE:Factory",
		Package:      "ripgrep",
		Architecture: "x86_64",
		Priority:     100,
	}
	jobData, _ := json.Marshal(job)
	_, err = js.Publish(ctx, natsqueue.SubjectJobsPending, jobData)
	if err != nil {
		t.Fatalf("failed to publish job to pending stream: %v", err)
	}

	// 5. Worker consumes job
	cons, err := js.CreateOrUpdateConsumer(ctx, natsqueue.StreamJobsPending, jetstream.ConsumerConfig{
		Durable:   "worker-consumer",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}

	msg, err := cons.Next()
	if err != nil {
		t.Fatalf("failed to fetch message: %v", err)
	}
	_ = msg.Ack()

	var receivedJob v1.BuildJob
	_ = json.Unmarshal(msg.Data(), &receivedJob)
	if receivedJob.Package != "ripgrep" {
		t.Errorf("expected job for ripgrep, got %s", receivedJob.Package)
	}

	// 6. Worker simulates completion and emits PublishRequest
	pubReq := publisher.PublishRequest{
		RequestID:    "pub-req-01",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
		NewRPMPaths:  []string{"ripgrep-14.1.0-1.x86_64.rpm"},
	}
	pubData, _ := json.Marshal(pubReq)
	_, err = js.Publish(ctx, "bsgo.publish.rpm.openSUSE:Factory.standard.x86_64", pubData)
	if err != nil {
		t.Fatalf("failed to publish to PUBLISH stream: %v", err)
	}

	// 7. Verify Publisher Stream receipt
	pubCons, err := js.CreateOrUpdateConsumer(ctx, natsqueue.StreamPublish, jetstream.ConsumerConfig{
		Durable:   "publisher-consumer",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("failed to create publish consumer: %v", err)
	}

	pubMsg, err := pubCons.Next()
	if err != nil {
		t.Fatalf("failed to fetch publish message: %v", err)
	}
	_ = pubMsg.Ack()

	if !strings.Contains(pubMsg.Subject(), "openSUSE:Factory") {
		t.Errorf("expected publish subject to contain openSUSE:Factory, got %s", pubMsg.Subject())
	}
}
