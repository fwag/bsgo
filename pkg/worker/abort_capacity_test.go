package worker_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/worker"
)

// Every way a job can end gives its resources back and removes it from the
// active jobs: abort by the server, build failure, duplicate dispatch.
func TestWorkerReleasesCapacityOnEveryExit(t *testing.T) {
	tests := []struct {
		name string
		end  func(nc *nats.Conn, sb *countingSandbox)
	}{
		{"abort by control message", func(nc *nats.Conn, _ *countingSandbox) {
			ctrl, _ := json.Marshal(v1.JobControl{JobID: "j1", Action: "ABORT", Reason: "WORKER_LEASE_EXPIRED"})
			_ = nc.Publish("bsgo.jobs.j1.control", ctrl)
		}},
		{"build finishes", func(_ *nats.Conn, sb *countingSandbox) { close(sb.gate) }},
		{"abort after a repeated dispatch", func(nc *nats.Conn, _ *countingSandbox) {
			data, _ := json.Marshal(&v1.BuildJob{JobID: "j1", Project: "p", Package: "x", Architecture: "x86_64"})
			_ = nc.Publish("bsgo.workers.w-cap.job", data)
			time.Sleep(100 * time.Millisecond)
			ctrl, _ := json.Marshal(v1.JobControl{JobID: "j1", Action: "ABORT"})
			_ = nc.Publish("bsgo.jobs.j1.control", ctrl)
		}},
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

			var mu sync.Mutex
			var last v1.WorkerHeartbeat
			_, _ = nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
				mu.Lock()
				defer mu.Unlock()
				last = v1.WorkerHeartbeat{}
				_ = json.Unmarshal(msg.Data, &last)
			})

			sb := &countingSandbox{gate: make(chan struct{})}
			cfg := worker.WorkerConfig{WorkerID: "w-cap", Architecture: "x86_64",
				Hardware: v1.WorkerHardware{TotalVCPUs: 4, TotalMemoryMB: 8192}}
			daemon := worker.NewWorkerDaemon(cfg, nc, sb)
			go func() { _ = daemon.Start(ctx) }()
			defer daemon.Stop()
			time.Sleep(100 * time.Millisecond)

			data, _ := json.Marshal(&v1.BuildJob{JobID: "j1", Project: "p", Package: "x", Architecture: "x86_64",
				Resources: v1.ResourceRequirements{VCPUs: 2, MemoryMB: 2048}})
			_ = nc.Publish("bsgo.workers.w-cap.job", data)
			waitFor(t, "job active", func() bool { return len(daemon.ActiveJobs()) == 1 })

			tt.end(nc, sb)
			waitFor(t, "job released", func() bool { return len(daemon.ActiveJobs()) == 0 })
			waitFor(t, "full capacity in the heartbeat", func() bool {
				mu.Lock()
				defer mu.Unlock()
				return last.Status == "IDLE" && last.Hardware.AvailableVCPUs == 4 &&
					last.Hardware.AvailableMemoryMB == 8192 && len(last.ActiveJobIDs) == 0 && last.ActiveJobID == ""
			})
		})
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
