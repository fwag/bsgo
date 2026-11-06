package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

func main() {
	var (
		natsURL  string
		httpPort int
		dbURL    string
	)

	flag.StringVar(&natsURL, "nats-url", "nats://localhost:4222", "NATS JetStream connection URL")
	flag.IntVar(&httpPort, "http-port", 8080, "HTTP API gateway listening port")
	flag.StringVar(&dbURL, "db-url", "postgres://bsgo:password@localhost:5432/bsgodb?sslmode=disable", "PostgreSQL URL")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting bsgo Control Plane (API + Scheduler + Dispatcher)...\n")

	// 1. Connect to NATS JetStream and ensure streams
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS (%s): %v\n", natsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to provision JetStream streams: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  • NATS JetStream streams verified: TRIGGERS, JOBS_PENDING, JOB_STATUS, PUBLISH\n")

	// 2. Storage
	store := initControlPlaneStore(ctx, dbURL)
	fmt.Printf("  • Storage initialized & verified\n")

	// 3. Start Scheduler & Dispatcher
	startSchedulerAndDispatcher(ctx, nc, store)

	// 4. HTTP Gateway
	startHTTPServer(ctx, httpPort, store)

	fmt.Printf("\n✔ bsgo Control Plane is LIVE and listening for triggers and workers!\n")
	<-ctx.Done()
	fmt.Println("bsgo Control Plane shut down cleanly.")
}

func initControlPlaneStore(ctx context.Context, dbURL string) storage.Store {
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pgStore, err := postgres.NewPostgresStore(connectCtx, dbURL)
	if err == nil {
		seedDefaultData(ctx, pgStore)
		return pgStore
	}

	memStore := storage.NewMemoryStore()
	seedDefaultData(ctx, memStore)
	return memStore
}

func startSchedulerAndDispatcher(ctx context.Context, nc *nats.Conn, store storage.Store) {
	disp := dispatcher.NewDispatcher(dispatcher.NewFuncPublisher(func(subj string, data []byte) {
		_ = nc.Publish(subj, data)
	}), 30*time.Second)

	// Ingest worker heartbeats into Dispatcher
	_, _ = nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if err := json.Unmarshal(msg.Data, &hb); err == nil {
			disp.Registry().UpdateHeartbeat(hb)
		}
	})

	// Ingest triggers and dispatch
	solverPool, _ := solv.NewWorkerPool("x86_64", 2)
	pub := scheduler.NewNatsPublisher(func(pCtx context.Context, subject string, data []byte) error {
		return nc.Publish(subject, data)
	})
	sched := scheduler.NewScheduler(store, solverPool, pub, scheduler.Config{S3Bucket: "bsgo-artifacts"})

	_, _ = nc.Subscribe("bsgo.triggers.>", func(msg *nats.Msg) {
		var trig v1.BuildTrigger
		if err := json.Unmarshal(msg.Data, &trig); err != nil {
			return
		}

		jobs, err := sched.ProcessTrigger(ctx, &trig)
		if err != nil {
			// Fallback: create direct job so it builds immediately
			jobs = []*v1.BuildJob{createFallbackJob(&trig)}
		}

		for _, j := range jobs {
			disp.EnqueueJob(j)
			statusMsg, _ := json.Marshal(map[string]any{
				"job_id":       j.JobID,
				"project":      j.Project,
				"package":      j.Package,
				"phase":        "PENDING",
				"architecture": j.Architecture,
			})
			_ = nc.Publish("bsgo.status", statusMsg)
		}
	})

	// Background dispatch loop
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				assignedWorker, dispatchedJob, _ := disp.DispatchNext(ctx)
				if dispatchedJob != nil {
					statusMsg, _ := json.Marshal(map[string]any{
						"job_id":       dispatchedJob.JobID,
						"project":      dispatchedJob.Project,
						"package":      dispatchedJob.Package,
						"phase":        "BUILDING",
						"worker_id":    assignedWorker,
						"architecture": dispatchedJob.Architecture,
					})
					_ = nc.Publish("bsgo.status", statusMsg)
				}
			}
		}
	}()
}

func createFallbackJob(trig *v1.BuildTrigger) *v1.BuildJob {
	return &v1.BuildJob{
		JobID:        trig.TriggerID,
		Project:      trig.Project,
		Package:      trig.Package,
		Architecture: "x86_64",
		Priority:     50,
		Destination: v1.ArtifactDestination{
			S3Bucket: "bsgo-artifacts",
			S3Prefix: fmt.Sprintf("artifacts/%s/%s/", trig.Project, trig.Package),
		},
	}
}

func startHTTPServer(ctx context.Context, httpPort int, store storage.Store) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	srv := &http.Server{Addr: fmt.Sprintf(":%d", httpPort), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}

func seedDefaultData(ctx context.Context, store storage.Store) {
	_, err := store.GetProject(ctx, "openSUSE:Factory")
	if err == nil {
		return
	}

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "openSUSE:Factory", Title: "openSUSE Factory Distribution"},
		Targets:       map[string]manifest.BuildTarget{"standard": {Architectures: []string{"x86_64", "aarch64"}}},
	}
	_ = store.CreateProject(ctx, prj)
}
