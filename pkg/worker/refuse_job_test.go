package worker_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/worker"
)

// A job that does not fit what the worker has left is not started: the
// worker reports it REQUEUED for lack of capacity and keeps its running jobs.
func TestWorkerRefusesJobBeyondFreeCapacity(t *testing.T) {
	tests := []struct {
		name       string
		second     v1.ResourceRequirements
		wantRefuse bool
	}{
		{"fits the remaining capacity", v1.ResourceRequirements{VCPUs: 2, MemoryMB: 4096}, false},
		{"too many vCPUs", v1.ResourceRequirements{VCPUs: 3, MemoryMB: 1024}, true},
		{"too much memory", v1.ResourceRequirements{VCPUs: 1, MemoryMB: 8192}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, natsURL := runEmbeddedNATS(t)
			nc, err := nats.Connect(natsURL)
			if err != nil {
				t.Fatal(err)
			}
			defer nc.Close()

			sb := &countingSandbox{gate: make(chan struct{})}
			defer close(sb.gate)
			cfg := worker.WorkerConfig{WorkerID: "w-refuse", Architecture: "x86_64", Hardware: v1.WorkerHardware{
				TotalVCPUs: 4, AvailableVCPUs: 4, TotalMemoryMB: 8192, AvailableMemoryMB: 8192,
			}}
			daemon := worker.NewWorkerDaemon(cfg, nc, sb)
			go func() { _ = daemon.Start(ctx) }()
			defer daemon.Stop()
			time.Sleep(100 * time.Millisecond)

			refused := make(chan string, 4)
			_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
				var st map[string]any
				if json.Unmarshal(msg.Data, &st) == nil && st["job_id"] == "j2" && st["phase"] == "REQUEUED" {
					refused <- st["reason"].(string)
				}
			})
			send := func(id string, res v1.ResourceRequirements) {
				data, _ := json.Marshal(&v1.BuildJob{JobID: id, Project: "p", Package: id, Architecture: "x86_64", Resources: res})
				_ = nc.Publish("bsgo.workers.w-refuse.job", data)
				time.Sleep(150 * time.Millisecond)
			}
			send("j1", v1.ResourceRequirements{VCPUs: 2, MemoryMB: 4096})
			send("j2", tt.second)

			select {
			case reason := <-refused:
				if !tt.wantRefuse || reason != v1.ReasonWorkerCapacity {
					t.Fatalf("job refused (%s), want refused=%v", reason, tt.wantRefuse)
				}
			case <-time.After(300 * time.Millisecond):
				if tt.wantRefuse {
					t.Fatal("job beyond free capacity was not refused")
				}
			}
			want := 2
			if tt.wantRefuse {
				want = 1
			}
			if got := len(daemon.ActiveJobs()); got != want {
				t.Fatalf("%d active jobs, want %d", got, want)
			}
		})
	}
}
