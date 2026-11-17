package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/worker"
)

// countingSandbox blocks every build until released and counts the builds.
type countingSandbox struct {
	mu      sync.Mutex
	started int
	gate    chan struct{}
}

func (c *countingSandbox) Execute(ctx context.Context, _ *v1.BuildJob, _ io.Writer) (*worker.ExecutionResult, error) {
	c.mu.Lock()
	c.started++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return &worker.ExecutionResult{ExitCode: 137}, ctx.Err()
	case <-c.gate:
		return &worker.ExecutionResult{}, nil
	}
}

func (c *countingSandbox) Cleanup(_ context.Context) error { return nil }

func (c *countingSandbox) builds() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// The dispatcher can send a job again while the worker still runs it (a
// requeue after a missed heartbeat). The running build must go on untouched.
func TestWorkerIgnoresRepeatedDispatchOfRunningJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, natsURL := runEmbeddedNATS(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	sb := &countingSandbox{gate: make(chan struct{})}
	daemon := worker.NewWorkerDaemon(worker.WorkerConfig{WorkerID: "w-dup", Architecture: "x86_64"}, nc, sb)
	go func() { _ = daemon.Start(ctx) }()
	defer daemon.Stop()
	time.Sleep(100 * time.Millisecond)

	phases := make(chan string, 10)
	_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st map[string]any
		if json.Unmarshal(msg.Data, &st) == nil && st["job_id"] == "dup-job" {
			phases <- st["phase"].(string)
		}
	})

	data, _ := json.Marshal(&v1.BuildJob{JobID: "dup-job", Project: "p", Package: "x", Architecture: "x86_64"})
	for i := 0; i < 3; i++ {
		_ = nc.Publish("bsgo.workers.w-dup.job", data)
		time.Sleep(100 * time.Millisecond)
	}
	if n := sb.builds(); n != 1 {
		t.Fatalf("%d builds started for one job ID, want 1", n)
	}
	if got := daemon.ActiveJobs(); len(got) != 1 {
		t.Fatalf("active jobs %v, want the one running build", got)
	}

	close(sb.gate)
	want := []string{"BUILDING", "SUCCEEDED"}
	for _, w := range want {
		select {
		case got := <-phases:
			if got != w {
				t.Fatalf("phase %s, want %s", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", w)
		}
	}
}
