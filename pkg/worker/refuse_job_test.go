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

// Heartbeats carry a rising sequence number and a refusal reports the one the
// worker had, so the dispatcher can wait for a newer heartbeat.
func TestRefusalReportsHeartbeatSeqAndHeartbeatFollows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, natsURL := runEmbeddedNATS(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	seqs := make(chan int64, 64)
	_, _ = nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if json.Unmarshal(msg.Data, &hb) == nil {
			seqs <- hb.Seq
		}
	})
	refusal := make(chan int64, 1)
	_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st struct {
			Phase string `json:"phase"`
			Seq   int64  `json:"heartbeat_seq"`
		}
		if json.Unmarshal(msg.Data, &st) == nil && st.Phase == "REQUEUED" {
			refusal <- st.Seq
		}
	})

	sb := &countingSandbox{gate: make(chan struct{})}
	defer close(sb.gate)
	cfg := worker.WorkerConfig{WorkerID: "w-seq", Architecture: "x86_64", Hardware: v1.WorkerHardware{
		TotalVCPUs: 2, AvailableVCPUs: 2, TotalMemoryMB: 4096, AvailableMemoryMB: 4096,
	}}
	daemon := worker.NewWorkerDaemon(cfg, nc, sb)
	go func() { _ = daemon.Start(ctx) }()
	defer daemon.Stop()
	time.Sleep(100 * time.Millisecond)

	data, _ := json.Marshal(&v1.BuildJob{JobID: "j", Project: "p", Package: "j", Architecture: "x86_64",
		Resources: v1.ResourceRequirements{VCPUs: 8, MemoryMB: 1024}})
	_ = nc.Publish("bsgo.workers.w-seq.job", data)
	// vCPUs above the total are capped to it, so ask for what cannot fit after a first job
	time.Sleep(100 * time.Millisecond)
	data2, _ := json.Marshal(&v1.BuildJob{JobID: "j2", Project: "p", Package: "j2", Architecture: "x86_64",
		Resources: v1.ResourceRequirements{VCPUs: 1, MemoryMB: 1024}})
	_ = nc.Publish("bsgo.workers.w-seq.job", data2)

	var refusedAt int64
	select {
	case refusedAt = <-refusal:
	case <-time.After(2 * time.Second):
		t.Fatal("no refusal")
	}
	var prev, last int64
	deadline := time.After(2 * time.Second)
	for last <= refusedAt {
		select {
		case s := <-seqs:
			if s <= prev {
				t.Fatalf("heartbeat seq %d after %d", s, prev)
			}
			prev, last = s, s
		case <-deadline:
			t.Fatalf("no heartbeat above the refusal seq %d", refusedAt)
		}
	}
}
