package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"gopkg.in/yaml.v3"
)

// RenderBuildConfig serializes a manifest.ProjectConfig into legacy obs-build .build.config format.
func RenderBuildConfig(cfg *manifest.ProjectConfig) string {
	if cfg == nil {
		return ""
	}
	var b strings.Builder

	if len(cfg.Preinstall) > 0 {
		fmt.Fprintf(&b, "Preinstall: %s\n", strings.Join(cfg.Preinstall, " "))
	}
	if len(cfg.VMinstall) > 0 {
		fmt.Fprintf(&b, "VMinstall: %s\n", strings.Join(cfg.VMinstall, " "))
	}
	if len(cfg.Order) > 0 {
		fmt.Fprintf(&b, "Order: %s\n", strings.Join(cfg.Order, " "))
	}
	if cfg.BuildEngine != "" {
		fmt.Fprintf(&b, "BuildEngine: %s\n", cfg.BuildEngine)
	}
	if len(cfg.BuildTypes) > 0 {
		fmt.Fprintf(&b, "Type: %s\n", cfg.BuildTypes[0])
	}
	if len(cfg.RepoTypes) > 0 {
		fmt.Fprintf(&b, "Repotype: %s\n", strings.Join(cfg.RepoTypes, " "))
	}
	for _, p := range cfg.Prefer {
		fmt.Fprintf(&b, "Prefer: %s\n", p)
	}
	for _, ign := range cfg.Ignore {
		fmt.Fprintf(&b, "Ignore: %s\n", ign)
	}
	for k, v := range cfg.Substitute {
		fmt.Fprintf(&b, "Substitute: %s %s\n", k, strings.Join(v, " "))
	}
	for _, pair := range cfg.Conflict {
		if len(pair) > 0 {
			fmt.Fprintf(&b, "Conflict: %s\n", strings.Join(pair, " "))
		}
	}
	for arch, opt := range cfg.Optflags {
		fmt.Fprintf(&b, "Optflags: %s %s\n", arch, opt)
	}
	for _, f := range cfg.BuildFlags {
		fmt.Fprintf(&b, "buildflags: %s\n", f)
	}
	for _, f := range cfg.PublishFlags {
		fmt.Fprintf(&b, "publishflags: %s\n", f)
	}
	if cfg.Macros != "" {
		fmt.Fprintf(&b, "Macros:\n%s\n:macros\n", strings.TrimSpace(cfg.Macros))
	}

	return b.String()
}

type WorkerConfig struct {
	WorkerID     string
	Architecture string
	NatsURL      string
	S3Endpoint   string
	S3Bucket     string
	S3Region     string
	S3AccessKey  string
	S3SecretKey  string
	WorkDir      string
	CacheDir     string
	DiskDir      string
	ObsBuildBin  string
	Hardware     v1.WorkerHardware
	SandboxMode  string
}

type WorkerConfigFile struct {
	Worker struct {
		ID       string `yaml:"id"`
		Arch     string `yaml:"arch"`
		WorkDir  string `yaml:"work_dir"`
		CacheDir string `yaml:"cache_dir"`
		DiskDir  string `yaml:"disk_dir"`
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
}

type Sandbox interface {
	Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error)
	Cleanup(ctx context.Context) error
}
