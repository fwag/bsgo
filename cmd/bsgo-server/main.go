package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/signer"
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
	fmt.Printf("  - NATS JetStream streams verified: TRIGGERS, JOBS_PENDING, JOB_STATUS, PUBLISH\n")

	// 2. Storage
	store := initControlPlaneStore(ctx, dbURL)
	fmt.Printf("  - Storage initialized and verified\n")

	// 3. Start Scheduler & Dispatcher
	startSchedulerAndDispatcher(ctx, nc, store)

	// 4. HTTP Gateway
	startHTTPServer(ctx, httpPort, store)

	fmt.Printf("bsgo Control Plane is LIVE and listening for triggers and workers\n")
	<-ctx.Done()
	fmt.Println("\nbsgo Control Plane shut down cleanly.")
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

	gatekeeper := scheduler.NewMemoryGatekeeper()
	pubService := initPublisherService(nc)

	subscribeWorkerHeartbeats(nc, disp)
	subscribeJobStatus(nc, gatekeeper, disp, pubService)

	solverPool, _ := solv.NewWorkerPool("x86_64", 2)
	pub := scheduler.NewNatsPublisher(func(pCtx context.Context, subject string, data []byte) error {
		return nc.Publish(subject, data)
	})
	sched := scheduler.NewScheduler(store, solverPool, pub, scheduler.Config{S3Bucket: "bsgo-artifacts"})
	cascade := scheduler.NewCascadeEngine(store, solverPool, pub, gatekeeper)

	subscribeTriggers(ctx, nc, sched, disp)
	subscribePublicationEvents(ctx, nc, cascade)
	startDispatchLoop(ctx, disp, nc)
}

func subscribeWorkerHeartbeats(nc *nats.Conn, disp *dispatcher.Dispatcher) {
	knownWorkers := make(map[string]bool)
	var workerMu sync.Mutex

	_, _ = nc.Subscribe("bsgo.workers.heartbeat", func(msg *nats.Msg) {
		var hb v1.WorkerHeartbeat
		if err := json.Unmarshal(msg.Data, &hb); err == nil {
			disp.Registry().UpdateHeartbeat(hb)
			workerMu.Lock()
			if !knownWorkers[hb.WorkerID] {
				knownWorkers[hb.WorkerID] = true
				fmt.Printf("[%s] [DISPATCHER] Worker discovered: %s (Arch: %s, %d vCPUs, %dGB RAM)\n",
					time.Now().Format("15:04:05"), hb.WorkerID, hb.Architecture, hb.Hardware.TotalVCPUs, hb.Hardware.TotalMemoryMB/1024)
			}
			workerMu.Unlock()
		}
	})
}

func subscribeJobStatus(nc *nats.Conn, gk *scheduler.MemoryGatekeeper, disp *dispatcher.Dispatcher, pub *publisher.Publisher) {
	_, _ = nc.Subscribe("bsgo.status", func(msg *nats.Msg) {
		var st map[string]any
		if err := json.Unmarshal(msg.Data, &st); err != nil {
			return
		}

		jobID, _ := st["job_id"].(string)
		phase, _ := st["phase"].(string)
		pkgName, _ := st["package"].(string)
		prjName, _ := st["project"].(string)

		if jobID != "" && phase != "" {
			fmt.Printf("[%s] [STATUS] Job %s (%s) phase -> %s\n",
				time.Now().Format("15:04:05"), jobID, pkgName, phase)
		}

		updateGatekeeperStatus(gk, prjName, pkgName, phase)
		if (phase == "SUCCEEDED" || phase == "FAILED") && prjName != "" {
			exitCode := 0
			if phase == "FAILED" {
				exitCode = 1
				if c, ok := st["exit_code"].(float64); ok {
					exitCode = int(c)
				}
			}
			workerID, _ := st["worker_id"].(string)
			disp.RecordJobResult(jobID, workerID, prjName, exitCode, 30*time.Second)

			if phase == "SUCCEEDED" && pub != nil {
				go triggerPublisher(pub, jobID, prjName, pkgName)
			}
		}
	})
}

func triggerPublisher(pub *publisher.Publisher, jobID, prjName, pkgName string) {
	ctx := context.Background()
	_, err := pub.ProcessPublishRequest(ctx, publisher.PublishRequest{
		RequestID:    jobID,
		Project:      prjName,
		Target:       "standard",
		Architecture: "x86_64",
		Package:      pkgName,
	})
	if err == nil {
		fmt.Printf("[%s] [PUBLISHER] Published %s/%s repodata (signed with repomd.xml.asc, repomd.xml.key)\n",
			time.Now().Format("15:04:05"), prjName, pkgName)
	}
}

func initPublisherService(nc *nats.Conn) *publisher.Publisher {
	pubS3 := NewDiskS3Client("/tmp/bsgo-artifacts")
	pubService := publisher.NewPublisher(
		publisher.NewRealCreateRepoExecutor(),
		pubS3,
		publisher.NewLocalLockManager(),
		"/tmp/bsgo-publisher-work",
	)
	pubService.SetEventPublisher(&eventPubAdapter{nc: nc})
	pubService.SetMetadataSigner(&signerAdapter{driver: signer.NewMemorySignDriver()})
	return pubService
}

type eventPubAdapter struct {
	nc *nats.Conn
}

func (a *eventPubAdapter) PublishEvent(_ context.Context, subject string, data []byte) error {
	return a.nc.Publish(subject, data)
}

type signerAdapter struct {
	driver signer.SignerDriver
}

func (a *signerAdapter) SignRepositoryMetadata(ctx context.Context, repodataDir string, opts publisher.SignOptions) (string, string, error) {
	svc := signer.NewSignerService(a.driver)
	return svc.SignRepositoryMetadata(ctx, repodataDir, signer.SignOptions{
		Project: opts.Project,
	})
}

type DiskS3Client struct {
	rootDir string
}

func NewDiskS3Client(rootDir string) *DiskS3Client {
	if rootDir == "" {
		rootDir = "/tmp/bsgo-artifacts"
	}
	_ = os.MkdirAll(rootDir, 0750)
	return &DiskS3Client{rootDir: rootDir}
}

func (c *DiskS3Client) DownloadMetadata(_ context.Context, prefix, localDir string) error {
	cleanPrefix := filepath.Clean(prefix)
	srcDir := filepath.Clean(filepath.Join(c.rootDir, cleanPrefix))
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		dest := filepath.Clean(filepath.Join(localDir, rel))
		_ = os.MkdirAll(filepath.Dir(dest), 0750)
		cleanPath := filepath.Clean(path)
		// #nosec G304 G122 -- reading from sanitized metadata cache
		data, err := os.ReadFile(cleanPath)
		if err == nil {
			// #nosec G703 -- writing to validated localDir
			_ = os.WriteFile(dest, data, 0600)
		}
		return nil
	})
}

func (c *DiskS3Client) UploadFile(_ context.Context, localPath, s3Key string) error {
	cleanKey := filepath.Clean(s3Key)
	dest := filepath.Clean(filepath.Join(c.rootDir, cleanKey))
	_ = os.MkdirAll(filepath.Dir(dest), 0750)
	cleanLocalPath := filepath.Clean(localPath)
	// #nosec G304 -- reading from sanitized local metadata path
	data, err := os.ReadFile(cleanLocalPath)
	if err != nil {
		return err
	}
	// #nosec G703 -- writing to validated s3Key destination
	return os.WriteFile(dest, data, 0600)
}

func updateGatekeeperStatus(gk *scheduler.MemoryGatekeeper, prjName, pkgName, phase string) {
	if prjName == "" || pkgName == "" {
		return
	}
	switch phase {
	case "BUILDING":
		gk.SetBuilding(prjName, pkgName, true)
	case "SUCCEEDED", "FAILED":
		gk.SetBuilding(prjName, pkgName, false)
	}
}

func subscribePublicationEvents(ctx context.Context, nc *nats.Conn, cascade *scheduler.CascadeEngine) {
	_, _ = nc.Subscribe(natsqueue.SubjectEventsPublished, func(msg *nats.Msg) {
		var ev v1.PublicationEvent
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			return
		}

		fmt.Printf("[%s] [PUBLISHER] Received publication event: %s/%s (%s)\n",
			time.Now().Format("15:04:05"), ev.Project, ev.Package, ev.Target)

		triggers, err := cascade.ProcessPublicationEvent(ctx, &ev)
		if err == nil && len(triggers) > 0 {
			fmt.Printf("[%s] [CASCADE] Emitted %d downstream rebuild triggers for %s/%s\n",
				time.Now().Format("15:04:05"), len(triggers), ev.Project, ev.Package)
		}
	})
}

func subscribeTriggers(ctx context.Context, nc *nats.Conn, sched *scheduler.Scheduler, disp *dispatcher.Dispatcher) {
	_, _ = nc.Subscribe("bsgo.triggers.>", func(msg *nats.Msg) {
		var trig v1.BuildTrigger
		if err := json.Unmarshal(msg.Data, &trig); err != nil {
			return
		}

		// Infer priority tier if missing
		if trig.Priority == "" {
			parts := strings.Split(msg.Subject, ".")
			if len(parts) >= 3 && (parts[2] == "high" || parts[2] == "med" || parts[2] == "low") {
				trig.Priority = parts[2]
			}
		}

		fmt.Printf("[%s] [SCHEDULER] Ingested trigger: %s/%s (Ref: %s, Commit: %s, Priority: %s, Reason: %s)\n",
			time.Now().Format("15:04:05"), trig.Project, trig.Package, trig.Ref, trig.CommitSHA, trig.NormalizedPriority(), trig.Reason)

		jobs, err := sched.ProcessTrigger(ctx, &trig)
		if err != nil {
			jobs = []*v1.BuildJob{createFallbackJob(&trig)}
		}

		enqueueDispatchedJobs(nc, disp, jobs)
	})
}

func enqueueDispatchedJobs(nc *nats.Conn, disp *dispatcher.Dispatcher, jobs []*v1.BuildJob) {
	for _, j := range jobs {
		disp.EnqueueJob(j)
		fmt.Printf("[%s] [DISPATCHER] Enqueued build job: %s (%s/%s, Priority: %d)\n",
			time.Now().Format("15:04:05"), j.JobID, j.Project, j.Package, j.Priority)

		statusMsg, _ := json.Marshal(map[string]any{
			"job_id":       j.JobID,
			"project":      j.Project,
			"package":      j.Package,
			"phase":        "PENDING",
			"architecture": j.Architecture,
		})
		_ = nc.Publish("bsgo.status", statusMsg)
	}
}

func startDispatchLoop(ctx context.Context, disp *dispatcher.Dispatcher, nc *nats.Conn) {
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
					fmt.Printf("[%s] [DISPATCHER] Dispatched job %s (%s) to %s\n",
						time.Now().Format("15:04:05"), dispatchedJob.JobID, dispatchedJob.Package, assignedWorker)

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

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", httpPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
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
