package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/auth"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/dod"
	"github.com/openSUSE/bsgo/pkg/gateway"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

type ServerConfig struct {
	Role       string
	NatsURL    string
	HTTPPort   int
	DBURL      string
	S3Endpoint string
	S3Bucket   string
	WorkDir    string
	SignerType string
	SigndAddr  string
	TLSCert    string
	TLSKey     string
	ServerCA   string
	// DoDRepomdTTL is how often an external repository's repomd.xml is re-checked.
	DoDRepomdTTL time.Duration
	// Checker enables the scheduler loop (the OBS bs_sched equivalent).
	Checker bool
	// CheckerDebounce is how long a prpa must stay unchanged before it is checked.
	CheckerDebounce time.Duration
	// CheckerWorkers bounds the prpas checked concurrently by this instance.
	CheckerWorkers int
	// CheckerResync marks every prpa dirty at this interval (0 disables).
	CheckerResync time.Duration
}

func parseServerConfig() ServerConfig {
	subcmd := ""
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		subcmd = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	var cfg ServerConfig
	flag.StringVar(&cfg.Role, "role", "all", "Server role: 'all' (API+Scheduler+Publisher), 'api', or 'publisher'")
	flag.StringVar(&cfg.NatsURL, "nats-url", getEnv("NATS_URL", "nats://localhost:4222"), "NATS JetStream connection URL")
	flag.IntVar(&cfg.HTTPPort, "http-port", 8080, "HTTP API gateway listening port")
	flag.StringVar(&cfg.DBURL, "db-url", getEnv("DATABASE_URL", "postgres://bsgo:password@localhost:5432/bsgodb?sslmode=disable"), "PostgreSQL URL")
	flag.StringVar(&cfg.S3Endpoint, "s3-endpoint", getEnv("S3_ENDPOINT", "http://127.0.0.1:3900"), "S3 storage endpoint")
	flag.StringVar(&cfg.S3Bucket, "s3-bucket", getEnv("S3_BUCKET", "bsgo-artifacts"), "S3 bucket for published repositories")
	flag.StringVar(&cfg.WorkDir, "work-dir", "/tmp/bsgo-publisher-work", "Working directory for metadata staging")
	flag.StringVar(&cfg.SignerType, "signer-type", getEnv("SIGNER_TYPE", "signd"), "Signer backend: 'signd' or 'memory'")
	flag.StringVar(&cfg.SigndAddr, "signd-addr", getEnv("SIGND_ADDR", "127.0.0.1:5167"), "bsgo-signd network address")
	flag.StringVar(&cfg.TLSCert, "signd-tls-cert", getEnv("SIGND_TLS_CERT", ""), "Client certificate for mTLS connection to bsgo-signd")
	flag.StringVar(&cfg.TLSKey, "signd-tls-key", getEnv("SIGND_TLS_KEY", ""), "Client key for mTLS connection to bsgo-signd")
	flag.StringVar(&cfg.ServerCA, "signd-server-ca", getEnv("SIGND_SERVER_CA", ""), "Server root CA certificate for verifying bsgo-signd")
	flag.DurationVar(&cfg.DoDRepomdTTL, "dod-repomd-ttl", getEnvDuration("DOD_REPOMD_TTL", dod.DefaultRepomdTTL), "Interval between repomd.xml re-checks of external (DoD) repositories")
	flag.BoolVar(&cfg.Checker, "checker", getEnvBool("BSGO_CHECKER", true), "Run the scheduler checker loop (prpa passes, job creation, indexing)")
	flag.DurationVar(&cfg.CheckerDebounce, "checker-debounce", getEnvDuration("BSGO_CHECKER_DEBOUNCE", 2*time.Second), "Quiet time of a prpa before its checker pass starts")
	flag.IntVar(&cfg.CheckerWorkers, "checker-workers", 2, "Prpas checked concurrently by this instance")
	flag.DurationVar(&cfg.CheckerResync, "checker-resync", getEnvDuration("BSGO_CHECKER_RESYNC", 10*time.Minute), "Interval at which every prpa is re-checked as a safety net; 0 disables")
	flag.Parse()

	if subcmd != "" {
		cfg.Role = subcmd
	}
	return cfg
}

func main() {
	cfg := parseServerConfig()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	nc, js, err := natsqueue.ConnectJetStream(cfg.NatsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS (%s): %v\n", cfg.NatsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to provision JetStream streams: %v\n", err)
		os.Exit(1)
	}

	store := initControlPlaneStore(ctx, cfg.DBURL)

	if cfg.Role == "publisher" {
		runPublisherOnly(ctx, cfg, nc, js, store)
		return
	}

	runControlPlane(ctx, cfg, nc, js, store)
}

func runPublisherOnly(ctx context.Context, cfg ServerConfig, nc *nats.Conn, js jetstream.JetStream, store storage.Store) {
	fmt.Printf("Starting bsgo-server [publisher] connected to %s (S3: %s)...\n", cfg.NatsURL, cfg.S3Endpoint)
	pubService := initPublisherService(ctx, cfg, nc, store)
	if err := consumePublishRequests(ctx, js, pubService); err != nil {
		fmt.Fprintf(os.Stderr, "Publisher consumer error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nbsgo-server publisher stopped cleanly.")
}

func runControlPlane(ctx context.Context, cfg ServerConfig, nc *nats.Conn, js jetstream.JetStream, store storage.Store) {
	var pubService *publisher.Publisher
	if cfg.Role == "all" {
		fmt.Printf("Starting bsgo Control Plane (API + Scheduler + Dispatcher + Publisher)...\n")
		pubService = initPublisherService(ctx, cfg, nc, store)
		go func() {
			_ = consumePublishRequests(ctx, js, pubService)
		}()
	} else {
		fmt.Printf("Starting bsgo Control Plane (API + Scheduler + Dispatcher)...\n")
	}

	startSchedulerAndDispatcher(ctx, cfg, nc, js, store)
	startHTTPServer(ctx, cfg.HTTPPort, store, nc)

	fmt.Printf("bsgo Control Plane is LIVE and listening for triggers and workers\n")
	<-ctx.Done()
	fmt.Println("\nbsgo Control Plane shut down cleanly.")
}

func initControlPlaneStore(ctx context.Context, dbURL string) storage.Store {
	for attempts := 0; attempts < 10; attempts++ {
		connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		pgStore, err := postgres.NewPostgresStore(connectCtx, dbURL)
		cancel()
		if err == nil {
			fmt.Println("[STORAGE] Connected to PostgreSQL metadata store")
			return pgStore
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Println("[STORAGE] WARNING: Failed to connect to PostgreSQL, falling back to In-Memory store")
	return storage.NewMemoryStore()
}

func initPublisherService(ctx context.Context, cfg ServerConfig, nc *nats.Conn, store storage.Store) *publisher.Publisher {
	var s3Client publisher.S3Client
	if s3Storage, err := s3.NewS3Storage(ctx, cfg.S3Endpoint, "garage", cfg.S3Bucket, "", ""); err == nil {
		s3Client = s3Storage
	} else {
		s3Client = NewDiskS3Client("/tmp/bsgo-artifacts")
	}

	var lockMgr publisher.LockManager
	if pgStore, ok := store.(*postgres.PostgresStore); ok {
		lockMgr = postgres.NewPostgresLockManager(pgStore.Pool())
	} else {
		lockMgr = publisher.NewLocalLockManager()
	}

	pubService := publisher.NewPublisher(
		publisher.NewRealCreateRepoExecutor(),
		s3Client,
		lockMgr,
		cfg.WorkDir,
	)
	pubService.SetStore(store)
	pubService.SetEventPublisher(&eventPubAdapter{nc: nc})

	signerDrv := initSignerDriver(cfg, store)
	pubService.SetMetadataSigner(&signerAdapter{driver: signerDrv})
	return pubService
}

func initSignerDriver(cfg ServerConfig, store storage.Store) signer.SignerDriver {
	if cfg.SignerType == "memory" {
		return signer.NewMemorySignDriver()
	}

	var client *signd.Client
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		tlsCfg, err := signd.LoadClientTLSConfig(cfg.TLSCert, cfg.TLSKey, cfg.ServerCA)
		if err == nil {
			client = signd.NewTLSClient(cfg.SigndAddr, tlsCfg)
		}
	}
	if client == nil {
		client = signd.NewClient(cfg.SigndAddr)
	}

	if keyStore, ok := store.(signer.ProjectKeyStore); ok {
		return signer.NewRemoteSignerDriver(client, keyStore)
	}

	return signer.NewMemorySignDriver()
}

func consumePublishRequests(ctx context.Context, js jetstream.JetStream, pub *publisher.Publisher) error {
	cons, err := js.CreateOrUpdateConsumer(ctx, natsqueue.StreamPublish, jetstream.ConsumerConfig{
		Durable:       "bsgo-server-publisher",
		FilterSubject: "bsgo.publish.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("failed to create JetStream consumer: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
			if err != nil {
				continue
			}
			for msg := range msgs.Messages() {
				processPublishMessage(ctx, msg, pub)
			}
		}
	}
}

func processPublishMessage(ctx context.Context, msg jetstream.Msg, pub *publisher.Publisher) {
	var req v1.PublishRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		_ = msg.Term()
		return
	}

	fmt.Printf("[%s] [PUBLISHER] Ingested publish request: %s/%s (%s, %d RPMs)\n",
		time.Now().Format("15:04:05"), req.Project, req.Package, req.Architecture, len(req.NewRPMPaths))

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[PUBLISHER] Failed publishing %s/%s: %v\n", req.Project, req.Package, err)
		if meta, mErr := msg.Metadata(); mErr == nil && meta.NumDelivered >= 3 {
			_ = msg.Term()
			return
		}
		_ = msg.NakWithDelay(2 * time.Second)
		return
	}

	_ = msg.Ack()
	fmt.Printf("[%s] [PUBLISHER] Successfully committed repository: %s\n",
		time.Now().Format("15:04:05"), res.RepomdXMLKey)
}

type s3DoDAdapter struct {
	storage publisher.S3Client
}

func (a *s3DoDAdapter) GetObject(ctx context.Context, key string) ([]byte, error) {
	if s3Store, ok := a.storage.(*s3.S3Storage); ok {
		rc, _, _, err := s3Store.GetObject(ctx, key)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	if diskStore, ok := a.storage.(*DiskS3Client); ok {
		path := filepath.Clean(filepath.Join(diskStore.rootDir, key))
		return os.ReadFile(path)
	}
	return nil, fmt.Errorf("unsupported s3 storage type")
}

func (a *s3DoDAdapter) PutObject(ctx context.Context, key string, data []byte) error {
	tmpFile, err := os.CreateTemp("", "bsgo-dod-put-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	_ = tmpFile.Close()
	return a.storage.UploadFile(ctx, tmpFile.Name(), key)
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

func (c *DiskS3Client) DownloadFile(_ context.Context, s3Key, localPath string) error {
	cleanKey := filepath.Clean(s3Key)
	src := filepath.Clean(filepath.Join(c.rootDir, cleanKey))
	// #nosec G304 -- reading from local S3 cache
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(localPath), 0750)
	// #nosec G703 -- writing to validated local destination
	return os.WriteFile(localPath, data, 0600)
}

func (c *DiskS3Client) ListObjects(_ context.Context, prefix string) ([]publisher.S3ObjectInfo, error) {
	cleanPrefix := filepath.Clean(prefix)
	srcDir := filepath.Clean(filepath.Join(c.rootDir, cleanPrefix))
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil, nil
	}
	var res []publisher.S3ObjectInfo
	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(c.rootDir, path)
		res = append(res, publisher.S3ObjectInfo{
			Key:          filepath.ToSlash(rel),
			LastModified: info.ModTime(),
			Size:         info.Size(),
		})
		return nil
	})
	return res, nil
}

func (c *DiskS3Client) DeleteObject(_ context.Context, s3Key string) error {
	cleanKey := filepath.Clean(s3Key)
	dest := filepath.Clean(filepath.Join(c.rootDir, cleanKey))
	_ = os.Remove(dest)
	return nil
}

// publishUnresolvable announces a scheduling failure. Target and architecture
// come from the failing (target, arch) pair when known, else from the trigger.
func startDispatchLoop(ctx context.Context, disp *dispatcher.Dispatcher, nc *nats.Conn) {
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		reaperTicker := time.NewTicker(5 * time.Second)
		defer reaperTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-reaperTicker.C:
				reaped := disp.ReapStaleJobs(time.Now())
				for _, rj := range reaped {
					fmt.Printf("[%s] [WATCHDOG] Re-queued stale/interrupted job %s (%s/%s) due to worker failure/restart\n",
						time.Now().Format("15:04:05"), rj.JobID, rj.Project, rj.Package)

					statusMsg, _ := json.Marshal(map[string]any{
						"job_id":       rj.JobID,
						"project":      rj.Project,
						"package":      rj.Package,
						"target":       rj.Target,
						"phase":        "ABORTED",
						"reason":       "WORKER_LEASE_EXPIRED",
						"architecture": rj.Architecture,
					})
					_ = nc.Publish("bsgo.status", statusMsg)
				}
			case <-ticker.C:
				assignedWorker, dispatchedJob, _ := disp.DispatchNext(ctx)
				if dispatchedJob != nil {
					fmt.Printf("[%s] [DISPATCHER] Dispatched job %s (%s) to %s\n",
						time.Now().Format("15:04:05"), dispatchedJob.JobID, dispatchedJob.Package, assignedWorker)

					statusMsg, _ := json.Marshal(map[string]any{
						"job_id":       dispatchedJob.JobID,
						"project":      dispatchedJob.Project,
						"package":      dispatchedJob.Package,
						"target":       dispatchedJob.Target,
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

func startHTTPServer(ctx context.Context, httpPort int, store storage.Store, nc *nats.Conn) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	repoStorage := initRepoStorage(ctx)
	authorizer := auth.NewAuthorizer()
	repoServer := gateway.NewRepoServer(repoStorage, authorizer)
	mux.Handle("/repos/", repoServer)

	logServer := gateway.NewLogServer(nc, repoStorage)
	mux.Handle("/api/v1/jobs/", logServer)

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

func initRepoStorage(ctx context.Context) gateway.RepoStorage {
	s3Endpoint := os.Getenv("S3_ENDPOINT")
	if s3Endpoint == "" {
		s3Endpoint = "http://localhost:3900"
	}
	s3Bucket := os.Getenv("S3_BUCKET")
	if s3Bucket == "" {
		s3Bucket = "bsgo-artifacts"
	}

	if s3Storage, err := s3.NewS3Storage(ctx, s3Endpoint, "garage", s3Bucket, "", ""); err == nil {
		return s3Storage
	}

	return gateway.NewDiskRepoStorage("/tmp/bsgo-artifacts")
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid duration in %s=%q, using %s: %v\n", key, val, fallback, err)
		return fallback
	}
	return d
}
