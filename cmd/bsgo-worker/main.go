package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/worker"
)

func main() {
	var (
		workerID string
		arch     string
		natsURL  string
		s3Bucket string
		workDir  string
	)

	flag.StringVar(&workerID, "worker-id", "bsgo-worker-01", "Unique worker host identifier")
	flag.StringVar(&arch, "arch", "x86_64", "Host architecture (x86_64, aarch64, s390x, ppc64le)")
	flag.StringVar(&natsURL, "nats-url", "nats://localhost:4222", "NATS JetStream connection URL")
	flag.StringVar(&s3Bucket, "s3-bucket", "bsgo-artifacts", "S3 bucket for artifact upload")
	flag.StringVar(&workDir, "work-dir", "/tmp/bsgo-worker", "Root workspace directory for build jobs")
	flag.Parse()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS (%s): %v\n", natsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	cfg := worker.WorkerConfig{
		WorkerID:     workerID,
		Architecture: arch,
		NatsURL:      natsURL,
		S3Bucket:     s3Bucket,
		WorkDir:      workDir,
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      8,
			TotalMemoryMB:   16384,
			AvailableDiskGB: 50,
			SandboxType:     "podman",
		},
	}

	sandbox := &worker.ProcessSandbox{WorkDir: workDir}
	daemon := worker.NewWorkerDaemon(cfg, nc, sandbox)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting bsgo-worker [%s] (Arch: %s) connecting to %s...\n", workerID, arch, natsURL)
	if err := daemon.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
		os.Exit(1)
	}
}
