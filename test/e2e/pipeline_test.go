package e2e_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
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
		t.Fatalf("failed to create embedded nats: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatalf("nats start timed out")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s, s.ClientURL()
}

func TestE2EFullDistributionPipeline(t *testing.T) {
	ctx := context.Background()

	// Always use isolated embedded NATS for tests to prevent polluting live dev streams
	_, natsURL := runEmbeddedNATS(t)

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
		Durable:   "worker-e2e-consumer",
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
}
