package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/worker"
)

// manyArtifactsSandbox "builds" jobs that each produce perJob distinct RPMs.
type manyArtifactsSandbox struct{ perJob int }

func (m manyArtifactsSandbox) Execute(_ context.Context, job *v1.BuildJob, _ io.Writer) (*worker.ExecutionResult, error) {
	res := &worker.ExecutionResult{}
	for i := 0; i < m.perJob; i++ {
		res.Artifacts = append(res.Artifacts, v1.ChrootRPM{SHA256: fmt.Sprintf("%s-%064d", job.JobID, i)})
	}
	return res, nil
}

func (manyArtifactsSandbox) Cleanup(context.Context) error { return nil }

// A worker that built a lot must keep heartbeating. The advertised artifact
// digests used to grow without bound until the heartbeat exceeded the NATS
// max_payload; the publish then failed silently and the dispatcher saw a dead
// worker that was in fact still building.
func TestWorkerHeartbeatStaysPublishableAfterManyBuilds(t *testing.T) {
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
	var lastSize int
	done := make(chan struct{}, 64)
	_, _ = nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		if json.Unmarshal(msg.Data, &last) == nil {
			lastSize = len(msg.Data)
			done <- struct{}{}
		}
	})
	statuses := make(chan struct{}, 64)
	_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st map[string]any
		if json.Unmarshal(msg.Data, &st) == nil && st["phase"] == "SUCCEEDED" {
			statuses <- struct{}{}
		}
	})

	daemon := worker.NewWorkerDaemon(worker.WorkerConfig{WorkerID: "w-big", Architecture: "x86_64"}, nc, manyArtifactsSandbox{perJob: 5000})
	go func() { _ = daemon.Start(ctx) }()
	defer daemon.Stop()
	time.Sleep(100 * time.Millisecond)

	const jobs = 5 // 25000 distinct digests, far above max_payload
	for i := 0; i < jobs; i++ {
		data, _ := json.Marshal(&v1.BuildJob{JobID: fmt.Sprintf("big-%d", i), Project: "p", Package: "x", Architecture: "x86_64"})
		if err := nc.Publish("bsgo.workers.w-big.job", data); err != nil {
			t.Fatal(err)
		}
		select {
		case <-statuses:
		case <-time.After(10 * time.Second):
			t.Fatalf("job %d did not finish", i)
		}
	}

	for len(done) > 0 {
		<-done
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat after the builds: the worker went silent")
	}
	mu.Lock()
	defer mu.Unlock()
	if limit := int(nc.MaxPayload()); lastSize >= limit {
		t.Errorf("heartbeat is %d bytes, at or over the %d byte NATS payload limit", lastSize, limit)
	}
	if n := len(last.CachedRPMHashes); n == 0 || n > 4096 {
		t.Errorf("heartbeat advertises %d digests, want 1..4096", n)
	}
}
