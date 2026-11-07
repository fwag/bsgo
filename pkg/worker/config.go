package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"gopkg.in/yaml.v3"
)

type WorkerConfig struct {
	WorkerID     string
	Architecture string
	NatsURL      string
	S3Endpoint   string
	S3Bucket     string
	WorkDir      string
	CacheDir     string
	Hardware     v1.WorkerHardware
	SandboxMode  string
}

type WorkerConfigFile struct {
	Worker struct {
		ID       string `yaml:"id"`
		Arch     string `yaml:"arch"`
		WorkDir  string `yaml:"work_dir"`
		CacheDir string `yaml:"cache_dir"`
	} `yaml:"worker"`
	Nats struct {
		URL string `yaml:"url"`
	} `yaml:"nats"`
	Storage struct {
		S3Endpoint string `yaml:"s3_endpoint"`
		S3Bucket   string `yaml:"s3_bucket"`
		S3Region   string `yaml:"s3_region"`
	} `yaml:"storage"`
	Sandbox struct {
		Mode        string `yaml:"mode"`
		ObsBuildBin string `yaml:"obs_build_bin"`
		KVM         struct {
			VMType   string `yaml:"vm_type"`
			Device   string `yaml:"device"`
			Kernel   string `yaml:"kernel"`
			Initrd   string `yaml:"initrd"`
			MemoryMB int    `yaml:"memory_mb"`
			VCPUs    int    `yaml:"vcpus"`
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

	var cfg WorkerConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
}

type Sandbox interface {
	Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error)
	Cleanup(ctx context.Context) error
}
