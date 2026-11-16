package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"gopkg.in/yaml.v3"
)

// RenderBuildConfig serializes a manifest.ProjectConfig into legacy obs-build .build.config format.
func RenderBuildConfig(cfg *manifest.ProjectConfig) string {
	return manifest.RenderBuildConfig(cfg)
}

type WorkerConfig struct {
	WorkerID string
	// Architecture is the host architecture.
	Architecture string
	// Archs are the architectures the worker advertises; empty means the
	// BSCando set of Architecture (see DefaultArchs).
	Archs       []string
	NatsURL     string
	S3Endpoint  string
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string
	WorkDir     string
	StateDir    string
	CacheDir    string
	DiskDir     string
	ObsBuildBin string
	Hardware    v1.WorkerHardware
	// HardwareOverrides are explicit capacity settings; they win over detection.
	HardwareOverrides HardwareOverrides
	SandboxMode       string
	KVMUser           string
	KVMVMArgs         string
	KVMAio            string
	KVMCache          string
	// NspawnArgs are the configured systemd-nspawn options; nil selects
	// DefaultNspawnArgs.
	NspawnArgs []string
	// AllowUnisolatedChroot lets BuildInfo jobs run in plain obs-build chroot
	// mode, which has no network isolation. Default deny.
	AllowUnisolatedChroot bool
	// KeepFailedJobs keeps the build root, rpmlist and bdeps of failed jobs
	// for debugging. Default false: every job directory is cleaned.
	KeepFailedJobs bool
	// ConstrainedOnly makes the worker take only jobs with a sandbox
	// constraint (sandbox.accept_unconstrained: false).
	ConstrainedOnly bool
}

// BuildArchs returns the architectures the worker advertises: the explicit
// list, else the BSCando set of the host architecture.
func (c WorkerConfig) BuildArchs() []string {
	if len(c.Archs) > 0 {
		return append([]string(nil), c.Archs...)
	}
	return DefaultArchs(c.Architecture)
}

type WorkerConfigFile struct {
	Worker struct {
		ID   string `yaml:"id"`
		Arch string `yaml:"arch"`
		// Archs lists the architectures this worker builds, for example
		// [x86_64, i586]. Default: everything the host can build (BSCando).
		Archs    []string `yaml:"archs"`
		WorkDir  string   `yaml:"work_dir"`
		StateDir string   `yaml:"state_dir"`
		CacheDir string   `yaml:"cache_dir"`
		DiskDir  string   `yaml:"disk_dir"`
		// Hardware overrides. Unset values are detected at startup.
		TotalVCPUs      int32  `yaml:"total_vcpus"`
		TotalMemoryMB   int64  `yaml:"total_memory_mb"`
		DiskGB          int64  `yaml:"disk_gb"`
		ReserveMemoryMB *int64 `yaml:"reserve_memory_mb"`
		ReserveVCPUs    *int32 `yaml:"reserve_vcpus"`
	} `yaml:"worker"`
	Nats struct {
		URL string `yaml:"url"`
	} `yaml:"nats"`
	Storage struct {
		S3Endpoint string `yaml:"s3_endpoint"`
		S3Bucket   string `yaml:"s3_bucket"`
		S3Region   string `yaml:"s3_region"`
		AccessKey  string `yaml:"access_key"`
		SecretKey  string `yaml:"secret_key"`
	} `yaml:"storage"`
	Sandbox struct {
		Mode        string `yaml:"mode"`
		ObsBuildBin string `yaml:"obs_build_bin"`
		// AllowUnisolatedChroot permits hermetic (BuildInfo) jobs in chroot
		// mode, which cannot isolate the network. Default false.
		AllowUnisolatedChroot bool `yaml:"allow_unisolated_chroot"`
		// NspawnArgs are extra systemd-nspawn options for mode nspawn. When set
		// (even to an empty list) they replace the defaults, see
		// DefaultNspawnArgs; {hostname} and {proc_filesystems} are expanded.
		NspawnArgs []string `yaml:"nspawn_args"`
		// KeepFailedJobs keeps the build root of failed jobs for debugging.
		KeepFailedJobs bool `yaml:"keep_failed_jobs"`
		// AcceptUnconstrained says whether the worker also takes jobs
		// without a sandbox constraint. Default true; false keeps the
		// worker (typically the KVM one) for the jobs that require it.
		AcceptUnconstrained *bool `yaml:"accept_unconstrained"`
		KVM                 struct {
			VMType    string `yaml:"vm_type"`
			Device    string `yaml:"device"`
			Kernel    string `yaml:"kernel"`
			Initrd    string `yaml:"initrd"`
			MemoryMB  int    `yaml:"memory_mb"`
			VCPUs     int    `yaml:"vcpus"`
			User      string `yaml:"user"`
			VMArgs    string `yaml:"vm_args"`
			AIO       string `yaml:"aio"`
			CacheMode string `yaml:"cache_mode"`
		} `yaml:"kvm"`
		Podman struct {
			VMType string `yaml:"vm_type"`
			Image  string `yaml:"image"`
			Memory string `yaml:"memory"`
			VCPUs  int    `yaml:"vcpus"`
		} `yaml:"podman"`
	} `yaml:"sandbox"`
}

func LoadConfigFile(path string) (*WorkerConfigFile, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read worker config: %w", err)
	}

	expandedData := os.ExpandEnv(string(data))
	var cfg WorkerConfigFile
	if err := yaml.Unmarshal([]byte(expandedData), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse worker config: %w", err)
	}
	return &cfg, nil
}

type ExecutionResult struct {
	ExitCode          int
	Artifacts         []v1.ChrootRPM
	PreinstallSeconds float64
	MainBuildSeconds  float64
	TotalSeconds      float64
	// SourceCommit is the exact source commit the build ran on (provenance).
	SourceCommit string
	// Meta and Release echo BuildInfo.Meta and BuildInfo.Release.
	Meta    string
	Release string
	// Aborted marks a job stopped from the outside (timeout, log idle limit,
	// abort request, killed by a signal) as opposed to a genuine build failure.
	Aborted bool
}

type Sandbox interface {
	Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error)
	Cleanup(ctx context.Context) error
}
