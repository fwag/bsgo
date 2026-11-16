package signing_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/signing"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func embeddedJS(t *testing.T) jetstream.JetStream {
	t.Helper()
	s, err := server.NewServer(&server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats not ready")
	}
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if err := natsqueue.SetupStreams(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	return js
}

type failKeys struct{}

func (failKeys) ResolveKey(context.Context, string, string) (*storage.ProjectSigningKey, error) {
	return nil, errors.New("no key")
}

type queueFixture struct {
	queue signing.Queue
	stage *signing.Stage
	store *storage.MemoryStore
	job   signing.Job
}

func newQueueFixture(t *testing.T) *queueFixture {
	t.Helper()
	f := &queueFixture{store: storage.NewMemoryStore()}
	f.queue = signing.Queue{
		JS: embeddedJS(t), AckWait: 3 * time.Second, MaxDeliver: 3, Workers: 2, RetryDelay: 50 * time.Millisecond,
		Logf: t.Logf,
	}
	f.stage = &signing.Stage{
		Keys: failKeys{}, States: f.store, Attempts: 1, Publish: f.queue.PublishAnswer,
		Sleep: func(context.Context, time.Duration) {},
	}
	f.job = signing.Job{
		JobID: "job-1", Project: "p", Package: "foo", Target: "std", Architecture: "x86_64",
		Artifacts: []indexer.Artifact{{Filename: "foo.rpm", S3Key: "k/foo.rpm"}},
	}
	err := f.store.UpsertBuildState(context.Background(), &storage.PackageBuildState{
		Project: "p", Repo: "std", Arch: "x86_64", Package: "foo", JobID: "job-1", State: storage.StateSigning,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type answers struct {
	mu   sync.Mutex
	got  []signing.Job
	fail atomic.Int32 // number of calls that still fail
}

func (a *answers) handle(_ context.Context, data []byte) error {
	if a.fail.Add(-1) >= 0 {
		return errors.New("integration failed")
	}
	j, err := signing.Decode(data)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.got = append(a.got, j)
	a.mu.Unlock()
	return nil
}

func (a *answers) wait(t *testing.T) signing.Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(a.got)
		var j signing.Job
		if n > 0 {
			j = a.got[0]
		}
		a.mu.Unlock()
		if n > 0 {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no answer arrived")
	return signing.Job{}
}

func streamEmpty(t *testing.T, js jetstream.JetStream, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := js.Stream(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		info, err := s.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.State.Msgs == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("stream %s still holds messages: every message must be acknowledged", name)
}

func TestQueueRoundTripAcknowledges(t *testing.T) {
	f := newQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := &answers{}
	if err := f.queue.StartResults(ctx, res.handle); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.StartSigner(ctx, f.stage); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Request(ctx, f.job); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Request(ctx, f.job); err != nil { // same job ID: stored once
		t.Fatal(err)
	}
	got := res.wait(t)
	if got.JobID != "job-1" || got.Error == "" {
		t.Fatalf("answer %+v: want job-1 with the key error", got)
	}
	streamEmpty(t, f.queue.JS, natsqueue.StreamSignRequests)
	streamEmpty(t, f.queue.JS, natsqueue.StreamSignResults)
	if len(res.got) != 1 {
		t.Errorf("%d answers, want exactly one", len(res.got))
	}
}

func TestQueueRedeliversFailedIntegration(t *testing.T) {
	f := newQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := &answers{}
	res.fail.Store(2) // two failed integrations, the third delivery succeeds
	if err := f.queue.StartResults(ctx, res.handle); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.StartSigner(ctx, f.stage); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Request(ctx, f.job); err != nil {
		t.Fatal(err)
	}
	res.wait(t)
	streamEmpty(t, f.queue.JS, natsqueue.StreamSignResults)
}

func TestQueueFailsJobOnLastDelivery(t *testing.T) {
	f := newQueueFixture(t)
	var publishes atomic.Int32
	f.stage.Publish = func(ctx context.Context, data []byte) error {
		if publishes.Add(1) <= 3 { // the answers of the three deliveries are lost
			return errors.New("nats unavailable")
		}
		return f.queue.PublishAnswer(ctx, data)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := &answers{}
	if err := f.queue.StartResults(ctx, res.handle); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.StartSigner(ctx, f.stage); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.Request(ctx, f.job); err != nil {
		t.Fatal(err)
	}
	got := res.wait(t)
	if got.Error == "" || publishes.Load() != 4 {
		t.Fatalf("answer %+v after %d publishes: want a terminal failure on the last delivery", got, publishes.Load())
	}
	streamEmpty(t, f.queue.JS, natsqueue.StreamSignRequests)
}

func TestQueueDropsMalformedMessages(t *testing.T) {
	f := newQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.queue.StartSigner(ctx, f.stage); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.JS.Publish(ctx, natsqueue.SubjectJobFinished, []byte("{")); err != nil {
		t.Fatal(err)
	}
	streamEmpty(t, f.queue.JS, natsqueue.StreamSignRequests)
}
