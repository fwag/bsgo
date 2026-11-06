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
		configFile string
		workerID   string
		arch       string
		natsURL    string
		s3Bucket   string
		workDir    string
	)

	flag.StringVar(&configFile, "config", "config/worker.yaml", "Path to YAML configuration file")
	flag.StringVar(&workerID, "worker-id", "", "Unique worker host identifier (overrides config)")
	flag.StringVar(&arch, "arch", "", "Host architecture (x86_64, aarch64, s390x, ppc64le)")
	flag.StringVar(&natsURL, "nats-url", "", "NATS JetStream connection URL (overrides config)")
	flag.StringVar(&s3Bucket, "s3-bucket", "", "S3 bucket for artifact upload (overrides config)")
	flag.StringVar(&workDir, "work-dir", "", "Root workspace directory for build jobs")
	flag.Parse()

	cfg := resolveWorkerConfig(configFile, workerID, arch, natsURL, s3Bucket, workDir)

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS (%s): %v\n", cfg.NatsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	sandbox := &worker.ProcessSandbox{
		WorkDir:     cfg.WorkDir,
		SandboxMode: cfg.SandboxMode,
	}
	daemon := worker.NewWorkerDaemon(cfg, nc, sandbox)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting bsgo-worker [%s] (Arch: %s, Sandbox: %s) connecting to %s...\n",
		cfg.WorkerID, cfg.Architecture, cfg.SandboxMode, cfg.NatsURL)
	if err := daemon.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nbsgo-worker stopped cleanly.")
}

func resolveWorkerConfig(configFile, flagID, flagArch, flagNats, flagBucket, flagWork string) worker.WorkerConfig {
	cfg := worker.WorkerConfig{
		WorkerID:     "worker-local-01",
		Architecture: "x86_64",
		NatsURL:      "nats://localhost:4222",
		S3Bucket:     "bsgo-artifacts",
		WorkDir:      "/tmp/bsgo-worker",
		SandboxMode:  "rpmbuild",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      8,
			TotalMemoryMB:   16384,
			AvailableDiskGB: 50,
			SandboxType:     "rpmbuild",
		},
	}

	if fileCfg, err := worker.LoadConfigFile(configFile); err == nil {
		applyFileConfig(&cfg, fileCfg)
	}

	applyFlagOverrides(&cfg, flagID, flagArch, flagNats, flagBucket, flagWork)
	return cfg
}

func applyFileConfig(cfg *worker.WorkerConfig, fileCfg *worker.WorkerConfigFile) {
	if fileCfg.Worker.ID != "" {
		cfg.WorkerID = fileCfg.Worker.ID
	}
	if fileCfg.Worker.Arch != "" {
		cfg.Architecture = fileCfg.Worker.Arch
	}
	if fileCfg.Worker.WorkDir != "" {
		cfg.WorkDir = fileCfg.Worker.WorkDir
	}
	if fileCfg.Nats.URL != "" {
		cfg.NatsURL = fileCfg.Nats.URL
	}
	if fileCfg.Storage.S3Bucket != "" {
		cfg.S3Bucket = fileCfg.Storage.S3Bucket
	}
	if fileCfg.Sandbox.Mode != "" {
		cfg.SandboxMode = fileCfg.Sandbox.Mode
		cfg.Hardware.SandboxType = fileCfg.Sandbox.Mode
	}
}

func applyFlagOverrides(cfg *worker.WorkerConfig, flagID, flagArch, flagNats, flagBucket, flagWork string) {
	if flagID != "" {
		cfg.WorkerID = flagID
	}
	if flagArch != "" {
		cfg.Architecture = flagArch
	}
	if flagNats != "" {
		cfg.NatsURL = flagNats
	}
	if flagBucket != "" {
		cfg.S3Bucket = flagBucket
	}
	if flagWork != "" {
		cfg.WorkDir = flagWork
	}
}
