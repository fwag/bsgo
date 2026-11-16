package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
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
		diskDir    string
		vcpus      int
		memoryMB   int64
	)

	flag.StringVar(&configFile, "config", "config/worker.yaml", "Path to YAML configuration file")
	flag.StringVar(&workerID, "worker-id", "", "Unique worker host identifier (overrides config)")
	flag.StringVar(&arch, "arch", "", "Host architecture (x86_64, aarch64, s390x, ppc64le)")
	flag.StringVar(&natsURL, "nats-url", "", "NATS JetStream connection URL (overrides config)")
	flag.StringVar(&s3Bucket, "s3-bucket", "", "S3 bucket for artifact upload (overrides config)")
	flag.StringVar(&workDir, "work-dir", "", "Root workspace directory for build jobs")
	flag.StringVar(&diskDir, "disk-dir", "", "Dedicated directory for VM disk block devices (overrides config)")
	flag.IntVar(&vcpus, "vcpus", 0, "Total vCPUs offered to builds (overrides detection and config)")
	flag.Int64Var(&memoryMB, "memory-mb", 0, "Total memory in MiB offered to builds (overrides detection and config)")
	flag.Parse()

	cfg := resolveWorkerConfig(configFile, workerID, arch, natsURL, s3Bucket, workDir, diskDir)
	if err := applyHardware(&cfg, vcpus, memoryMB); err != nil {
		fmt.Fprintf(os.Stderr, "Hardware detection failed: %v\n", err)
		os.Exit(1)
	}
	if err := worker.CheckSandboxMode(cfg.SandboxMode); err != nil {
		fmt.Fprintf(os.Stderr, "Sandbox check failed: %v\n", err)
		os.Exit(1)
	}
	if cfg.Architecture == "" {
		fmt.Fprintln(os.Stderr, "Worker architecture is required: set --arch or worker.arch in the config file")
		os.Exit(1)
	}

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS (%s): %v\n", cfg.NatsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	s3Storage, err := s3.NewS3Storage(ctx, cfg.S3Endpoint, "garage", cfg.S3Bucket, cfg.S3AccessKey, cfg.S3SecretKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to connect to S3 (%s): %v\n", cfg.S3Endpoint, err)
	}

	var store worker.ObjectStore
	if s3Storage != nil && err == nil {
		store = s3Storage
	}
	sandbox := newSandbox(cfg, store)
	if cfg.SandboxMode == "nspawn" {
		if sandbox.NspawnArgs, err = worker.PrepareNspawnArgs(cfg.NspawnArgs, cfg.WorkDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error: nspawn arguments: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("systemd-nspawn arguments: %s\n", strings.Join(sandbox.NspawnArgs, " "))
	}
	daemon := worker.NewWorkerDaemon(cfg, nc, sandbox)

	fmt.Printf("Starting bsgo-worker [%s] (Arch: %s, Sandbox: %s) connecting to %s...\n",
		cfg.WorkerID, cfg.Architecture, cfg.SandboxMode, cfg.NatsURL)
	if err := daemon.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nbsgo-worker stopped cleanly.")
}

func resolveWorkerConfig(configFile, flagID, flagArch, flagNats, flagBucket, flagWork, flagDisk string) worker.WorkerConfig {
	cfg := worker.WorkerConfig{
		WorkerID:    "worker-local-01",
		NatsURL:     "nats://localhost:4222",
		S3Endpoint:  "http://localhost:3900",
		S3Bucket:    "bsgo-artifacts",
		WorkDir:     "/tmp/bsgo-worker",
		SandboxMode: "rpmbuild",
		Hardware:    v1.WorkerHardware{SandboxType: "rpmbuild"},
	}

	if fileCfg, err := worker.LoadConfigFile(configFile); err == nil {
		applyFileConfig(&cfg, fileCfg)
	}

	applyFlagOverrides(&cfg, flagID, flagArch, flagNats, flagBucket, flagWork, flagDisk)
	return cfg
}

func applyFileConfig(cfg *worker.WorkerConfig, fileCfg *worker.WorkerConfigFile) {
	if fileCfg.Worker.ID != "" {
		cfg.WorkerID = fileCfg.Worker.ID
	}
	if fileCfg.Worker.Arch != "" {
		cfg.Architecture = fileCfg.Worker.Arch
	}
	if len(fileCfg.Worker.Archs) > 0 {
		cfg.Archs = fileCfg.Worker.Archs
	}
	if fileCfg.Worker.WorkDir != "" {
		cfg.WorkDir = fileCfg.Worker.WorkDir
	}
	if fileCfg.Worker.StateDir != "" {
		cfg.StateDir = fileCfg.Worker.StateDir
	}
	if fileCfg.Worker.CacheDir != "" {
		cfg.CacheDir = fileCfg.Worker.CacheDir
	}
	if fileCfg.Worker.DiskDir != "" {
		cfg.DiskDir = fileCfg.Worker.DiskDir
	}
	applyHardwareFileConfig(cfg, fileCfg)
	if fileCfg.Nats.URL != "" {
		cfg.NatsURL = fileCfg.Nats.URL
	}
	if fileCfg.Storage.S3Endpoint != "" {
		cfg.S3Endpoint = fileCfg.Storage.S3Endpoint
	}
	if fileCfg.Storage.S3Bucket != "" {
		cfg.S3Bucket = fileCfg.Storage.S3Bucket
	}
	if fileCfg.Storage.S3Region != "" {
		cfg.S3Region = fileCfg.Storage.S3Region
	}
	if fileCfg.Storage.AccessKey != "" {
		cfg.S3AccessKey = fileCfg.Storage.AccessKey
	}
	if fileCfg.Storage.SecretKey != "" {
		cfg.S3SecretKey = fileCfg.Storage.SecretKey
	}
	if fileCfg.Sandbox.Mode != "" {
		cfg.SandboxMode = fileCfg.Sandbox.Mode
		cfg.Hardware.SandboxType = fileCfg.Sandbox.Mode
	}
	cfg.AllowUnisolatedChroot = fileCfg.Sandbox.AllowUnisolatedChroot
	cfg.KeepFailedJobs = fileCfg.Sandbox.KeepFailedJobs
	cfg.NspawnArgs = fileCfg.Sandbox.NspawnArgs
	if fileCfg.Sandbox.ObsBuildBin != "" {
		cfg.ObsBuildBin = fileCfg.Sandbox.ObsBuildBin
	}
	if fileCfg.Sandbox.KVM.User != "" {
		cfg.KVMUser = fileCfg.Sandbox.KVM.User
	}
	if fileCfg.Sandbox.KVM.VMArgs != "" {
		cfg.KVMVMArgs = fileCfg.Sandbox.KVM.VMArgs
	}
	if fileCfg.Sandbox.KVM.AIO != "" {
		cfg.KVMAio = fileCfg.Sandbox.KVM.AIO
	}
	if fileCfg.Sandbox.KVM.CacheMode != "" {
		cfg.KVMCache = fileCfg.Sandbox.KVM.CacheMode
	}
}

func applyFlagOverrides(cfg *worker.WorkerConfig, flagID, flagArch, flagNats, flagBucket, flagWork, flagDisk string) {
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
	if flagDisk != "" {
		cfg.DiskDir = flagDisk
	}
}

func applyHardwareFileConfig(cfg *worker.WorkerConfig, fileCfg *worker.WorkerConfigFile) {
	cfg.HardwareOverrides.VCPUs = fileCfg.Worker.TotalVCPUs
	cfg.HardwareOverrides.MemoryMB = fileCfg.Worker.TotalMemoryMB
	cfg.HardwareOverrides.DiskGB = fileCfg.Worker.DiskGB
	cfg.HardwareOverrides.ReserveMemoryMB = fileCfg.Worker.ReserveMemoryMB
	cfg.HardwareOverrides.ReserveVCPUs = fileCfg.Worker.ReserveVCPUs
}

// applyHardware detects the host resources, applies the explicit overrides
// (config file, then flags) and logs detected and effective values once.
func applyHardware(cfg *worker.WorkerConfig, flagVCPUs int, flagMemoryMB int64) error {
	if flagVCPUs > 0 {
		cfg.HardwareOverrides.VCPUs = int32(min(flagVCPUs, math.MaxInt32))
	}
	if flagMemoryMB > 0 {
		cfg.HardwareOverrides.MemoryMB = flagMemoryMB
	}
	detected, err := worker.DetectHardware("/proc", "/sys/fs/cgroup", cfg.WorkDir)
	if err != nil {
		return err
	}
	effective := worker.EffectiveHardware(detected, cfg.HardwareOverrides)
	effective.SandboxType = cfg.Hardware.SandboxType
	cfg.Hardware = effective
	fmt.Printf("Hardware detected: %s\n", worker.DescribeHardware(detected))
	fmt.Printf("Hardware effective: %s\n", worker.DescribeHardware(effective))
	return nil
}

func newSandbox(cfg worker.WorkerConfig, store worker.ObjectStore) *worker.ProcessSandbox {
	return &worker.ProcessSandbox{
		WorkDir:               cfg.WorkDir,
		StateDir:              cfg.StateDir,
		DiskDir:               cfg.DiskDir,
		CacheDir:              cfg.CacheDir,
		SandboxMode:           cfg.SandboxMode,
		ObsBuildBin:           cfg.ObsBuildBin,
		HostArch:              cfg.Architecture,
		KVMUser:               cfg.KVMUser,
		KVMVMArgs:             cfg.KVMVMArgs,
		KVMAio:                cfg.KVMAio,
		KVMCache:              cfg.KVMCache,
		S3Storage:             store,
		AllowUnisolatedChroot: cfg.AllowUnisolatedChroot,
		KeepFailedJobs:        cfg.KeepFailedJobs,
	}
}
