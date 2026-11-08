package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

type Config struct {
	NatsURL    string
	S3Endpoint string
	S3Bucket   string
	DBURL      string
	WorkDir    string
}

func main() {
	var cfg Config
	flag.StringVar(&cfg.NatsURL, "nats-url", getEnv("NATS_URL", "nats://localhost:4222"), "NATS JetStream connection URL")
	flag.StringVar(&cfg.S3Endpoint, "s3-endpoint", getEnv("S3_ENDPOINT", "http://127.0.0.1:3900"), "S3 storage endpoint")
	flag.StringVar(&cfg.S3Bucket, "s3-bucket", getEnv("S3_BUCKET", "bsgo-artifacts"), "S3 bucket for published repositories")
	flag.StringVar(&cfg.DBURL, "db-url", getEnv("DATABASE_URL", "postgres://bsgo:bsgo@localhost:5432/bsgodb?sslmode=disable"), "PostgreSQL connection string for advisory locking")
	flag.StringVar(&cfg.WorkDir, "work-dir", "/tmp/bsgo-publisher", "Working directory for metadata staging")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting bsgo-publisher daemon connecting to %s (S3: %s)...\n", cfg.NatsURL, cfg.S3Endpoint)
	if err := runDaemon(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Publisher daemon error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nbsgo-publisher stopped cleanly.")
}

func runDaemon(ctx context.Context, cfg Config) error {
	nc, js, err := natsqueue.ConnectJetStream(cfg.NatsURL)
	if err != nil {
		return fmt.Errorf("failed connecting to NATS (%s): %w", cfg.NatsURL, err)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		return fmt.Errorf("failed setting up JetStream streams: %w", err)
	}

	pub, err := initPublisher(ctx, cfg, nc)
	if err != nil {
		return err
	}

	return consumePublishRequests(ctx, js, pub)
}

func initPublisher(ctx context.Context, cfg Config, nc *nats.Conn) (*publisher.Publisher, error) {
	s3Storage, err := s3.NewS3Storage(ctx, cfg.S3Endpoint, "garage", cfg.S3Bucket, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to S3 (%s): %w", cfg.S3Endpoint, err)
	}

	lockMgr := initLockManager(ctx, cfg.DBURL)
	cr := publisher.NewRealCreateRepoExecutor()

	pub := publisher.NewPublisher(cr, s3Storage, lockMgr, cfg.WorkDir)
	pub.SetEventPublisher(&eventPublisherAdapter{nc: nc})
	pub.SetMetadataSigner(&signerAdapter{driver: signer.NewMemorySignDriver()})

	return pub, nil
}

func initLockManager(ctx context.Context, dbURL string) publisher.LockManager {
	if dbURL != "" {
		connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if pool, err := pgxpool.New(connectCtx, dbURL); err == nil && pool.Ping(connectCtx) == nil {
			return postgres.NewPostgresLockManager(pool)
		}
	}
	return publisher.NewLocalLockManager()
}

func consumePublishRequests(ctx context.Context, js jetstream.JetStream, pub *publisher.Publisher) error {
	cons, err := js.CreateOrUpdateConsumer(ctx, natsqueue.StreamPublish, jetstream.ConsumerConfig{
		Durable:       "bsgo-publisher-daemon",
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
				processMessage(ctx, msg, pub)
			}
		}
	}
}

func processMessage(ctx context.Context, msg jetstream.Msg, pub *publisher.Publisher) {
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
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
	fmt.Printf("[%s] [PUBLISHER] Successfully committed repository: %s\n",
		time.Now().Format("15:04:05"), res.RepomdXMLKey)
}

type eventPublisherAdapter struct {
	nc *nats.Conn
}

func (a *eventPublisherAdapter) PublishEvent(_ context.Context, subject string, data []byte) error {
	return a.nc.Publish(subject, data)
}

type signerAdapter struct {
	driver signer.SignerDriver
}

func (a *signerAdapter) SignRepositoryMetadata(ctx context.Context, repodataDir string, opts publisher.SignOptions) (string, string, error) {
	return opts.Project, repodataDir, nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
