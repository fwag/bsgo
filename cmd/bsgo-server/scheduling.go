package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/dod"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

func logf(component, format string, args ...any) {
	fmt.Printf("[%s] [%s] %s\n", time.Now().Format("15:04:05"), component, fmt.Sprintf(format, args...))
}

func startSchedulerAndDispatcher(ctx context.Context, cfg ServerConfig, nc *nats.Conn, js jetstream.JetStream, store storage.Store) {
	disp := dispatcher.NewDispatcher(dispatcher.NewFuncPublisher(func(subj string, data []byte) {
		_ = nc.Publish(subj, data)
	}), 30*time.Second)
	subscribeWorkerHeartbeats(nc, disp)
	subscribeDispatcherResults(nc, disp)
	startDispatchLoop(ctx, disp, nc)
	startOrphanReconciler(ctx, store, disp, nc)
	if !cfg.Checker {
		logf("SCHEDULER", "checker disabled; no jobs will be scheduled by this instance")
		return
	}
	startChecker(ctx, cfg, nc, js, store, disp)
}

// subscribeDispatcherResults feeds finished jobs to the dispatcher's worker
// health and fair-share accounting.
func subscribeDispatcherResults(nc *nats.Conn, disp *dispatcher.Dispatcher) {
	_, _ = nc.Subscribe(natsqueue.SubjectJobStatus, func(msg *nats.Msg) {
		var st struct {
			JobID    string `json:"job_id"`
			Project  string `json:"project"`
			Package  string `json:"package"`
			Phase    string `json:"phase"`
			WorkerID string `json:"worker_id"`
			ExitCode *int   `json:"exit_code"`
		}
		if err := json.Unmarshal(msg.Data, &st); err != nil {
			return
		}
		switch st.Phase {
		case "SUCCEEDED", "FAILED", "ABORTED":
		default:
			return
		}
		logf("STATUS", "job %s (%s) phase -> %s", st.JobID, st.Package, st.Phase)
		exit := 0
		switch st.Phase {
		case "ABORTED":
			exit = 137
		case "FAILED":
			exit = 1
			if st.ExitCode != nil {
				exit = *st.ExitCode
			}
		}
		disp.RecordJobResult(st.JobID, st.WorkerID, st.Project, exit, 30*time.Second)
	})
}

// artifactStore returns the object storage for build repositories and recipe
// caches, or a disk fallback for local runs.
func artifactStore(ctx context.Context, cfg ServerConfig) (*s3DoDAdapter, fetch.Cache) {
	if st, err := s3.NewS3Storage(ctx, cfg.S3Endpoint, "garage", cfg.S3Bucket, "", ""); err == nil {
		return &s3DoDAdapter{storage: st}, &fetch.S3Cache{Store: st}
	}
	return &s3DoDAdapter{storage: NewDiskS3Client("/tmp/bsgo-artifacts")}, &fetch.MemoryCache{}
}

func newCoordinator(ctx context.Context, js jetstream.JetStream) coord.Coordinator {
	c, err := coord.NewNATS(ctx, js, 30*time.Second)
	if err != nil {
		logf("SCHEDULER", "WARNING: NATS KV unavailable (%v); prpa ownership is local to this process", err)
		return &coord.Memory{}
	}
	return c
}

// startChecker runs the checker loop: it consumes dirty prpas, takes their
// leases and runs the passes; it also integrates the binaries of finished jobs.
func startChecker(ctx context.Context, cfg ServerConfig, nc *nats.Conn, js jetstream.JetStream, store storage.Store, disp *dispatcher.Dispatcher) {
	blobs, recipeCache := artifactStore(ctx, cfg)
	coordinator := newCoordinator(ctx, js)
	owner := "bsgo-server-" + uuid.NewString()
	reader := buildrepo.NewReader(store, blobs)
	events := &checker.Events{Projects: store, Marker: coordinator}

	ix := &indexer.Indexer{
		Pointers: store, Blobs: blobs, Reader: reader, Leases: coordinator, Owner: owner,
		Notify: natsNotifier{nc: nc}, Exports: indexer.ManifestExports{Projects: store},
	}
	handler := &jobstatus.Handler{Store: store, Integrator: ix, Dirty: events}
	subscribeJobFinished(ctx, nc, handler)
	subscribeRepoChanged(ctx, nc, events)
	subscribeTriggers(ctx, nc, store, events)

	chk := checker.New(checker.Deps{
		Store: store, Recipes: &fetch.Fetcher{Cache: recipeCache}, Indexes: reader,
		DoD:  dod.NewRepoCache(nil, blobs, store, dod.RepoCacheConfig{TTL: cfg.DoDRepomdTTL}),
		Sink: newJobSink(js, nc, disp, cfg.S3Bucket),
	})
	runner := &coord.Runner{
		Coord: coordinator, Owner: owner, Debounce: cfg.CheckerDebounce, MaxDelay: 10 * cfg.CheckerDebounce,
		Workers: cfg.CheckerWorkers,
		Process: func(ctx context.Context, prpa string) (time.Duration, error) { return runPass(ctx, chk, prpa) },
		OnError: func(prpa string, err error) {
			fmt.Fprintf(os.Stderr, "[%s] [CHECKER] %s: %v\n", time.Now().Format("15:04:05"), prpa, err)
		},
	}
	go runner.Run(ctx)
	go func() {
		<-ctx.Done()
		chk.Close()
	}()
	go resync(ctx, events, cfg.CheckerResync)
}

func runPass(ctx context.Context, chk *checker.Checker, prpa string) (time.Duration, error) {
	p, err := buildrepo.ParsePrpa(prpa)
	if err != nil {
		return 0, nil // a malformed mark can never succeed; drop it
	}
	start := time.Now()
	res, err := chk.Run(ctx, p)
	if err != nil {
		return 0, err
	}
	logf("CHECKER", "%s: %d scheduled, %d cancelled, states %v in %s",
		prpa, len(res.Scheduled), len(res.Cancelled), res.Counts, time.Since(start).Round(time.Millisecond))
	return res.RetryAfter, nil
}

// resync marks every prpa dirty at startup and then every interval, as a
// safety net for manifest changes that were written without an event.
func resync(ctx context.Context, events *checker.Events, every time.Duration) {
	mark := func() {
		if err := events.All(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [CHECKER] resync: %v\n", time.Now().Format("15:04:05"), err)
		}
	}
	mark()
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mark()
		}
	}
}

type natsNotifier struct{ nc *nats.Conn }

// RepoChanged implements indexer.Notifier.
func (n natsNotifier) RepoChanged(_ context.Context, p buildrepo.Prpa) error {
	data, err := indexer.EncodeEvent(p)
	if err != nil {
		return err
	}
	return n.nc.Publish(natsqueue.SubjectEventsRepoChanged, data)
}

// subscribeJobFinished consumes worker status messages. Integration can take
// a while, so messages are handled concurrently up to a small bound.
func subscribeJobFinished(ctx context.Context, nc *nats.Conn, h *jobstatus.Handler) {
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	_, _ = nc.Subscribe(natsqueue.SubjectJobStatus, func(msg *nats.Msg) {
		data := msg.Data
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := h.Handle(ctx, data); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] [STATE] %v\n", time.Now().Format("15:04:05"), err)
			}
		}()
	})
}

func subscribeRepoChanged(ctx context.Context, nc *nats.Conn, events *checker.Events) {
	_, _ = nc.Subscribe(natsqueue.SubjectEventsRepoChanged, func(msg *nats.Msg) {
		p, err := indexer.DecodeEvent(msg.Data)
		if err != nil {
			return
		}
		if err := events.BinariesIntegrated(ctx, p); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [CHECKER] %v\n", time.Now().Format("15:04:05"), err)
		}
	})
}

// subscribeTriggers maps build triggers onto the checker. A trigger never
// creates a job itself: it marks the project's prpas dirty, and a forced
// trigger (bsgo package rebuild) first records the force request the checker
// honours.
func subscribeTriggers(ctx context.Context, nc *nats.Conn, store storage.Store, events *checker.Events) {
	_, _ = nc.Subscribe(natsqueue.SubjectTriggersPattern, func(msg *nats.Msg) {
		var trig v1.BuildTrigger
		if err := json.Unmarshal(msg.Data, &trig); err != nil || trig.Project == "" {
			return
		}
		if trig.ForceRebuild {
			pkg, flavor, _ := strings.Cut(trig.Package, ":")
			err := store.RequestForcedRebuild(ctx, storage.ForcedRebuild{
				Project: trig.Project, Repo: trig.Target, Arch: trig.Architecture, Package: pkg, Flavor: flavor,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] [SCHEDULER] force rebuild of %s/%s: %v\n",
					time.Now().Format("15:04:05"), trig.Project, trig.Package, err)
				return
			}
		}
		logf("SCHEDULER", "trigger for %s/%s (reason %s, force %t)", trig.Project, trig.Package, trig.Reason, trig.ForceRebuild)
		if err := events.PackageChanged(ctx, trig.Project); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [SCHEDULER] %v\n", time.Now().Format("15:04:05"), err)
		}
	})
}
